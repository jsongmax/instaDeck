package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"instadeck/internal/identity"
	"time"
)

var _ identity.Repository = (*Store)(nil)

// CreateAdministrator atomically stores an administrator and recovery hashes.
func (s *Store) CreateAdministrator(ctx context.Context, administrator identity.Administrator) error {
	return s.transaction(ctx, func(tx *sql.Tx) error {
		password, err := json.Marshal(administrator.Password)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO administrator (id,username,password,key_version,nonce,ciphertext,last_totp_step,totp_bound_at) VALUES (?,?,?,?,?,?,?,?)`, administrator.ID, administrator.Username, password, administrator.TOTP.KeyVersion, administrator.TOTP.Nonce, administrator.TOTP.Ciphertext, administrator.LastTOTPStep, administrator.TOTPBoundAt.UnixMilli())
		if err != nil {
			return err
		}
		for _, hash := range administrator.RecoveryHashes {
			if _, err = tx.ExecContext(ctx, "INSERT INTO recovery_codes (administrator_id,hash) VALUES (?,?)", administrator.ID, hash[:]); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) administrator(ctx context.Context, column, value string) (identity.Administrator, error) {
	var administrator identity.Administrator
	var password []byte
	var boundAt int64
	err := s.reader.QueryRowContext(ctx, "SELECT id, username, password, key_version, nonce, ciphertext, last_totp_step, totp_bound_at, revision FROM administrator WHERE "+column+" = ?", value).Scan(&administrator.ID, &administrator.Username, &password, &administrator.TOTP.KeyVersion, &administrator.TOTP.Nonce, &administrator.TOTP.Ciphertext, &administrator.LastTOTPStep, &boundAt, &administrator.Revision)
	if err != nil {
		return identity.Administrator{}, storageError(err)
	}
	administrator.TOTPBoundAt = time.UnixMilli(boundAt).UTC()
	if json.Unmarshal(password, &administrator.Password) != nil {
		return identity.Administrator{}, ErrRejected
	}
	rows, err := s.reader.QueryContext(ctx, "SELECT hash FROM recovery_codes WHERE administrator_id=?", administrator.ID)
	if err != nil {
		return identity.Administrator{}, storageError(err)
	}
	defer rows.Close()
	for rows.Next() {
		var hash []byte
		if rows.Scan(&hash) != nil || len(hash) != 32 {
			return identity.Administrator{}, ErrRejected
		}
		administrator.RecoveryHashes = append(administrator.RecoveryHashes, [32]byte(hash))
	}
	if rows.Err() != nil {
		return identity.Administrator{}, ErrRejected
	}
	return administrator, nil
}

// AdministratorByName returns the named administrator.
func (s *Store) AdministratorByName(ctx context.Context, name string) (identity.Administrator, error) {
	return s.administrator(ctx, "username", name)
}

// AdministratorByID returns an administrator by identifier.
func (s *Store) AdministratorByID(ctx context.Context, id string) (identity.Administrator, error) {
	return s.administrator(ctx, "id", id)
}

// CreateChallenge persists a new password-verified challenge.
func (s *Store) CreateChallenge(ctx context.Context, challenge identity.Challenge) error {
	if challenge.Attempts < 1 {
		return ErrRejected
	}
	_, err := s.writer.ExecContext(ctx, "INSERT INTO auth_challenges (hash,administrator_id,expires_at,attempts) VALUES (?,?,?,?)", challenge.Hash[:], challenge.AdministratorID, challenge.ExpiresAt.UnixMilli(), challenge.Attempts)
	if err != nil {
		return storageError(err)
	}
	return nil
}

// AtomicTakeChallenge consumes one attempt before code verification.
func (s *Store) AtomicTakeChallenge(ctx context.Context, hash [32]byte, now time.Time) (identity.Challenge, error) {
	var challenge identity.Challenge
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		var expires int64
		err := tx.QueryRowContext(ctx, `UPDATE auth_challenges SET attempts=attempts-1,taken=1 WHERE hash=? AND expires_at>? AND attempts>0 RETURNING administrator_id,expires_at,attempts`, hash[:], now.UnixMilli()).Scan(&challenge.AdministratorID, &expires, &challenge.Attempts)
		challenge.Hash = hash
		challenge.ExpiresAt = time.UnixMilli(expires).UTC()
		return err
	})
	return challenge, err
}

func insertSession(ctx context.Context, tx *sql.Tx, session identity.Session) error {
	_, err := tx.ExecContext(ctx, "INSERT INTO sessions (public_id,device,source_address,hash,administrator_id,csrf_secret,created_at,last_seen,authenticated_at,revoked) VALUES (?,?,?,?,?,?,?,?,?,?)", session.PublicID, session.Device, session.SourceAddress, session.Hash[:], session.AdministratorID, session.CSRFSecret, session.CreatedAt.UnixMilli(), session.LastSeen.UnixMilli(), session.AuthenticatedAt.UnixMilli(), session.Revoked)
	return err
}

func (s *Store) complete(ctx context.Context, hash [32]byte, session identity.Session, now time.Time, verify func(*sql.Tx, string) error) error {
	return s.transaction(ctx, func(tx *sql.Tx) error {
		var administratorID string
		if err := tx.QueryRowContext(ctx, "SELECT administrator_id FROM auth_challenges WHERE hash=? AND expires_at>? AND taken=1", hash[:], now.UnixMilli()).Scan(&administratorID); err != nil {
			return err
		}
		if administratorID != session.AdministratorID {
			return ErrRejected
		}
		if err := verify(tx, administratorID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM auth_challenges WHERE hash=?", hash[:]); err != nil {
			return err
		}
		return insertSession(ctx, tx, session)
	})
}

// AtomicCompleteTOTP advances the replay counter and creates a session atomically.
func (s *Store) AtomicCompleteTOTP(ctx context.Context, hash [32]byte, step int64, session identity.Session, now time.Time) error {
	return s.complete(ctx, hash, session, now, func(tx *sql.Tx, id string) error {
		return requireOne(tx.ExecContext(ctx, "UPDATE administrator SET last_totp_step=? WHERE id=? AND last_totp_step<?", step, id, step))
	})
}

// AtomicCompleteRecovery consumes a recovery code and completes a challenge atomically.
func (s *Store) AtomicCompleteRecovery(ctx context.Context, hash, code [32]byte, session identity.Session, now time.Time) error {
	return s.complete(ctx, hash, session, now, func(tx *sql.Tx, id string) error {
		return requireOne(tx.ExecContext(ctx, "DELETE FROM recovery_codes WHERE administrator_id=? AND hash=?", id, code[:]))
	})
}

// CreateSession stores a session.
func (s *Store) CreateSession(ctx context.Context, session identity.Session) error {
	return s.transaction(ctx, func(tx *sql.Tx) error { return insertSession(ctx, tx, session) })
}

func useSession(ctx context.Context, tx *sql.Tx, hash [32]byte, now time.Time, idle, absolute time.Duration) (identity.Session, error) {
	var session identity.Session
	var created, lastSeen, authenticated int64
	err := tx.QueryRowContext(ctx, "SELECT public_id,device,source_address,administrator_id,csrf_secret,created_at,last_seen,authenticated_at,revoked FROM sessions WHERE hash=?", hash[:]).Scan(&session.PublicID, &session.Device, &session.SourceAddress, &session.AdministratorID, &session.CSRFSecret, &created, &lastSeen, &authenticated, &session.Revoked)
	session.Hash = hash
	session.CreatedAt = time.UnixMilli(created).UTC()
	session.LastSeen = time.UnixMilli(lastSeen).UTC()
	session.AuthenticatedAt = time.UnixMilli(authenticated).UTC()
	if err != nil {
		return identity.Session{}, err
	}
	if session.Revoked || idle <= 0 || absolute <= 0 || now.Before(session.CreatedAt) || !now.Before(session.LastSeen.Add(idle)) || !now.Before(session.CreatedAt.Add(absolute)) {
		return identity.Session{}, ErrRejected
	}
	// now is taken before queueing for the writer, so a request can commit after
	// a later one. It must neither be rejected nor move last_seen backwards.
	if now.After(session.LastSeen) {
		session.LastSeen = now.UTC()
		_, err = tx.ExecContext(ctx, "UPDATE sessions SET last_seen=? WHERE hash=?", now.UnixMilli(), hash[:])
	}
	return session, err
}

// AtomicUseSession validates lifetimes and revocation and refreshes last activity.
func (s *Store) AtomicUseSession(ctx context.Context, hash [32]byte, now time.Time, idle, absolute time.Duration) (identity.Session, error) {
	var session identity.Session
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		var err error
		session, err = useSession(ctx, tx, hash, now, idle, absolute)
		return err
	})
	return session, err
}

// RevokeSession invalidates a session; revocation is idempotent.
func (s *Store) RevokeSession(ctx context.Context, hash [32]byte) error {
	_, err := s.writer.ExecContext(ctx, "UPDATE sessions SET revoked=1 WHERE hash=?", hash[:])
	if err != nil {
		return storageError(err)
	}
	return nil
}

// AtomicRotateSession advances TOTP and replaces a valid session without extending its absolute lifetime.
func (s *Store) AtomicRotateSession(ctx context.Context, hash [32]byte, step int64, replacement identity.Session, now time.Time, idle, absolute time.Duration) error {
	return s.transaction(ctx, func(tx *sql.Tx) error {
		old, err := useSession(ctx, tx, hash, now, idle, absolute)
		if err != nil {
			return err
		}
		if replacement.AdministratorID != old.AdministratorID || replacement.Hash == hash || replacement.Revoked {
			return ErrRejected
		}
		if err = requireOne(tx.ExecContext(ctx, "UPDATE administrator SET last_totp_step=? WHERE id=? AND last_totp_step<?", step, old.AdministratorID, step)); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "DELETE FROM sessions WHERE hash=?", hash[:]); err != nil {
			return err
		}
		replacement.PublicID = old.PublicID
		replacement.Device = old.Device
		replacement.SourceAddress = old.SourceAddress
		replacement.CreatedAt = old.CreatedAt
		replacement.LastSeen = now
		replacement.AuthenticatedAt = now
		return insertSession(ctx, tx, replacement)
	})
}
