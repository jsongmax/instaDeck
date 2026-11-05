package httpapi

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestFileValidators(t *testing.T) {
	for _, s := range []string{"", ".", "..", "relative", "//", "/a/", "/a/../b", "/./a", "/a//b", "/a\x00", "/a\n", "/\xff", "/" + strings.Repeat("a", 4096)} {
		if validFilePath(s) {
			t.Errorf("accepted invalid path %q", s)
		}
	}
	for _, s := range []string{"/", "/-name", "/a b/$(x)'"} {
		if !validFilePath(s) {
			t.Errorf("rejected valid path %q", s)
		}
	}
	for _, s := range []string{"", ".", "..", "a/b", "a\n", "\xff", strings.Repeat("a", 256)} {
		if validFileName(s) {
			t.Errorf("accepted invalid name %q", s)
		}
	}
	if !validFileName("-name") {
		t.Fatal("leading dash")
	}
	for _, s := range []string{"/", "/data", "/etc", "/data/home"} {
		if !protectedFilePath(s) {
			t.Fatal("unprotected", s)
		}
	}
	if protectedFilePath("/data/home/a") {
		t.Fatal("overprotected")
	}
	for _, s := range []string{"", "..", ".", "//", "abc/", "\xff", strings.Repeat("A", 32), strings.Repeat("a", 33), strings.Repeat("a", 31), strings.Repeat("a", 31) + "\n"} {
		if validUploadID(s) {
			t.Fatal("invalid upload ID")
		}
	}
	if !validUploadID(strings.Repeat("a", 32)) {
		t.Fatal("valid upload ID")
	}
}
func TestMergeScript(t *testing.T) {
	bash, e := exec.LookPath("bash")
	if e != nil {
		t.Skip("bash unavailable")
	}
	if e = exec.Command(bash, "-c", "true").Run(); e != nil {
		t.Skip("bash cannot execute")
	}
	for _, tc := range []struct {
		name      string
		code      int
		overwrite bool
	}{{"success", 0, false}, {"missing", 13, false}, {"size", 14, false}, {"exists", 12, false}, {"overwrite", 0, true}, {"directory", 11, true}, {"symlink", 11, true}, {"injection", 0, false}} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, "parts")
			if e := os.Mkdir(dir, 0700); e != nil {
				t.Fatal(e)
			}
			target := filepath.Join(root, "-space ' $(touch injected)")
			if tc.name != "missing" {
				if e := os.WriteFile(filepath.Join(dir, "0"), []byte("abc"), 0600); e != nil {
					t.Fatal(e)
				}
			}
			switch tc.name {
			case "exists", "overwrite":
				_ = os.WriteFile(target, []byte("old"), 0600)
			case "directory":
				_ = os.Mkdir(target, 0700)
			case "symlink":
				if e := os.Symlink(filepath.Join(root, "absent"), target); e != nil {
					t.Skip("symlinks unavailable")
				}
			}
			size := 3
			if tc.name == "size" {
				size = 4
			}
			ow := "0"
			if tc.overwrite {
				ow = "1"
			}
			cmd := exec.Command(bash, "-c", mergeScript, "instadeck-merge", filepath.ToSlash(dir), filepath.ToSlash(target), "1", strconv.Itoa(size), ow)
			cmd.Dir = root
			e := cmd.Run()
			code := 0
			if e != nil {
				var ok bool
				exit, yes := e.(*exec.ExitError)
				ok = yes
				if !ok {
					t.Fatal(e)
				}
				code = exit.ExitCode()
			}
			if code != tc.code {
				t.Fatalf("exit %d want %d", code, tc.code)
			}
			if _, e := os.Stat(filepath.Join(root, "injected")); !os.IsNotExist(e) {
				t.Fatal("injection created file")
			}
			if code == 0 {
				b, e := os.ReadFile(target)
				if e != nil || string(b) != "abc" {
					t.Fatal("wrong content")
				}
				if _, e := os.Stat(dir); !os.IsNotExist(e) {
					t.Fatal("parts retained")
				}
			}
		})
	}
}
