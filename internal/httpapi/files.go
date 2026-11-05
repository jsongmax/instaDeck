package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"mime"
	"net/http"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"instadeck/internal/account"
	"instadeck/internal/instabox"
)

const maxFilePart = 64 << 20

var uploadIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

func validFileText(s string, max int) bool {
	if !utf8.ValidString(s) || len(s) > max {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
func validFilePath(s string) bool {
	if !validFileText(s, 4096) || !strings.HasPrefix(s, "/") {
		return false
	}
	if s == "/" {
		return true
	}
	for _, part := range strings.Split(s[1:], "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}
func validFileName(s string) bool {
	return s != "" && s != "." && s != ".." && !strings.Contains(s, "/") && validFileText(s, 255)
}
func protectedFilePath(s string) bool {
	return s == "/" || strings.Count(s, "/") == 1 || s == "/data/home"
}
func validUploadID(s string) bool { return uploadIDPattern.MatchString(s) }

const mergeScript = `dir=$1
target=$2
parts=$3
size=$4
overwrite=$5
[ -d "$dir" ] && [ ! -L "$dir" ] || exit 13
if [ -e "$target" ] || [ -L "$target" ]; then
 [ -f "$target" ] && [ ! -L "$target" ] || exit 11
 [ "$overwrite" = 1 ] || exit 12
fi
rm -f -- "$dir/merged" || exit 15
( set -C; : > "$dir/merged" ) || exit 15
for ((i=0;i<parts;i++)); do
 [ -f "$dir/$i" ] && [ ! -L "$dir/$i" ] || exit 13
 cat -- "$dir/$i" >> "$dir/merged" || exit 15
done
actual=$(wc -c < "$dir/merged") || exit 15
[ "$actual" -eq "$size" ] || exit 14
if [ "$overwrite" = 1 ]; then
 [ ! -e "$target" ] || chmod --reference="$target" -- "$dir/merged" || exit 15
 mv -fT -- "$dir/merged" "$target" || exit 15
elif ! ln -T -- "$dir/merged" "$target"; then
 [ -e "$target" ] || [ -L "$target" ] && exit 12
 exit 15
fi
rm -rf -- "$dir" || exit 15
`

func fileFailure(code string) error { return &account.Failure{Code: code} }
func fileCode(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "file_operation_timeout"
	}
	var limit *http.MaxBytesError
	if errors.As(err, &limit) {
		return "file_too_large"
	}
	var up *instabox.APIError
	if errors.As(err, &up) {
		switch up.StatusCode {
		case 400:
			return "file_operation_failed"
		case 404:
			return "file_not_found"
		case 409:
			if up.Code == "not_a_file" {
				return "file_not_a_file"
			}
			if up.Code == "machine_not_running" {
				return up.Code
			}
		case 403:
			if up.Code == "missing_scope" {
				return up.Code
			}
			return "file_permission_denied"
		case 413:
			return "file_too_large"
		case 507:
			return "file_no_space"
		case 429:
			return "rate_limited"
		}
	}
	return account.FilterError(err).Code
}

// resolvesToProtected follows symlinks in the parent, since a link elsewhere could
// otherwise reach a protected directory. The last component is not followed: rm and
// mv act on a link itself.
func resolvesToProtected(ctx context.Context, c account.ResourceClient, id, p string) (bool, error) {
	v, err := fileExec(ctx, c, id, []string{"realpath", "-e", "--", path.Dir(p)}, true)
	if err != nil {
		return false, err
	}
	if v.ExitCode != 0 {
		return false, execFailure(v)
	}
	parent := strings.TrimSuffix(v.Stdout, string(rune(10)))
	if !validFilePath(parent) {
		return false, fileFailure("file_operation_failed")
	}
	return protectedFilePath(path.Join(parent, path.Base(p))), nil
}

func mergeArgv(dir, target string, parts int, size int64, overwrite bool) []string {
	flag := "0"
	if overwrite {
		flag = "1"
	}
	return []string{"bash", "-c", mergeScript, "instadeck-merge", dir, target, strconv.Itoa(parts), strconv.FormatInt(size, 10), flag}
}

func mergeFailure(v instabox.ExecResult) error {
	switch v.ExitCode {
	case 0:
		return nil
	case 11:
		return fileFailure("file_target_invalid")
	case 12:
		return fileFailure("file_exists")
	case 13, 14:
		return fileFailure("file_upload_incomplete")
	}
	return execFailure(v)
}

func newUploadID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

func fileExec(ctx context.Context, c account.ResourceClient, id string, argv []string, noHistory bool) (instabox.ExecResult, error) {
	return c.Exec(ctx, id, instabox.ExecRequest{Argv: argv, Envs: map[string]string{"LC_ALL": "C"}, TimeoutSeconds: 600, NoHistory: noHistory})
}
func execFailure(v instabox.ExecResult) error {
	if v.ExitCode == 124 {
		return fileFailure("file_operation_timeout")
	}
	for _, p := range []struct{ text, code string }{{"Permission denied", "file_permission_denied"}, {"No space left", "file_no_space"}, {"No such file", "file_not_found"}, {"File exists", "file_exists"}, {"not replacing", "file_exists"}} {
		if strings.Contains(v.Stderr, p.text) {
			return fileFailure(p.code)
		}
	}
	return fileFailure("file_operation_failed")
}

type uploadReader struct {
	reader io.Reader
	err    error
	cancel context.CancelFunc
}

func (u *uploadReader) Read(p []byte) (int, error) {
	n, e := u.reader.Read(p)
	if e != nil && e != io.EOF {
		u.err = e
		u.cancel()
	}
	return n, e
}

type fileInput struct {
	CredentialID string `json:"credentialId"`
	Path         string `json:"path"`
	UploadID     string `json:"uploadId"`
	Index        int    `json:"index"`
	Parts        int    `json:"parts"`
	Size         int64  `json:"size"`
	Overwrite    bool   `json:"overwrite"`
	NewName      string `json:"newName"`
	ConfirmPath  string `json:"confirmPath"`
}

func (api *API) files(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	input := fileInput{CredentialID: q.Get("credentialId"), Path: q.Get("path"), UploadID: q.Get("uploadId")}
	action := strings.TrimPrefix(r.URL.Path, "/api/v1/machines/"+chi.URLParam(r, "id")+"/files")
	if r.Method == "POST" && !decode(w, r, &input, 16384) {
		return
	}
	id := chi.URLParam(r, "id")
	if _, _, err := api.visibleMachine(r, input.CredentialID); err != nil {
		fail(w, r, 404, "machine_not_visible")
		return
	}
	reject := func(code string) { fail(w, r, 400, code) }
	if !validFilePath(input.Path) {
		reject("file_path_invalid")
		return
	}
	if action == "/delete" {
		if !api.sensitive(w, r) {
			return
		}
		if protectedFilePath(input.Path) {
			reject("file_protected_path")
			return
		}
		if input.ConfirmPath != input.Path {
			reject("file_path_invalid")
			return
		}
	}
	if action == "/rename" {
		if protectedFilePath(input.Path) {
			reject("file_protected_path")
			return
		}
		if !validFileName(input.NewName) {
			reject("file_name_invalid")
			return
		}
	}
	taskDir := ""
	if action == "/parts" || strings.HasPrefix(action, "/uploads/") {
		if !validUploadID(input.UploadID) {
			reject("file_upload_invalid")
			return
		}
		taskDir = path.Join(path.Dir(input.Path), ".instadeck-upload-"+input.UploadID)
		if !validFilePath(taskDir) {
			reject("file_path_invalid")
			return
		}
	}
	if r.Method == "PUT" {
		if r.ContentLength < 0 {
			reject("file_upload_invalid")
			return
		}
		if r.ContentLength > maxFilePart {
			reject("file_too_large")
			return
		}
		if action == "/parts" {
			n, e := strconv.Atoi(q.Get("index"))
			if e != nil || n < 0 || n >= 400 {
				reject("file_upload_invalid")
				return
			}
			input.Index = n
		} else {
			if q.Get("overwrite") != "0" && q.Get("overwrite") != "1" {
				reject("file_upload_invalid")
				return
			}
			input.Overwrite = q.Get("overwrite") == "1"
		}
		if err := http.NewResponseController(w).SetReadDeadline(time.Now().Add(2 * time.Hour)); err != nil && !errors.Is(err, http.ErrNotSupported) {
			reject("file_upload_invalid")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxFilePart)
	}
	if action == "/uploads/complete" && (input.Parts < 1 || input.Parts > 400 || input.Size < 0 || input.Size > 20<<30) {
		reject("file_upload_invalid")
		return
	}
	event := ""
	switch {
	case r.Method == "GET" && action == "/content":
		event = "file.download"
	case r.Method == "PUT" && action == "/content", action == "/uploads/complete":
		event = "file.upload"
	case action == "/folders":
		event = "file.mkdir"
	case action == "/rename":
		event = "file.rename"
	case action == "/delete":
		event = "file.delete"
	}
	if event != "" && !api.requireAudit(w, r, event, input.CredentialID, id) {
		return
	}
	duration := 610 * time.Second
	if r.Method == "PUT" || r.Method == "GET" {
		duration = 2 * time.Hour
	}
	ctx, cancel := context.WithTimeout(r.Context(), duration)
	defer cancel()
	var result any
	var body io.ReadCloser
	var length int64
	err := api.dependencies.Accounts.WithResources(ctx, input.CredentialID, func(a account.DTO, c account.ResourceClient) error {
		if r.Method != "GET" && a.Capabilities.Write == account.Denied {
			return fileFailure("capability_denied")
		}
		switch {
		case r.Method == "GET" && action == "":
			v, e := c.ListFiles(ctx, id, input.Path)
			result = v
			return e
		case r.Method == "GET":
			var e error
			body, length, e = c.DownloadFile(ctx, id, input.Path)
			return e
		case r.Method == "PUT":
			target := input.Path
			if action == "/parts" {
				target = path.Join(taskDir, strconv.Itoa(input.Index))
			} else if !input.Overwrite {
				v, e := fileExec(ctx, c, id, []string{"bash", "-c", `[ -e "$1" ] || [ -L "$1" ]`, "instadeck-check", target}, true)
				if e != nil {
					return e
				}
				if v.ExitCode == 0 {
					return fileFailure("file_exists")
				}
				if v.ExitCode != 1 {
					return execFailure(v)
				}
			}
			// Without consent to overwrite, publish through the merge so that the
			// final step is an atomic no-clobber link. The check above only saves
			// sending a file that is already known to conflict.
			publishDir := ""
			if action != "/parts" && !input.Overwrite {
				uploadID, e := newUploadID()
				if e != nil {
					return fileFailure("file_operation_failed")
				}
				publishDir = path.Join(path.Dir(input.Path), ".instadeck-upload-"+uploadID)
				target = path.Join(publishDir, "0")
			}
			if !validFilePath(target) {
				return fileFailure("file_path_invalid")
			}
			upload := &uploadReader{reader: r.Body, cancel: cancel}
			v, e := c.WriteFile(ctx, id, target, action == "/parts" || publishDir != "", upload, r.ContentLength)
			if publishDir != "" {
				// Runs even when the request was cancelled, so nothing is left behind.
				defer func() {
					cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
					defer stop()
					_, _ = fileExec(cleanup, c, id, []string{"rm", "-rf", "--one-file-system", "--", publishDir}, true)
				}()
			}
			if upload.err != nil {
				var limit *http.MaxBytesError
				if errors.As(upload.err, &limit) {
					return fileFailure("file_too_large")
				}
				return fileFailure("file_upload_invalid")
			}
			if e != nil {
				return e
			}
			if action == "/parts" {
				result = map[string]any{"size": v.Size, "sha256": v.SHA256}
				return nil
			}
			if publishDir != "" {
				merged, e := fileExec(ctx, c, id, mergeArgv(publishDir, input.Path, 1, v.Size, false), true)
				if e != nil {
					return e
				}
				if e = mergeFailure(merged); e != nil {
					return e
				}
				v.Path, v.Created = input.Path, true
			}
			result = v
			return nil
		default:
			if action == "/delete" || action == "/rename" {
				protected, e := resolvesToProtected(ctx, c, id, input.Path)
				if e != nil {
					return e
				}
				if protected {
					return fileFailure("file_protected_path")
				}
			}
			var argv []string
			switch action {
			case "/folders":
				argv = []string{"mkdir", "--", input.Path}
				result = map[string]string{"path": input.Path}
			case "/rename":
				target := path.Join(path.Dir(input.Path), input.NewName)
				if !validFilePath(target) {
					return fileFailure("file_path_invalid")
				}
				argv = []string{"mv", "--no-clobber", "--no-target-directory", "--verbose", "--", input.Path, target}
				result = map[string]string{"path": target}
			case "/delete":
				argv = []string{"rm", "-rf", "--one-file-system", "--", input.Path}
				result = map[string]bool{"ok": true}
			case "/uploads/abort":
				argv = []string{"rm", "-rf", "--one-file-system", "--", taskDir}
				result = map[string]bool{"ok": true}
			case "/uploads/complete":
				argv = mergeArgv(taskDir, input.Path, input.Parts, input.Size, input.Overwrite)
				result = map[string]any{"path": input.Path, "size": input.Size}
			default:
				return fileFailure("file_operation_failed")
			}
			v, e := fileExec(ctx, c, id, argv, strings.HasPrefix(action, "/uploads/"))
			if e != nil {
				return e
			}
			if action == "/uploads/complete" {
				return mergeFailure(v)
			}
			if v.ExitCode != 0 {
				return execFailure(v)
			}
			if action == "/rename" && v.Stdout == "" {
				return fileFailure("file_exists")
			}
			return nil
		}
	})
	if event != "" {
		outcome, detail := "success", ""
		if err != nil {
			outcome = "failure"
			detail = fileCode(err)
		}
		_ = api.eventDetail(r, event, outcome, input.CredentialID, id, detail)
	}
	if err != nil {
		code := fileCode(err)
		status := 400
		if code == "file_exists" {
			status = 409
		}
		fail(w, r, status, code)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if body != nil {
		defer body.Close()
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": path.Base(input.Path)}))
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
		if length >= 0 {
			w.Header().Set("Content-Length", strconv.FormatInt(length, 10))
		}
		n, e := io.Copy(w, body)
		if e != nil || length >= 0 && n != length {
			panic(http.ErrAbortHandler)
		}
		return
	}
	writeJSON(w, 200, result)
}
