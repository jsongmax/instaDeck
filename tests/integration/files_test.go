package integration_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"instadeck/internal/account"
	"instadeck/internal/instabox"
)

func TestFilesSlowUpload(t *testing.T) {
	f := newFixture(t, false)
	cookie, csrf := f.login(t)
	a := f.addAccount(t, cookie, csrf)
	f.resource = func(w http.ResponseWriter, r *http.Request) bool {
		if r.Method == "PUT" && strings.HasSuffix(r.URL.Path, "/files") {
			b, e := io.ReadAll(r.Body)
			if e != nil {
				t.Error(e)
			}
			respond(w, 200, instabox.FileWritten{Path: "/tmp/test", Size: int64(len(b)), SHA256: "hash"})
			return true
		}
		return false
	}
	server := httptest.NewUnstartedServer(f.api)
	server.Config.ReadTimeout = 100 * time.Millisecond
	server.Start()
	defer server.Close()
	for _, suffix := range []string{"/content?overwrite=1", "/parts?uploadId=" + strings.Repeat("a", 32) + "&index=0"} {
		reader, writer := io.Pipe()
		req, e := http.NewRequest("PUT", server.URL+"/api/v1/machines/machine/files"+suffix+"&path=/tmp/test&credentialId="+a.CredentialID, reader)
		if e != nil {
			t.Fatal(e)
		}
		req.ContentLength = 2
		req.AddCookie(cookie)
		req.Header.Set("Origin", f.dependencies.Config.Origin)
		req.Header.Set("X-CSRF-Token", csrf)
		go func() {
			defer writer.Close()
			_, _ = writer.Write([]byte("a"))
			time.Sleep(250 * time.Millisecond)
			_, _ = writer.Write([]byte("b"))
		}()
		response, e := server.Client().Do(req)
		if e != nil {
			t.Fatal(e)
		}
		b, _ := io.ReadAll(response.Body)
		response.Body.Close()
		if response.StatusCode != 200 {
			t.Fatal(response.StatusCode, string(b))
		}
	}
}
func TestFilesAuthorization(t *testing.T) {
	f := newFixture(t, false)
	cookie, csrf := f.login(t)
	a := f.addAccount(t, cookie, csrf)
	for _, route := range []struct{ method, suffix string }{{"GET", ""}, {"GET", "/content"}, {"PUT", "/content"}, {"PUT", "/parts"}, {"POST", "/uploads/complete"}, {"POST", "/uploads/abort"}, {"POST", "/folders"}, {"POST", "/rename"}, {"POST", "/delete"}} {
		url := "/api/v1/machines/machine/files" + route.suffix + "?path=/tmp/test&credentialId=" + a.CredentialID
		body := fmt.Sprintf(`{"credentialId":%q,"path":"/tmp/test"}`, a.CredentialID)
		if r := f.request(route.method, url, body, nil, csrf, f.dependencies.Config.Origin); r.Code != 401 {
			t.Fatal(route, r.Code)
		}
		if route.method != "GET" {
			if r := f.request(route.method, url, body, cookie, "", f.dependencies.Config.Origin); r.Code != 403 {
				t.Fatal(route, r.Code)
			}
		}
	}
}
func TestFilesErrorsAndAudit(t *testing.T) {
	f := newFixture(t, false)
	cookie, csrf := f.login(t)
	a := f.addAccount(t, cookie, csrf)
	base := "/api/v1/machines/machine/files"
	upstreamStatus, upstreamCode, exit, stderr, stdout := 200, "", 0, "", "ok"
	calls := 0
	f.resource = func(w http.ResponseWriter, r *http.Request) bool {
		if !strings.Contains(r.URL.Path, "/files") && !strings.HasSuffix(r.URL.Path, "/exec") {
			return false
		}
		calls++
		if upstreamStatus != 200 {
			respond(w, upstreamStatus, map[string]any{"error": map[string]string{"code": upstreamCode, "message": "/private/path secret-stderr"}})
			return true
		}
		if strings.HasSuffix(r.URL.Path, "/exec") {
			var input map[string]any
			if e := json.NewDecoder(r.Body).Decode(&input); e != nil {
				t.Error(e)
			}
			if _, ok := input["cmd"]; ok {
				t.Error("cmd sent")
			}
			if _, ok := input["argv"]; !ok {
				t.Error("argv missing")
			}
			if argv, _ := input["argv"].([]any); len(argv) > 0 && argv[0] == "realpath" {
				respond(w, 200, instabox.ExecResult{Stdout: "/tmp" + string(rune(10))})
				return true
			}
			respond(w, 200, instabox.ExecResult{ExitCode: exit, Stdout: stdout, Stderr: stderr})
			return true
		}
		if strings.HasSuffix(r.URL.Path, "/content") {
			w.Header().Set("Content-Length", "3")
			w.Header().Set("X-Upstream-Secret", "secret")
			_, _ = io.WriteString(w, "abc")
			return true
		}
		respond(w, 200, map[string]any{"path": "/tmp/test", "type": "dir", "private": "secret"})
		return true
	}
	for _, tc := range []struct {
		status     int
		code, want string
	}{{404, "", "file_not_found"}, {409, "not_a_file", "file_not_a_file"}, {409, "machine_not_running", "machine_not_running"}, {403, "", "file_permission_denied"}, {413, "", "file_too_large"}, {507, "", "file_no_space"}, {429, "", "rate_limited"}} {
		upstreamStatus, upstreamCode = tc.status, tc.code
		r := f.request("GET", base+"?credentialId="+a.CredentialID+"&path=/tmp/test", "", cookie, "", "")
		if !strings.Contains(r.Body.String(), tc.want) || strings.Contains(r.Body.String(), "/private") {
			t.Fatal(tc, r.Body)
		}
	}
	upstreamStatus = 200
	r := f.request("GET", base+"/content?credentialId="+a.CredentialID+"&path=/tmp/test", "", cookie, "", "")
	if r.Code != 200 || r.Body.String() != "abc" || r.Header().Get("Content-Security-Policy") != "sandbox; default-src 'none'" || r.Header().Get("X-Upstream-Secret") != "" || !strings.Contains(r.Header().Get("Content-Disposition"), "attachment") {
		t.Fatal("download", r.Code, r.Header())
	}
	post := func(suffix, path string) *httptest.ResponseRecorder {
		return f.request("POST", base+suffix, fmt.Sprintf(`{"credentialId":%q,"path":%q,"confirmPath":%q,"newName":"-new","uploadId":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","parts":1,"size":3}`, a.CredentialID, path, path), cookie, csrf, f.dependencies.Config.Origin)
	}
	before := calls
	r = post("/delete", "/data/home")
	if !strings.Contains(r.Body.String(), "file_protected_path") || calls != before {
		t.Fatal("protected", r.Body)
	}
	for _, tc := range []struct {
		exit int
		want string
	}{{11, "file_target_invalid"}, {12, "file_exists"}, {13, "file_upload_incomplete"}, {14, "file_upload_incomplete"}} {
		exit = tc.exit
		r = post("/uploads/complete", "/tmp/test")
		if !strings.Contains(r.Body.String(), tc.want) {
			t.Fatal(tc, r.Body)
		}
	}
	exit = 1
	stderr = "Permission denied /private/path secret-stderr"
	r = post("/folders", "/tmp/test")
	if !strings.Contains(r.Body.String(), "file_permission_denied") || strings.Contains(r.Body.String(), "secret-stderr") {
		t.Fatal(r.Body)
	}
	exit = 0
	stdout = ""
	r = post("/rename", "/tmp/test")
	if !strings.Contains(r.Body.String(), "file_exists") {
		t.Fatal(r.Body)
	}
	records, _ := f.store.ReadAuditEvents(context.Background(), 0, 1000)
	raw, _ := json.Marshal(records)
	if strings.Contains(string(raw), "/tmp/test") || strings.Contains(string(raw), "secret-stderr") {
		t.Fatal("audit leaked path")
	}
	f.clock.Add(301)
	r = post("/delete", "/tmp/test")
	if r.Code != 403 {
		t.Fatal("sensitive bypass", r.Code)
	}
}

