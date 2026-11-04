package identity

import (
	"context"
	"crypto/subtle"
	"fmt"
	"time"
	"unicode/utf8"

	"instadeck/internal/keyring"
)

// MinPasswordLength is the shortest accepted administrator password.
const MinPasswordLength = 12

type Administrator struct {
	ID, Username   string
	Password       PasswordHash
	TOTP           keyring.Envelope
	LastTOTPStep   int64
	RecoveryHashes [][32]byte
}
type Challenge struct {
	Hash            [32]byte
	AdministratorID string
	ExpiresAt       time.Time
	Attempts        int
}
type Session struct {
	Hash                                 [32]byte
	AdministratorID, CSRFSecret          string
	CreatedAt, LastSeen, AuthenticatedAt time.Time
	Revoked                              bool
}
type Repository interface {
	CreateAdministrator(context.Context, Administrator) error
	AdministratorByName(context.Context, string) (Administrator, error)
	AdministratorByID(context.Context, string) (Administrator, error)
	CreateChallenge(context.Context, Challenge) error
	AtomicTakeChallenge(context.Context, [32]byte, time.Time) (Challenge, error)
	AtomicCompleteTOTP(context.Context, [32]byte, int64, Session, time.Time) error
	AtomicCompleteRecovery(context.Context, [32]byte, [32]byte, Session, time.Time) error
	CreateSession(context.Context, Session) error
	AtomicUseSession(context.Context, [32]byte, time.Time, time.Duration, time.Duration) (Session, error)
	RevokeSession(context.Context, [32]byte) error
	AtomicRotateSession(context.Context, [32]byte, int64, Session, time.Time, time.Duration, time.Duration) error
}
type Cipher interface {
	Encrypt([]byte, []byte) (keyring.Envelope, error)
	Decrypt(keyring.Envelope, []byte) ([]byte, error)
}
type Options struct {
	ChallengeTTL, IdleTTL, AbsoluteTTL, RecentTTL time.Duration
	ChallengeAttempts                             int
}
type Service struct {
	repo    Repository
	cipher  Cipher
	now     func() time.Time
	options Options
	dummy   PasswordHash
	limiter *Limiter
}

