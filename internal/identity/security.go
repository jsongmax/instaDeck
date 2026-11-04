package identity

import (
	"context"
	"encoding/base32"
	"errors"
	"time"
	"unicode/utf8"

	"instadeck/internal/keyring"
)

// SessionMetadata contains only coarse device information and the source address.
type SessionMetadata struct{ Device, SourceAddress string }

// SecurityRepository provides atomic security settings and session operations.
type SecurityRepository interface {
	CreateVerifiedChallenge(context.Context, Challenge, int64) error
	AtomicSecurityChange(context.Context, SecurityChange) ([][32]byte, error)
	ListSessions(context.Context, string, time.Time, time.Duration, time.Duration) ([]Session, error)
	AtomicRevokeSessions(context.Context, Session, string, time.Time, time.Duration, time.Duration) ([][32]byte, error)
}

// SecurityChange is a compare-and-swap update of administrator credentials.
type SecurityChange struct {
	Mode                 string
	AdministratorID      string
	Revision             int64
	Current, Replacement Session
	Password             PasswordHash
	TOTP                 keyring.Envelope
	RecoveryHashes       [][32]byte
	Step                 int64
	Now                  time.Time
	IdleTTL, AbsoluteTTL time.Duration
}

type pendingTOTP struct {
	secret      []byte
	sessionHash [32]byte
	revision    int64
	expiresAt   time.Time
}

// SecurityPreparation holds a password-verified snapshot without holding any lock.
type SecurityPreparation struct {
	administrator Administrator
	session       Session
	password      PasswordHash
}

// SecurityResult contains replacement credentials that may be returned only once.
type SecurityResult struct {
	Token              string
	RecoveryCodes      []string
	Secret, OTPAuthURI string
	Revoked            [][32]byte
}

// PrepareSecurity verifies passwords and performs Argon2 work outside the HTTP lock.
func (s *Service) PrepareSecurity(ctx context.Context, token, password, newPassword, ip, mode string) (SecurityPreparation, error) {
	session, err := s.Session(ctx, token)
	if err != nil {
		return SecurityPreparation{}, errors.New("unauthorized")
	}
	if !s.limiter.Allow(ip, s.now()) {
		return SecurityPreparation{}, errors.New("authentication_failed")
	}
	administrator, err := s.repo.AdministratorByID(ctx, session.AdministratorID)
	if err != nil || len(password) > 1024 || !VerifyPassword(administrator.Password, password) {
		return SecurityPreparation{}, errors.New("authentication_failed")
	}
	preparation := SecurityPreparation{administrator: administrator, session: session}
	if mode == "password" {
		if utf8.RuneCountInString(newPassword) < MinPasswordLength || len(newPassword) > 1024 {
			return SecurityPreparation{}, errors.New("weak_password")
		}
		if newPassword == password {
			return SecurityPreparation{}, errors.New("password_unchanged")
		}
		preparation.password, err = HashPassword(newPassword)
	}
	return preparation, err
}

