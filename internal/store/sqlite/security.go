package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"instadeck/internal/identity"
)

var _ identity.SecurityRepository = (*Store)(nil)

// CreateVerifiedChallenge rejects password verification completed against old credentials.
func (s *Store) CreateVerifiedChallenge(ctx context.Context, challenge identity.Challenge, revision int64) error {
	return s.transaction(ctx, func(tx *sql.Tx) error {
		return requireOne(tx.ExecContext(ctx, `INSERT INTO auth_challenges (hash,administrator_id,expires_at,attempts) SELECT ?,id,?,? FROM administrator WHERE id=? AND revision=?`, challenge.Hash[:], challenge.ExpiresAt.UnixMilli(), challenge.Attempts, challenge.AdministratorID, revision))
	})
}

func revokedHashes(ctx context.Context, tx *sql.Tx, administratorID string, current [32]byte, publicID string) ([][32]byte, error) {
	rows, err := tx.QueryContext(ctx, `UPDATE sessions SET revoked=1 WHERE administrator_id=? AND hash<>? AND revoked=0 AND (?='' OR public_id=?) RETURNING hash`, administratorID, current[:], publicID, publicID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	hashes := make([][32]byte, 0)
	for rows.Next() {
		var hash []byte
		if err = rows.Scan(&hash); err != nil {
			return nil, err
		}
		if len(hash) != 32 {
			return nil, ErrRejected
		}
		hashes = append(hashes, [32]byte(hash))
	}
	return hashes, rows.Err()
}

// AtomicSecurityChange updates credentials, replay counters and sessions in one transaction.
func (s *Store) AtomicSecurityChange(ctx context.Context, change identity.SecurityChange) (revoked [][32]byte, err error) {
	err = s.transaction(ctx, func(tx *sql.Tx) error {
		var revision int64
		if err := tx.QueryRowContext(ctx, "SELECT revision FROM administrator WHERE id=?", change.AdministratorID).Scan(&revision); err != nil {
			return err
		}
		if revision != change.Revision {
			return ErrRejected
		}
		if change.Mode != "reset" {
			current, err := useSession(ctx, tx, change.Current.Hash, change.Now, change.IdleTTL, change.AbsoluteTTL)
			if err != nil {
				return err
			}
			if current.AdministratorID != change.AdministratorID {
				return ErrRejected
			}
		}
		switch change.Mode {
		case "start", "recovery":
			if err := requireOne(tx.ExecContext(ctx, "UPDATE administrator SET last_totp_step=? WHERE id=? AND last_totp_step<?", change.Step, change.AdministratorID, change.Step)); err != nil {
				return err
			}
		case "password", "reset", "totp":
		default:
			return ErrRejected
		}
		if change.Mode == "password" || change.Mode == "reset" {
			password, err := json.Marshal(change.Password)
			if err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, "UPDATE administrator SET password=? WHERE id=?", password, change.AdministratorID); err != nil {
				return err
			}
		}
		if change.Mode == "totp" || change.Mode == "reset" {
			if _, err := tx.ExecContext(ctx, "UPDATE administrator SET key_version=?,nonce=?,ciphertext=?,totp_bound_at=?,last_totp_step=? WHERE id=?", change.TOTP.KeyVersion, change.TOTP.Nonce, change.TOTP.Ciphertext, change.Now.UnixMilli(), change.Step, change.AdministratorID); err != nil {
				return err
			}
		}
		if change.Mode == "totp" || change.Mode == "reset" || change.Mode == "recovery" {
			if _, err := tx.ExecContext(ctx, "DELETE FROM recovery_codes WHERE administrator_id=?", change.AdministratorID); err != nil {
				return err
			}
			for _, hash := range change.RecoveryHashes {
				if _, err := tx.ExecContext(ctx, "INSERT INTO recovery_codes (administrator_id,hash) VALUES (?,?)", change.AdministratorID, hash[:]); err != nil {
					return err
				}
			}
		}
		if change.Mode != "start" {
			if _, err := tx.ExecContext(ctx, "UPDATE administrator SET revision=revision+1 WHERE id=?", change.AdministratorID); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, "DELETE FROM auth_challenges WHERE administrator_id=?", change.AdministratorID); err != nil {
				return err
			}
		}
		if change.Mode == "password" || change.Mode == "totp" || change.Mode == "reset" {
			var err error
			revoked, err = revokedHashes(ctx, tx, change.AdministratorID, change.Current.Hash, "")
			if err != nil {
				return err
			}
			if change.Mode != "reset" {
				if change.Replacement.Hash == change.Current.Hash || change.Replacement.AdministratorID != change.AdministratorID {
					return ErrRejected
				}
				if _, err = tx.ExecContext(ctx, "DELETE FROM sessions WHERE hash=?", change.Current.Hash[:]); err != nil {
					return err
				}
				if err = insertSession(ctx, tx, change.Replacement); err != nil {
					return err
				}
			}
		}
		if change.Mode == "reset" {
			_, err := tx.ExecContext(ctx, `INSERT INTO audit_events (time,type,subject_id,credential_id,machine_id,action_id,result,request_id,source_address,detail) VALUES (?,'security.reset-auth',?,'','','','success','','local','')`, change.Now.UnixMilli(), change.AdministratorID)
			return err
		}
		return nil
	})
	return revoked, err
}

// ListSessions returns only sessions within both activity and absolute lifetimes.
func (s *Store) ListSessions(ctx context.Context, administratorID string, now time.Time, idle, absolute time.Duration) ([]identity.Session, error) {
	rows, err := s.reader.QueryContext(ctx, `SELECT hash,public_id,device,source_address,created_at,last_seen FROM sessions WHERE administrator_id=? AND revoked=0 AND created_at<=? AND created_at>? AND last_seen>? ORDER BY created_at,public_id`, administratorID, now.UnixMilli(), now.Add(-absolute).UnixMilli(), now.Add(-idle).UnixMilli())
	if err != nil {
		return nil, storageError(err)
	}
	defer rows.Close()
	sessions := make([]identity.Session, 0)
	for rows.Next() {
		var session identity.Session
		var hash []byte
		var created, seen int64
		if err = rows.Scan(&hash, &session.PublicID, &session.Device, &session.SourceAddress, &created, &seen); err != nil || len(hash) != 32 {
			return nil, ErrRejected
		}
		session.Hash = [32]byte(hash)
		session.CreatedAt = time.UnixMilli(created).UTC()
		session.LastSeen = time.UnixMilli(seen).UTC()
		sessions = append(sessions, session)
	}
	return sessions, rows.Err()
}

// AtomicRevokeSessions checks the initiating session again before revoking other sessions.
func (s *Store) AtomicRevokeSessions(ctx context.Context, current identity.Session, publicID string, now time.Time, idle, absolute time.Duration) (hashes [][32]byte, err error) {
	err = s.transaction(ctx, func(tx *sql.Tx) error {
		session, err := useSession(ctx, tx, current.Hash, now, idle, absolute)
		if err != nil {
			return err
		}
		if publicID != "" && publicID == session.PublicID {
			return ErrRejected
		}
		hashes, err = revokedHashes(ctx, tx, session.AdministratorID, session.Hash, publicID)
		return err
	})
	return hashes, err
}
