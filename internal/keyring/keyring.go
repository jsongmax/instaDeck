package keyring

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"runtime"
)

type Envelope struct {
	KeyVersion int    `json:"keyVersion"`
	Nonce      []byte `json:"nonce"`
	Ciphertext []byte `json:"ciphertext"`
}
type Key struct {
	Version  int    `json:"version"`
	Material []byte `json:"material"`
}
type File struct {
	Keys []Key `json:"keys"`
}
type Ring struct {
	keys   map[int]cipher.AEAD
	latest int
}

func GenerateKey(version int) (Key, error) {
	if version < 1 {
		return Key{}, fmt.Errorf("invalid key version")
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return Key{}, fmt.Errorf("key generation failed")
	}
	return Key{version, b}, nil
}
func Load(path string) (*Ring, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("cannot open key file")
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Size() > 65536 {
		return nil, fmt.Errorf("invalid key file")
	}
	if runtime.GOOS != "windows" && st.Mode().Perm()&0077 != 0 {
		return nil, fmt.Errorf("key file permissions too broad")
	}
	d := json.NewDecoder(io.LimitReader(f, 65537))
	d.DisallowUnknownFields()
	var data File
	if err = d.Decode(&data); err != nil {
		return nil, fmt.Errorf("invalid key file format")
	}
	if d.Decode(new(any)) != io.EOF {
		return nil, fmt.Errorf("invalid key file trailing data")
	}
	r := &Ring{keys: make(map[int]cipher.AEAD)}
	for _, k := range data.Keys {
		if k.Version < 1 || len(k.Material) != 32 || r.keys[k.Version] != nil {
			return nil, fmt.Errorf("invalid key version or length")
		}
		b, _ := aes.NewCipher(k.Material)
		a, _ := cipher.NewGCM(b)
		r.keys[k.Version] = a
		if k.Version > r.latest {
			r.latest = k.Version
		}
	}
	if r.latest == 0 {
		return nil, fmt.Errorf("empty key file")
	}
	return r, nil
}
func (r *Ring) Encrypt(plain, aad []byte) (Envelope, error) {
	a := r.keys[r.latest]
	n := make([]byte, a.NonceSize())
	if _, err := rand.Read(n); err != nil {
		return Envelope{}, fmt.Errorf("nonce generation failed")
	}
	return Envelope{r.latest, n, a.Seal(nil, n, plain, aad)}, nil
}
func (r *Ring) Decrypt(e Envelope, aad []byte) ([]byte, error) {
	a := r.keys[e.KeyVersion]
	if a == nil || len(e.Nonce) != a.NonceSize() {
		return nil, fmt.Errorf("invalid encrypted value")
	}
	p, err := a.Open(nil, e.Nonce, e.Ciphertext, aad)
	if err != nil {
		return nil, fmt.Errorf("decryption failed")
	}
	return p, nil
}