func TestFilesVisibilityAndDenied(t *testing.T) {
	f := newFixture(t, false)
	cookie, csrf := f.login(t)
	a := f.addAccount(t, cookie, csrf)
	base := "/api/v1/machines/machine/files"
	r := f.request("GET", base+"?credentialId=unknown&path=/tmp/test", "", cookie, "", "")
	if r.Code != 404 || !strings.Contains(r.Body.String(), "machine_not_visible") {
		t.Fatal(r.Code, r.Body)
	}
	if e := f.dependencies.Accounts.Observe(context.Background(), a.CredentialID, a.Version, account.Write, &instabox.APIError{StatusCode: 403, Code: "missing_scope"}); e != nil {
		t.Fatal(e)
	}
	calls := 0
	f.resource = func(w http.ResponseWriter, r *http.Request) bool {
		if strings.HasSuffix(r.URL.Path, "/exec") {
			calls++
			respond(w, 200, instabox.ExecResult{})
			return true
		}
		return false
	}
	r = f.request("POST", base+"/folders", fmt.Sprintf(`{"credentialId":%q,"path":"/tmp/test"}`, a.CredentialID), cookie, csrf, f.dependencies.Config.Origin)
	if !strings.Contains(r.Body.String(), "capability_denied") || calls != 0 {
		t.Fatal(r.Code, r.Body, calls)
	}
}

