package identity

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/argon2"
)

type PasswordHash struct {
	Salt, Digest       []byte
	Memory, Iterations uint32
	Parallelism        uint8
}

func HashPassword(password string) (PasswordHash, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return PasswordHash{}, fmt.Errorf("random generation failed")
	}
	h := PasswordHash{Salt: salt, Memory: 19456, Iterations: 2, Parallelism: 1}
	h.Digest = argon2.IDKey([]byte(password), salt, h.Iterations, h.Memory, h.Parallelism, 32)
	return h, nil
}
func VerifyPassword(h PasswordHash, password string) bool {
	if h.Memory < 8192 || h.Memory > 65536 || h.Iterations < 1 || h.Iterations > 4 || h.Parallelism < 1 || h.Parallelism > 4 || len(h.Salt) != 16 || len(h.Digest) != 32 {
		return false
	}
	d := argon2.IDKey([]byte(password), h.Salt, h.Iterations, h.Memory, h.Parallelism, 32)
	return subtle.ConstantTimeCompare(d, h.Digest) == 1
}
func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("random generation failed")
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
func TokenHash(s string) [32]byte { return sha256.Sum256([]byte(s)) }
func GenerateRecoveryCodes(count int) ([]string, [][32]byte, error) {
	if count < 1 || count > 100 {
		return nil, nil, fmt.Errorf("invalid recovery count")
	}
	codes := make([]string, count)
	hashes := make([][32]byte, count)
	for i := range codes {
		v, e := randomToken()
		if e != nil {
			return nil, nil, e
		}
		codes[i] = v
		hashes[i] = TokenHash(v)
	}
	return codes, hashes, nil
}
func GenerateTOTPSecret() ([]byte, error) {
	b := make([]byte, 20)
	_, err := rand.Read(b)
	if err != nil {
		return nil, fmt.Errorf("random generation failed")
	}
	return b, nil
}
func OTPAuthURI(secret []byte, issuer, account string) string {
	q := url.Values{"secret": {base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(secret)}, "issuer": {issuer}, "algorithm": {"SHA1"}, "digits": {"6"}, "period": {"30"}}
	return "otpauth://totp/" + url.PathEscape(issuer+":"+account) + "?" + q.Encode()
}
func totp(secret []byte, step int64, digits int) string {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, uint64(step))
	m := hmac.New(sha1.New, secret)
	m.Write(b)
	d := m.Sum(nil)
	o := d[len(d)-1] & 15
	n := binary.BigEndian.Uint32(d[o:o+4]) & 0x7fffffff
	mod := uint32(1)
	for i := 0; i < digits; i++ {
		mod *= 10
	}
	return fmt.Sprintf("%0*d", digits, n%mod)
}
func MatchTOTP(secret []byte, code string, now time.Time, last int64) (int64, bool) {
	if len(code) != 6 {
		return 0, false
	}
	if _, err := strconv.Atoi(code); err != nil || strings.TrimSpace(code) != code {
		return 0, false
	}
	step := now.Unix() / 30
	for _, s := range []int64{step, step - 1, step + 1} {
		if s >= 0 && s > last && subtle.ConstantTimeCompare([]byte(totp(secret, s, 6)), []byte(code)) == 1 {
			return s, true
		}
	}
	return 0, false
}