// ApplySecurity commits a prepared change while the caller serializes terminal lifecycle changes.
func (s *Service) ApplySecurity(ctx context.Context, preparation SecurityPreparation, mode, code string) (result SecurityResult, err error) {
	repository, ok := s.repo.(SecurityRepository)
	if !ok {
		return result, errors.New("unavailable")
	}
	session, err := s.repo.AtomicUseSession(ctx, preparation.session.Hash, s.now(), s.options.IdleTTL, s.options.AbsoluteTTL)
	if err != nil {
		return result, errors.New("unauthorized")
	}
	administrator, err := s.repo.AdministratorByID(ctx, session.AdministratorID)
	if err != nil || administrator.Revision != preparation.administrator.Revision {
		return result, errors.New("authentication_failed")
	}
	change := SecurityChange{Mode: mode, AdministratorID: administrator.ID, Revision: administrator.Revision, Current: session, Password: preparation.password, Now: s.now(), IdleTTL: s.options.IdleTTL, AbsoluteTTL: s.options.AbsoluteTTL}
	if mode != "password" {
		s.secondFactor.mutex.Lock()
		defer s.secondFactor.mutex.Unlock()
		if !s.secondFactor.allow(s.now()) {
			return result, errors.New("authentication_failed")
		}
		defer func() {
			if err != nil {
				s.secondFactor.fail(s.now())
			}
		}()
		secret, decryptErr := s.cipher.Decrypt(administrator.TOTP, totpAAD())
		if decryptErr != nil {
			return result, errors.New("authentication_failed")
		}
		defer clear(secret)
		step, matched := MatchTOTP(secret, code, s.now(), administrator.LastTOTPStep)
		if !matched {
			return result, errors.New("authentication_failed")
		}
		change.Step = step
	}
	var pendingSecret []byte
	if mode == "start" {
		pendingSecret, err = GenerateTOTPSecret()
		if err != nil {
			return result, errors.New("unavailable")
		}
		defer clear(pendingSecret)
		result.Secret = base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(pendingSecret)
		result.OTPAuthURI = OTPAuthURI(pendingSecret, "InstaDeck", administrator.Username)
	}
	if mode == "recovery" {
		result.RecoveryCodes, change.RecoveryHashes, err = GenerateRecoveryCodes(10)
		if err != nil {
			return SecurityResult{}, errors.New("unavailable")
		}
	}
	if mode == "password" {
		change.Replacement, result.Token, err = s.replacement(session)
		if err != nil {
			return SecurityResult{}, errors.New("unavailable")
		}
	}
	result.Revoked, err = repository.AtomicSecurityChange(ctx, change)
	if err != nil {
		return SecurityResult{}, errors.New("authentication_failed")
	}
	s.pendingMutex.Lock()
	defer s.pendingMutex.Unlock()
	if s.pending != nil {
		clear(s.pending.secret)
		s.pending = nil
	}
	if mode == "start" {
		s.pending = &pendingTOTP{secret: append([]byte(nil), pendingSecret...), sessionHash: session.Hash, revision: administrator.Revision, expiresAt: s.now().Add(10 * time.Minute)}
	}
	return result, nil
}

func (s *Service) replacement(old Session) (Session, string, error) {
	replacement, token, err := s.newSession(old.AdministratorID, s.now())
	replacement.CreatedAt = old.CreatedAt
	replacement.PublicID = old.PublicID
	replacement.Device = old.Device
	replacement.SourceAddress = old.SourceAddress
	return replacement, token, err
}

// ConfirmTOTP validates only the pending secret and atomically activates it.
func (s *Service) ConfirmTOTP(ctx context.Context, token, code string) (result SecurityResult, err error) {
	repository, ok := s.repo.(SecurityRepository)
	if !ok {
		return result, errors.New("unavailable")
	}
	session, err := s.Session(ctx, token)
	if err != nil {
		return result, errors.New("unauthorized")
	}
	s.secondFactor.mutex.Lock()
	defer s.secondFactor.mutex.Unlock()
	if !s.secondFactor.allow(s.now()) {
		return result, errors.New("authentication_failed")
	}
	defer func() {
		if err != nil {
			s.secondFactor.fail(s.now())
		}
	}()
	s.pendingMutex.Lock()
	defer s.pendingMutex.Unlock()
	pending := s.pending
	if pending != nil && !s.now().Before(pending.expiresAt) {
		clear(pending.secret)
		s.pending = nil
		pending = nil
	}
	if pending == nil || pending.sessionHash != session.Hash {
		return result, errors.New("totp_pending_missing")
	}
	administrator, err := s.repo.AdministratorByID(ctx, session.AdministratorID)
	if err != nil || administrator.Revision != pending.revision {
		return result, errors.New("totp_pending_missing")
	}
	step, matched := MatchTOTP(pending.secret, code, s.now(), -1)
	if !matched {
		return result, errors.New("authentication_failed")
	}
	encrypted, err := s.cipher.Encrypt(pending.secret, totpAAD())
	if err != nil {
		return result, errors.New("unavailable")
	}
	codes, hashes, err := GenerateRecoveryCodes(10)
	if err != nil {
		return result, errors.New("unavailable")
	}
	replacement, newToken, err := s.replacement(session)
	if err != nil {
		return result, errors.New("unavailable")
	}
	revoked, err := repository.AtomicSecurityChange(ctx, SecurityChange{Mode: "totp", AdministratorID: administrator.ID, Revision: administrator.Revision, Current: session, Replacement: replacement, TOTP: encrypted, RecoveryHashes: hashes, Step: step, Now: s.now(), IdleTTL: s.options.IdleTTL, AbsoluteTTL: s.options.AbsoluteTTL})
	if err != nil {
		return result, errors.New("authentication_failed")
	}
	clear(pending.secret)
	s.pending = nil
	return SecurityResult{Token: newToken, RecoveryCodes: codes, Revoked: revoked}, nil
}