func TestFileRenameRefusesProtectedPathAndReportsMissingScope(t *testing.T) {
	f := newFixture(t, false)
	cookie, csrf := f.login(t)
	a := f.addAccount(t, cookie, csrf)
	origin := f.dependencies.Config.Origin
	var calls atomic.Int32
	f.resource = func(w http.ResponseWriter, r *http.Request) bool {
		if strings.HasSuffix(r.URL.Path, "/exec") {
			calls.Add(1)
		}
		if strings.HasSuffix(r.URL.Path, "/files") {
			respond(w, 403, map[string]any{"error": map[string]any{"code": "missing_scope"}})
			return true
		}
		return false
	}
	for _, p := range []string{"/data", "/data/home"} {
		body := fmt.Sprintf(`{"credentialId":%q,"path":%q,"newName":"x"}`, a.CredentialID, p)
		r := f.request("POST", "/api/v1/machines/machine/files/rename", body, cookie, csrf, origin)
		if r.Code != 400 || !strings.Contains(r.Body.String(), "file_protected_path") {
			t.Errorf("%s: %d %s", p, r.Code, r.Body)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("protected rename reached the machine")
	}
	r := f.request("GET", "/api/v1/machines/machine/files?path=/data/home&credentialId="+a.CredentialID, "", cookie, "", "")
	if !strings.Contains(r.Body.String(), "missing_scope") {
		t.Fatalf("scope: %d %s", r.Code, r.Body)
	}
}

func TestFileDownloadAbortsWhenTheStreamIsCutShort(t *testing.T) {
	f := newFixture(t, false)
	cookie, csrf := f.login(t)
	a := f.addAccount(t, cookie, csrf)
	f.resource = func(w http.ResponseWriter, r *http.Request) bool {
		if strings.HasSuffix(r.URL.Path, "/files/content") {
			w.Header().Set("Content-Length", "100")
			w.WriteHeader(200)
			_, _ = w.Write([]byte("short"))
			return true
		}
		return false
	}
	defer func() {
		if recover() != http.ErrAbortHandler {
			t.Fatal("a truncated download must abort the connection")
		}
	}()
	f.request("GET", "/api/v1/machines/machine/files/content?path=/data/home/a&credentialId="+a.CredentialID, "", cookie, "", "")
}

func TestFileDeleteRefusesProtectedTargetBehindSymlink(t *testing.T) {
	f := newFixture(t, false)
	cookie, csrf := f.login(t)
	a := f.addAccount(t, cookie, csrf)
	var commands []string
	f.resource = func(w http.ResponseWriter, r *http.Request) bool {
		if !strings.HasSuffix(r.URL.Path, "/exec") {
			return false
		}
		var input instabox.ExecRequest
		_ = json.NewDecoder(r.Body).Decode(&input)
		commands = append(commands, input.Argv[0])
		// The parent /tmp/alias is a link to /data.
		respond(w, 200, instabox.ExecResult{Stdout: "/data" + string(rune(10))})
		return true
	}
	body := fmt.Sprintf(`{"credentialId":%q,"path":"/tmp/alias/home","confirmPath":"/tmp/alias/home"}`, a.CredentialID)
	r := f.request("POST", "/api/v1/machines/machine/files/delete", body, cookie, csrf, f.dependencies.Config.Origin)
	if !strings.Contains(r.Body.String(), "file_protected_path") {
		t.Fatal(r.Code, r.Body)
	}
	if len(commands) != 1 || commands[0] != "realpath" {
		t.Fatalf("rm must not run: %v", commands)
	}
}

func TestSmallUploadPublishesWithoutClobbering(t *testing.T) {
	f := newFixture(t, false)
	cookie, csrf := f.login(t)
	a := f.addAccount(t, cookie, csrf)
	var steps []string
	var written string
	f.resource = func(w http.ResponseWriter, r *http.Request) bool {
		switch {
		case strings.HasSuffix(r.URL.Path, "/exec"):
			var input instabox.ExecRequest
			_ = json.NewDecoder(r.Body).Decode(&input)
			name := input.Argv[0]
			if name == "bash" {
				name = input.Argv[3]
			}
			steps = append(steps, name)
			code := 0
			if name == "instadeck-check" {
				code = 1
			}
			if name == "instadeck-merge" {
				if input.Argv[5] != "/data/home/a.txt" || input.Argv[8] != "0" {
					t.Errorf("merge argv %v", input.Argv[4:])
				}
				code = 12
			}
			respond(w, 200, instabox.ExecResult{ExitCode: code})
		case r.Method == "PUT":
			written = r.URL.Query().Get("path")
			steps = append(steps, "put")
			respond(w, 201, instabox.FileWritten{Path: written, Size: 1, SHA256: "x", Created: true})
		default:
			return false
		}
		return true
	}
	url := "/api/v1/machines/machine/files/content?credentialId=" + a.CredentialID + "&path=/data/home/a.txt&overwrite=0"
	r := f.request("PUT", url, "x", cookie, csrf, f.dependencies.Config.Origin)
	if r.Code != 409 || !strings.Contains(r.Body.String(), "file_exists") {
		t.Fatal(r.Code, r.Body)
	}
	if !strings.HasPrefix(written, "/data/home/.instadeck-upload-") {
		t.Fatalf("must not write to the target directly: %s", written)
	}
	if strings.Join(steps, ",") != "instadeck-check,put,instadeck-merge,rm" {
		t.Fatalf("steps %v", steps)
	}
}