func New(repo Repository, cipher Cipher, now func() time.Time, options Options, limiter *Limiter) (*Service, error) {
	if repo == nil || cipher == nil || limiter == nil {
		return nil, fmt.Errorf("identity dependencies required")
	}
	if now == nil {
		now = time.Now
	}
	if options.ChallengeTTL <= 0 || options.IdleTTL <= 0 || options.AbsoluteTTL <= 0 || options.RecentTTL <= 0 || options.ChallengeAttempts < 1 {
		return nil, fmt.Errorf("invalid identity options")
	}
	d, e := HashPassword("dummy password")
	if e != nil {
		return nil, e
	}
	return &Service{repo, cipher, now, options, d, limiter}, nil
}
func totpAAD() []byte { return []byte("instadeck:administrator:totp:v1") }
func (s *Service) Initialize(ctx context.Context, id, name, password string, secret []byte) ([]string, error) {
	if id == "" || name == "" || len(secret) < 20 {
		return nil, fmt.Errorf("invalid administrator")
	}
	if utf8.RuneCountInString(password) < MinPasswordLength {
		return nil, fmt.Errorf("password must be at least %d characters", MinPasswordLength)
	}
	h, e := HashPassword(password)
	if e != nil {
		return nil, e
	}
	enc, e := s.cipher.Encrypt(secret, totpAAD())
	if e != nil {
		return nil, fmt.Errorf("TOTP encryption failed")
	}
	codes, hashes, e := GenerateRecoveryCodes(10)
	if e != nil {
		return nil, e
	}
	e = s.repo.CreateAdministrator(ctx, Administrator{ID: id, Username: name, Password: h, TOTP: enc, LastTOTPStep: -1, RecoveryHashes: hashes})
	if e != nil {
		return nil, fmt.Errorf("administrator storage failed")
	}
	return codes, nil
}
func (s *Service) Password(ctx context.Context, name, password, ip string) (string, error) {
	if !s.limiter.Allow(ip, s.now()) {
		return "", fmt.Errorf("login rate limited")
	}
	a, e := s.repo.AdministratorByName(ctx, name)
	h := a.Password
	if e != nil {
		h = s.dummy
	}
	ok := VerifyPassword(h, password)
	if e != nil || !ok {
		return "", fmt.Errorf("authentication failed")
	}
	token, e := randomToken()
	if e != nil {
		return "", e
	}
	e = s.repo.CreateChallenge(ctx, Challenge{TokenHash(token), a.ID, s.now().Add(s.options.ChallengeTTL), s.options.ChallengeAttempts})
	if e != nil {
		return "", fmt.Errorf("challenge storage failed")
	}
	return token, nil
}
func (s *Service) newSession(id string, now time.Time) (Session, string, error) {
	token, e := randomToken()
	if e != nil {
		return Session{}, "", e
	}
	csrf, e := randomToken()
	if e != nil {
		return Session{}, "", e
	}
	return Session{Hash: TokenHash(token), AdministratorID: id, CSRFSecret: csrf, CreatedAt: now, LastSeen: now, AuthenticatedAt: now}, token, nil
}
func (s *Service) Complete(ctx context.Context, challenge, code string, recovery bool) (string, error) {
	now := s.now()
	h := TokenHash(challenge)
	c, e := s.repo.AtomicTakeChallenge(ctx, h, now)
	if e != nil {
		return "", fmt.Errorf("authentication failed")
	}
	a, e := s.repo.AdministratorByID(ctx, c.AdministratorID)
	if e != nil {
		return "", fmt.Errorf("authentication failed")
	}
	session, token, e := s.newSession(a.ID, now)
	if e != nil {
		return "", e
	}
	if recovery {
		e = s.repo.AtomicCompleteRecovery(ctx, h, TokenHash(code), session, now)
	} else {
		secret, err := s.cipher.Decrypt(a.TOTP, totpAAD())
		if err != nil {
			return "", fmt.Errorf("authentication failed")
		}
		step, ok := MatchTOTP(secret, code, now, a.LastTOTPStep)
		if !ok {
			return "", fmt.Errorf("authentication failed")
		}
		e = s.repo.AtomicCompleteTOTP(ctx, h, step, session, now)
	}
	if e != nil {
		return "", fmt.Errorf("authentication failed")
	}
	return token, nil
}
func (s *Service) Session(ctx context.Context, token string) (Session, error) {
	v, e := s.repo.AtomicUseSession(ctx, TokenHash(token), s.now(), s.options.IdleTTL, s.options.AbsoluteTTL)
	if e != nil {
		return Session{}, fmt.Errorf("session invalid")
	}
	return v, nil
}
func (s *Service) Revoke(ctx context.Context, token string) error {
	if err := s.repo.RevokeSession(ctx, TokenHash(token)); err != nil {
		return fmt.Errorf("session revocation failed")
	}
	return nil
}
func CheckCSRF(session Session, token string) bool {
	return len(token) > 0 && subtle.ConstantTimeCompare([]byte(session.CSRFSecret), []byte(token)) == 1
}
func (s *Service) RecentlyAuthenticated(session Session) bool {
	age := s.now().Sub(session.AuthenticatedAt)
	return age >= 0 && age < s.options.RecentTTL
}
func (s *Service) Reauthenticate(ctx context.Context, token, password, code, ip string) (string, error) {
	if !s.limiter.Allow(ip, s.now()) {
		return "", fmt.Errorf("login rate limited")
	}
	old, e := s.Session(ctx, token)
	if e != nil {
		return "", e
	}
	a, e := s.repo.AdministratorByID(ctx, old.AdministratorID)
	if e != nil {
		return "", fmt.Errorf("authentication failed")
	}
	if !VerifyPassword(a.Password, password) {
		return "", fmt.Errorf("authentication failed")
	}
	secret, e := s.cipher.Decrypt(a.TOTP, totpAAD())
	if e != nil {
		return "", fmt.Errorf("authentication failed")
	}
	step, ok := MatchTOTP(secret, code, s.now(), a.LastTOTPStep)
	if !ok {
		return "", fmt.Errorf("authentication failed")
	}
	replacement, newToken, e := s.newSession(a.ID, s.now())
	if e != nil {
		return "", e
	}
	replacement.CreatedAt = old.CreatedAt
	e = s.repo.AtomicRotateSession(ctx, old.Hash, step, replacement, s.now(), s.options.IdleTTL, s.options.AbsoluteTTL)
	if e != nil {
		return "", fmt.Errorf("authentication failed")
	}
	return newToken, nil
}
