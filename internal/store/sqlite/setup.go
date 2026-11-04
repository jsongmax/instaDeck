package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"instadeck/internal/identity"
)

// HasAdministrator reports whether initial setup has completed.
func (s *Store) HasAdministrator(ctx context.Context) (bool, error) {
	var count int
	err := s.reader.QueryRowContext(ctx, "SELECT count(*) FROM administrator").Scan(&count)
	return count > 0, storageError(err)
}

// HasProtectedData guards automatic creation of a missing master key.
func (s *Store) HasProtectedData(ctx context.Context) (bool, error) {
	var count int
	err := s.reader.QueryRowContext(ctx, "SELECT (SELECT count(*) FROM administrator)+(SELECT count(*) FROM credentials WHERE status<>'deleted')+(SELECT count(*) FROM notification_channels)").Scan(&count)
	return count > 0, storageError(err)
}

// PanelOrigin reads the persisted origin, returning empty before setup.
func (s *Store) PanelOrigin(ctx context.Context) (string, error) {
	var origin string
	err := s.reader.QueryRowContext(ctx, "SELECT value FROM settings WHERE key='origin'").Scan(&origin)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return origin, storageError(err)
}

// SetPanelOrigin replaces the persisted panel origin.
func (s *Store) SetPanelOrigin(ctx context.Context, origin string) error {
	_, err := s.writer.ExecContext(ctx, "INSERT INTO settings(key,value) VALUES('origin',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", origin)
	return storageError(err)
}

// BoolSetting reads a boolean setting. A missing or malformed value yields the
// fallback, so a damaged row cannot silently disable a protection.
func (s *Store) BoolSetting(ctx context.Context, key string, fallback bool) (bool, error) {
	var value string
	err := s.reader.QueryRowContext(ctx, "SELECT value FROM settings WHERE key=?", key).Scan(&value)
	if err == sql.ErrNoRows {
		return fallback, nil
	}
	if err != nil {
		return fallback, storageError(err)
	}
	switch value {
	case "true":
		return true, nil
	case "false":
		return false, nil
	}
	return fallback, nil
}

// SetBoolSetting replaces a boolean setting.
func (s *Store) SetBoolSetting(ctx context.Context, key string, value bool) error {
	text := "false"
	if value {
		text = "true"
	}
	_, err := s.writer.ExecContext(ctx, "INSERT INTO settings(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", key, text)
	return storageError(err)
}

// CompleteSetup commits the sole administrator, recovery hashes, origin, audit and session together.
func (s *Store) CompleteSetup(ctx context.Context, administrator identity.Administrator, origin string, session identity.Session) error {
	return s.transaction(ctx, func(tx *sql.Tx) error {
		var count int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM administrator").Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			return ErrConflict
		}
		password, err := json.Marshal(administrator.Password)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO administrator(id,username,password,key_version,nonce,ciphertext,last_totp_step,totp_bound_at) VALUES(?,?,?,?,?,?,?,?)`, administrator.ID, administrator.Username, password, administrator.TOTP.KeyVersion, administrator.TOTP.Nonce, administrator.TOTP.Ciphertext, administrator.LastTOTPStep, administrator.TOTPBoundAt.UnixMilli())
		if err != nil {
			return err
		}
		for _, hash := range administrator.RecoveryHashes {
			if _, err = tx.ExecContext(ctx, "INSERT INTO recovery_codes(administrator_id,hash) VALUES(?,?)", administrator.ID, hash[:]); err != nil {
				return err
			}
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO settings(key,value) VALUES('origin',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", origin); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO audit_events(time,type,subject_id,credential_id,machine_id,action_id,result,request_id,source_address,detail) VALUES(?,'setup.complete',?,'','','','success','',?,'')`, session.CreatedAt.UnixMilli(), administrator.ID, session.SourceAddress); err != nil {
			return err
		}
		return insertSession(ctx, tx, session)
	})
}
