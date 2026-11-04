package identity

import (
	"context"
	"fmt"
	"instadeck/internal/keyring"
	"sync"
	"testing"
	"time"
)

type memoryRepo struct {
	mu         sync.Mutex
	a          Administrator
	challenges map[[32]byte]Challenge
	sessions   map[[32]byte]Session
}

func (r *memoryRepo) CreateAdministrator(_ context.Context, a Administrator) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.a.ID != "" {
		return fmt.Errorf("exists")
	}
	r.a = a
	return nil
}
func (r *memoryRepo) AdministratorByName(_ context.Context, n string) (Administrator, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if n != r.a.Username {
		return Administrator{}, fmt.Errorf("missing")
	}
	return r.a, nil
}
func (r *memoryRepo) AdministratorByID(_ context.Context, id string) (Administrator, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if id != r.a.ID {
		return Administrator{}, fmt.Errorf("missing")
	}
	return r.a, nil
}
func (r *memoryRepo) CreateChallenge(_ context.Context, c Challenge) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.challenges[c.Hash] = c
	return nil
}
func (r *memoryRepo) AtomicTakeChallenge(_ context.Context, h [32]byte, n time.Time) (Challenge, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.challenges[h]
	if !ok || !n.Before(c.ExpiresAt) || c.Attempts <= 0 {
		return Challenge{}, fmt.Errorf("invalid")
	}
	c.Attempts--
	r.challenges[h] = c
	return c, nil
}
func (r *memoryRepo) complete(h [32]byte, s Session, n time.Time) error {
	c, ok := r.challenges[h]
	if !ok || !n.Before(c.ExpiresAt) || c.AdministratorID != s.AdministratorID {
		return fmt.Errorf("invalid")
	}
	delete(r.challenges, h)
	r.sessions[s.Hash] = s
	return nil
}
func (r *memoryRepo) AtomicCompleteTOTP(_ context.Context, h [32]byte, step int64, s Session, n time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if step <= r.a.LastTOTPStep {
		return fmt.Errorf("replay")
	}
	if e := r.complete(h, s, n); e != nil {
		return e
	}
	r.a.LastTOTPStep = step
	return nil
}
func (r *memoryRepo) AtomicCompleteRecovery(_ context.Context, h, code [32]byte, s Session, n time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, c := range r.a.RecoveryHashes {
		if c == code {
			if e := r.complete(h, s, n); e != nil {
				return e
			}
			r.a.RecoveryHashes = append(r.a.RecoveryHashes[:i], r.a.RecoveryHashes[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("invalid")
}
func (r *memoryRepo) CreateSession(_ context.Context, s Session) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sessions[s.Hash] = s
	return nil
}
func valid(s Session, n time.Time, idle, absolute time.Duration) bool {
	return !s.Revoked && !n.Before(s.CreatedAt) && n.Before(s.LastSeen.Add(idle)) && n.Before(s.CreatedAt.Add(absolute))
}
func (r *memoryRepo) AtomicUseSession(_ context.Context, h [32]byte, n time.Time, idle, absolute time.Duration) (Session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.sessions[h]
	if !ok || !valid(s, n, idle, absolute) {
		return Session{}, fmt.Errorf("invalid")
	}
	s.LastSeen = n
	r.sessions[h] = s
	return s, nil
}
func (r *memoryRepo) RevokeSession(_ context.Context, h [32]byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.sessions[h]
	s.Revoked = true
	r.sessions[h] = s
	return nil
}
func (r *memoryRepo) AtomicRotateSession(_ context.Context, h [32]byte, step int64, s Session, n time.Time, idle, absolute time.Duration) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	old, ok := r.sessions[h]
	if !ok || !valid(old, n, idle, absolute) || step <= r.a.LastTOTPStep {
		return fmt.Errorf("invalid")
	}
	old.Revoked = true
	r.sessions[h] = old
	r.sessions[s.Hash] = s
	r.a.LastTOTPStep = step
	return nil
}

type testCipher struct{}

func (testCipher) Encrypt(b, a []byte) (keyring.Envelope, error) {
	return keyring.Envelope{Ciphertext: append([]byte(nil), b...)}, nil
}
func (testCipher) Decrypt(e keyring.Envelope, a []byte) ([]byte, error) { return e.Ciphertext, nil }
func fixture(t *testing.T) (*Service, *memoryRepo, *time.Time, []string) {
	t.Helper()
	now := time.Unix(1700000000, 0)
	r := &memoryRepo{challenges: map[[32]byte]Challenge{}, sessions: map[[32]byte]Session{}}
	s, e := New(r, testCipher{}, func() time.Time { return now }, Options{time.Minute, 5 * time.Minute, time.Hour, 2 * time.Minute, 2}, NewLimiter(time.Minute, 100, 1000))
	if e != nil {
		t.Fatal(e)
	}
	codes, e := s.Initialize(context.Background(), "admin", "admin", "test-password", []byte("12345678901234567890"))
	if e != nil {
		t.Fatal(e)
	}
	return s, r, &now, codes
}
func challenge(t *testing.T, s *Service) string {
	t.Helper()
	c, e := s.Password(context.Background(), "admin", "test-password", "127.0.0.1")
	if e != nil {
		t.Fatal(e)
	}
	return c
}
func login(t *testing.T, s *Service, n time.Time) string {
	t.Helper()
	v, e := s.Complete(context.Background(), challenge(t, s), totp([]byte("12345678901234567890"), n.Unix()/30, 6), false)
	if e != nil {
		t.Fatal(e)
	}
	return v
}
func TestRFC6238(t *testing.T) {
	secret := []byte("12345678901234567890")
	for _, v := range []struct {
		n    int64
		code string
	}{{59, "94287082"}, {1111111109, "07081804"}, {1111111111, "14050471"}, {1234567890, "89005924"}, {2000000000, "69279037"}, {20000000000, "65353130"}} {
		if got := totp(secret, v.n/30, 8); got != v.code {
			t.Fatalf("vector %d: %s", v.n, got)
		}
	}
}
func TestTOTPWindows(t *testing.T) {
	secret := []byte("12345678901234567890")
	n := time.Unix(3000, 0)
	for _, offset := range []int64{-2, -1, 0, 1, 2} {
		_, ok := MatchTOTP(secret, totp(secret, 100+offset, 6), n, -1)
		if ok != (offset >= -1 && offset <= 1) {
			t.Fatal(offset)
		}
	}
	code := totp(secret, 100, 6)
	if _, ok := MatchTOTP(secret, code, n, 100); ok {
		t.Fatal("replay")
	}
	if _, ok := MatchTOTP(secret, code, time.Unix(3059, 0), -1); !ok {
		t.Fatal("boundary")
	}
	if _, ok := MatchTOTP(secret, code, time.Unix(3060, 0), -1); ok {
		t.Fatal("outside boundary")
	}
}
func TestPassword(t *testing.T) {
	h, e := HashPassword("test")
	if e != nil || !VerifyPassword(h, "test") || VerifyPassword(h, "wrong") {
		t.Fatal("password")
	}
	h.Memory = 1 << 30
	if VerifyPassword(h, "test") {
		t.Fatal("unsafe parameters")
	}
}
func TestChallenges(t *testing.T) {
	ctx := context.Background()
	s, _, now, _ := fixture(t)
	c := challenge(t, s)
	if _, e := s.Session(ctx, c); e == nil {
		t.Fatal("challenge became session")
	}
	for i := 0; i < 2; i++ {
		if _, e := s.Complete(ctx, c, "bad", false); e == nil {
			t.Fatal("bad code")
		}
	}
	if _, e := s.Complete(ctx, c, totp([]byte("12345678901234567890"), now.Unix()/30, 6), false); e == nil {
		t.Fatal("exhaustion")
	}
	c = challenge(t, s)
	*now = now.Add(time.Minute)
	if _, e := s.Complete(ctx, c, "bad", false); e == nil {
		t.Fatal("expired")
	}
	if v, e := s.Password(ctx, "missing", "test-password", "127.0.0.1"); e == nil || v != "" {
		t.Fatal("missing user")
	}
}
func TestRecoveryAndReplay(t *testing.T) {
	ctx := context.Background()
	s, _, now, codes := fixture(t)
	c := challenge(t, s)
	if _, e := s.Complete(ctx, c, codes[0], true); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Complete(ctx, c, codes[1], true); e == nil {
		t.Fatal("challenge replay")
	}
	if _, e := s.Complete(ctx, challenge(t, s), codes[0], true); e == nil {
		t.Fatal("recovery reuse")
	}
	login(t, s, *now)
	if _, e := s.Complete(ctx, challenge(t, s), totp([]byte("12345678901234567890"), now.Unix()/30, 6), false); e == nil {
		t.Fatal("TOTP replay")
	}
}
func TestSessions(t *testing.T) {
	for _, kind := range []string{"idle", "absolute", "revoke", "csrf", "rotate"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			s, _, now, _ := fixture(t)
			token := login(t, s, *now)
			session, e := s.Session(ctx, token)
			if e != nil {
				t.Fatal(e)
			}
			switch kind {
			case "idle":
				*now = now.Add(5 * time.Minute)
			case "absolute":
				for i := 0; i < 60; i++ {
					*now = now.Add(time.Minute)
					_, _ = s.Session(ctx, token)
				}
			case "revoke":
				if e = s.Revoke(ctx, token); e != nil {
					t.Fatal(e)
				}
			case "csrf":
				if !CheckCSRF(session, session.CSRFSecret) || CheckCSRF(session, "") || CheckCSRF(session, "bad") {
					t.Fatal("csrf")
				}
				if !s.RecentlyAuthenticated(session) {
					t.Fatal("recent")
				}
				*now = now.Add(2 * time.Minute)
				if s.RecentlyAuthenticated(session) {
					t.Fatal("recent expired")
				}
				return
			case "rotate":
				*now = now.Add(30 * time.Second)
				replacement, e := s.Reauthenticate(ctx, token, "test-password", totp([]byte("12345678901234567890"), now.Unix()/30, 6), "127.0.0.1")
				if e != nil {
					t.Fatal(e)
				}
				if replacement == token {
					t.Fatal("not rotated")
				}
				if _, e = s.Session(ctx, replacement); e != nil {
					t.Fatal(e)
				}
			}
			if _, e = s.Session(ctx, token); e == nil {
				t.Fatal("old session valid")
			}
		})
	}
}
func TestLimiter(t *testing.T) {
	n := time.Unix(100, 0)
	l := NewLimiter(time.Minute, 1, 1)
	if !l.Allow("127.0.0.1", n) || l.Allow("127.0.0.1", n) || l.Allow("127.0.0.2", n) {
		t.Fatal("limits")
	}
	if !l.Allow("127.0.0.1", n.Add(time.Minute)) {
		t.Fatal("window recovery")
	}
}

