package keyring

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func writeFile(t *testing.T, data []byte, mode os.FileMode) string {
	t.Helper()
	dir, e := os.MkdirTemp(".", "test-keyring-")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	p := filepath.Join(dir, "keys.json")
	if e = os.WriteFile(p, data, mode); e != nil {
		t.Fatal(e)
	}
	return p
}
func TestEncryption(t *testing.T) {
	one, _ := GenerateKey(1)
	two, _ := GenerateKey(2)
	data, _ := json.Marshal(File{[]Key{one}})
	old, e := Load(writeFile(t, data, 0600))
	if e != nil {
		t.Fatal(e)
	}
	previous, _ := old.Encrypt([]byte("value"), []byte("purpose"))
	data, _ = json.Marshal(File{[]Key{one, two}})
	ring, e := Load(writeFile(t, data, 0600))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = ring.Decrypt(previous, []byte("purpose")); e != nil {
		t.Fatal(e)
	}
	a, _ := ring.Encrypt([]byte("value"), []byte("purpose"))
	b, _ := ring.Encrypt([]byte("value"), []byte("purpose"))
	if a.KeyVersion != 2 || string(a.Nonce) == string(b.Nonce) {
		t.Fatal("version or nonce")
	}
	if _, e = ring.Decrypt(a, []byte("wrong")); e == nil {
		t.Fatal("AAD")
	}
	a.Ciphertext[0] ^= 1
	if _, e = ring.Decrypt(a, []byte("purpose")); e == nil {
		t.Fatal("tampering")
	}
}
func TestFileFailures(t *testing.T) {
	for _, data := range []string{`{}`, `bad`, `{"keys":[{"version":1,"material":"YQ=="}]}`, `{"keys":[],"unknown":1}`, `{} {}`} {
		if _, e := Load(writeFile(t, []byte(data), 0600)); e == nil {
			t.Fatal("accepted invalid file")
		}
	}
	if _, e := Load(filepath.Join(".", "missing-key-file")); e == nil {
		t.Fatal("missing")
	}
	if runtime.GOOS != "windows" {
		k, _ := GenerateKey(1)
		d, _ := json.Marshal(File{[]Key{k}})
		if _, e := Load(writeFile(t, d, 0644)); e == nil {
			t.Fatal("permissions")
		}
	}
}