// Security returns the binding time and remaining recovery-code count.
func (s *Service) Security(ctx context.Context, administratorID string) (time.Time, int, error) {
	administrator, err := s.repo.AdministratorByID(ctx, administratorID)
	return administrator.TOTPBoundAt, len(administrator.RecoveryHashes), err
}

// Sessions lists active sessions without exposing their storage representation to HTTP.
func (s *Service) Sessions(ctx context.Context, administratorID string) ([]Session, error) {
	repository, ok := s.repo.(SecurityRepository)
	if !ok {
		return nil, errors.New("unavailable")
	}
	return repository.ListSessions(ctx, administratorID, s.now(), s.options.IdleTTL, s.options.AbsoluteTTL)
}

// RevokeSessions revokes a public session identifier, or all other sessions when empty.
func (s *Service) RevokeSessions(ctx context.Context, token, publicID string) ([][32]byte, error) {
	session, err := s.Session(ctx, token)
	if err != nil {
		return nil, errors.New("unauthorized")
	}
	if !s.RecentlyAuthenticated(session) {
		return nil, errors.New("reauthentication_required")
	}
	if publicID != "" && publicID == session.PublicID {
		return nil, errors.New("cannot_revoke_current")
	}
	repository, ok := s.repo.(SecurityRepository)
	if !ok {
		return nil, errors.New("unavailable")
	}
	return repository.AtomicRevokeSessions(ctx, session, publicID, s.now(), s.options.IdleTTL, s.options.AbsoluteTTL)
}

// ResetAuth replaces all credentials of an existing administrator from the local CLI.
func (s *Service) ResetAuth(ctx context.Context, name, password string, secret []byte) ([]string, error) {
	if utf8.RuneCountInString(password) < MinPasswordLength || len(password) > 1024 || len(secret) < 20 {
		return nil, errors.New("weak_password")
	}
	administrator, err := s.repo.AdministratorByName(ctx, name)
	if err != nil {
		return nil, errors.New("administrator required")
	}
	passwordHash, err := HashPassword(password)
	if err != nil {
		return nil, err
	}
	encrypted, err := s.cipher.Encrypt(secret, totpAAD())
	if err != nil {
		return nil, err
	}
	codes, hashes, err := GenerateRecoveryCodes(10)
	if err != nil {
		return nil, err
	}
	repository, ok := s.repo.(SecurityRepository)
	if !ok {
		return nil, errors.New("unavailable")
	}
	_, err = repository.AtomicSecurityChange(ctx, SecurityChange{Mode: "reset", AdministratorID: administrator.ID, Revision: administrator.Revision, Password: passwordHash, TOTP: encrypted, RecoveryHashes: hashes, Step: -1, Now: s.now()})
	if err != nil {
		return nil, err
	}
	return codes, nil
}