func TestConcurrentRecovery(t *testing.T) {
	s, r, _, codes := fixture(t)
	ctx := context.Background()
	c := challenge(t, s)
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, e := s.Complete(ctx, c, codes[0], true); results <- e }()
	}
	wg.Wait()
	close(results)
	success := 0
	for e := range results {
		if e == nil {
			success++
		}
	}
	if success != 1 || len(r.sessions) != 1 {
		t.Fatal("non-atomic recovery consumption")
	}
}
func TestFinalChallengeAttempt(t *testing.T) {
	s, _, now, _ := fixture(t)
	ctx := context.Background()
	c := challenge(t, s)
	if _, e := s.Complete(ctx, c, "invalid", false); e == nil {
		t.Fatal("invalid accepted")
	}
	if _, e := s.Complete(ctx, c, totp([]byte("12345678901234567890"), now.Unix()/30, 6), false); e != nil {
		t.Fatal("final attempt rejected", e)
	}
}
func TestIndependentLimits(t *testing.T) {
	n := time.Unix(100, 0)
	l := NewLimiter(time.Minute, 1, 10)
	if !l.Allow("127.0.0.1", n) || l.Allow("::ffff:127.0.0.1", n) || !l.Allow("127.0.0.2", n) {
		t.Fatal("per-IP limit")
	}
	if !l.Allow("127.0.0.1", n.Add(time.Minute)) {
		t.Fatal("IP window recovery")
	}
	g := NewLimiter(time.Minute, 10, 1)
	if !g.Allow("127.0.0.1", n) || g.Allow("127.0.0.2", n) || !g.Allow("127.0.0.2", n.Add(time.Minute)) {
		t.Fatal("global window")
	}
}
