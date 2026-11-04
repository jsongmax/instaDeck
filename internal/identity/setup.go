package identity

import (
	"context"
	"fmt"
	"time"
)

// SetupRepository atomically commits initial identity and panel state.
type SetupRepository interface {
	CompleteSetup(context.Context, Administrator, string, Session) error
}

// CompleteSetup verifies enrollment against the shared second factor budget and commits it.
func (s *Service) CompleteSetup(ctx context.Context, repository SetupRepository, name string, password PasswordHash, secret []byte, code, origin string, metadata SessionMetadata) (Session, string, []string, error) {
	s.secondFactor.mutex.Lock()
	defer s.secondFactor.mutex.Unlock()
	now := s.now()
	if !s.secondFactor.allow(now) {
		return Session{}, "", nil, fmt.Errorf("rate_limited")
	}
	step, valid := MatchTOTP(secret, code, now, -1)
	if !valid {
		s.secondFactor.fail(now)
		return Session{}, "", nil, fmt.Errorf("authentication_failed")
	}
	encrypted, err := s.cipher.Encrypt(secret, totpAAD())
	if err != nil {
		return Session{}, "", nil, err
	}
	codes, hashes, err := GenerateRecoveryCodes(10)
	if err != nil {
		return Session{}, "", nil, err
	}
	session, token, err := s.newSession("administrator", now)
	if err != nil {
		return Session{}, "", nil, err
	}
	session.Device, session.SourceAddress = metadata.Device, metadata.SourceAddress
	administrator := Administrator{ID: "administrator", Username: name, Password: password, TOTP: encrypted, LastTOTPStep: step, TOTPBoundAt: now, RecoveryHashes: hashes}
	if err = repository.CompleteSetup(ctx, administrator, origin, session); err != nil {
		return Session{}, "", nil, err
	}
	return session, token, codes, nil
}

// SetupDeadline returns the enrollment expiry using the identity clock.
func (s *Service) SetupDeadline() time.Time { return s.now().Add(15 * time.Minute) }

// SetupExpired checks an enrollment deadline using the identity clock.
func (s *Service) SetupExpired(deadline time.Time) bool { return !s.now().Before(deadline) }
