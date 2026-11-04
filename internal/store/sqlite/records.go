package sqlite

import (
	"context"
	"database/sql"
	"instadeck/internal/account"
	"instadeck/internal/audit"
	"instadeck/internal/operation"
	"time"
)

// Credential stores encrypted key material and declared account capabilities.
type Credential = account.Record

// Action stores the original request and durable upstream tracking state.
type Action = operation.Action

// CreateCredential stores an encrypted credential.
func (s *Store) CreateCredential(ctx context.Context, credential Credential) error {
	_, err := s.writer.ExecContext(ctx, "INSERT INTO credentials (id,name,key_version,nonce,ciphertext,organization_id,capabilities,status,created_at,updated_at,user_id,version,revision,enabled) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)", credential.ID, credential.Name, credential.Envelope.KeyVersion, credential.Envelope.Nonce, credential.Envelope.Ciphertext, credential.OrganizationID, credential.Capabilities, credential.Status, credential.CreatedAt.UnixMilli(), credential.UpdatedAt.UnixMilli(), credential.UserID, credential.Version, credential.Revision, credential.Enabled)
	if err != nil {
		return storageError(err)
	}
	return nil
}

// UpdateCredential replaces mutable credential fields without changing creation time.
func (s *Store) UpdateCredential(ctx context.Context, credential Credential) error {
	return requireOne(s.writer.ExecContext(ctx, "UPDATE credentials SET name=?,key_version=?,nonce=?,ciphertext=?,organization_id=?,capabilities=?,status=?,updated_at=?,user_id=?,version=?,revision=?,enabled=? WHERE id=? AND status<>'deleted'", credential.Name, credential.Envelope.KeyVersion, credential.Envelope.Nonce, credential.Envelope.Ciphertext, credential.OrganizationID, credential.Capabilities, credential.Status, credential.UpdatedAt.UnixMilli(), credential.UserID, credential.Version, credential.Revision, credential.Enabled, credential.ID))
}

// DeleteCredential retains history while erasing credential material.
func (s *Store) DeleteCredential(ctx context.Context, id string) error {
	return requireOne(s.writer.ExecContext(ctx, "UPDATE credentials SET status='deleted',enabled=0,key_version=0,nonce=NULL,ciphertext=NULL,revision=revision+1 WHERE id=?", id))
}

// CredentialByID reads an encrypted credential.
func (s *Store) CredentialByID(ctx context.Context, id string) (Credential, error) {
	var credential Credential
	var created, updated int64
	err := s.reader.QueryRowContext(ctx, "SELECT id,name,key_version,nonce,ciphertext,organization_id,capabilities,status,created_at,updated_at,user_id,version,revision,enabled FROM credentials WHERE id=?", id).Scan(&credential.ID, &credential.Name, &credential.Envelope.KeyVersion, &credential.Envelope.Nonce, &credential.Envelope.Ciphertext, &credential.OrganizationID, &credential.Capabilities, &credential.Status, &created, &updated, &credential.UserID, &credential.Version, &credential.Revision, &credential.Enabled)
	if err != nil {
		return Credential{}, storageError(err)
	}
	credential.CreatedAt = time.UnixMilli(created).UTC()
	credential.UpdatedAt = time.UnixMilli(updated).UTC()
	return credential, nil
}

// ListCredentials returns credentials ordered by identifier.
func (s *Store) ListCredentials(ctx context.Context) ([]Credential, error) {
	rows, err := s.reader.QueryContext(ctx, "SELECT id FROM credentials ORDER BY id")
	if err != nil {
		return nil, storageError(err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) != nil {
			rows.Close()
			return nil, ErrRejected
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, storageError(err)
	}
	records := make([]Credential, 0, len(ids))
	for _, id := range ids {
		record, err := s.CredentialByID(ctx, id)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, nil
}

// CreateAction persists an intent before any upstream request; both idempotency keys are unique.
func (s *Store) CreateAction(ctx context.Context, action Action) error {
	_, err := s.writer.ExecContext(ctx, "INSERT INTO actions (id,credential_id,machine_id,kind,request_body,upstream_idempotency_key,client_request_id,upstream_operation_id,state,phase,error_code,created_at,updated_at,method,path,digest,org_id,credential_version,upstream_state,attempts,next_attempt_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)", action.ID, action.CredentialID, action.MachineID, action.Kind, action.RequestBody, action.UpstreamIdempotencyKey, action.ClientRequestID, action.UpstreamOperationID, action.State, action.Phase, action.ErrorCode, action.CreatedAt.UnixMilli(), action.UpdatedAt.UnixMilli(), action.Method, action.Path, action.Digest, action.OrgID, action.CredentialVersion, action.UpstreamState, action.Attempts, action.NextAttemptAt.UnixMilli())
	if err != nil {
		return storageError(err)
	}
	return nil
}

// CreateActionIntent stores the immutable request and audit event in one transaction.
func (s *Store) CreateActionIntent(ctx context.Context, action Action, event audit.Event) error {
	return s.transaction(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO actions (id,credential_id,machine_id,kind,request_body,upstream_idempotency_key,client_request_id,upstream_operation_id,state,phase,error_code,created_at,updated_at,method,path,digest,org_id,credential_version,upstream_state,attempts,next_attempt_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)", action.ID, action.CredentialID, action.MachineID, action.Kind, action.RequestBody, action.UpstreamIdempotencyKey, action.ClientRequestID, action.UpstreamOperationID, action.State, action.Phase, action.ErrorCode, action.CreatedAt.UnixMilli(), action.UpdatedAt.UnixMilli(), action.Method, action.Path, action.Digest, action.OrgID, action.CredentialVersion, action.UpstreamState, action.Attempts, action.NextAttemptAt.UnixMilli())
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO audit_events (time,type,subject_id,credential_id,machine_id,action_id,result,request_id,source_address,detail) VALUES (?,?,?,?,?,?,?,?,?,?)", event.Time.UnixMilli(), event.Type, event.SubjectID, event.CredentialID, event.MachineID, event.ActionID, event.Result, event.RequestID, event.SourceAddress, event.Detail())
		return err
	})
}

// UpdateAction updates tracking results, preserving the original request and idempotency keys.
func (s *Store) UpdateAction(ctx context.Context, action Action) error {
	return requireOne(s.writer.ExecContext(ctx, "UPDATE actions SET upstream_operation_id=?,state=?,phase=?,error_code=?,updated_at=?,upstream_state=?,attempts=?,next_attempt_at=? WHERE id=?", action.UpstreamOperationID, action.State, action.Phase, action.ErrorCode, action.UpdatedAt.UnixMilli(), action.UpstreamState, action.Attempts, action.NextAttemptAt.UnixMilli(), action.ID))
}

// FinishAction stores a final action state and its audit outcome in one transaction.
func (s *Store) FinishAction(ctx context.Context, action Action, event audit.Event) error {
	return s.transaction(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, "UPDATE actions SET upstream_operation_id=?,state=?,phase=?,error_code=?,updated_at=?,upstream_state=?,attempts=?,next_attempt_at=? WHERE id=?", action.UpstreamOperationID, action.State, action.Phase, action.ErrorCode, action.UpdatedAt.UnixMilli(), action.UpstreamState, action.Attempts, action.NextAttemptAt.UnixMilli(), action.ID)
		if err != nil {
			return err
		}
		if count, err := result.RowsAffected(); err != nil {
			return err
		} else if count != 1 {
			return ErrNotFound
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO audit_events (time,type,subject_id,credential_id,machine_id,action_id,result,request_id,source_address,detail) VALUES (?,?,?,?,?,?,?,?,?,?)", event.Time.UnixMilli(), event.Type, event.SubjectID, event.CredentialID, event.MachineID, event.ActionID, event.Result, event.RequestID, event.SourceAddress, event.Detail())
		return err
	})
}

func (s *Store) actions(ctx context.Context, where string, args ...any) ([]Action, error) {
	rows, err := s.reader.QueryContext(ctx, "SELECT id,credential_id,machine_id,kind,request_body,upstream_idempotency_key,client_request_id,upstream_operation_id,state,phase,error_code,created_at,updated_at,method,path,digest,org_id,credential_version,upstream_state,attempts,next_attempt_at FROM actions "+where+" ORDER BY created_at,id", args...)
	if err != nil {
		return nil, storageError(err)
	}
	defer rows.Close()
	records := []Action{}
	for rows.Next() {
		var action Action
		var created, updated, nextAttempt int64
		if rows.Scan(&action.ID, &action.CredentialID, &action.MachineID, &action.Kind, &action.RequestBody, &action.UpstreamIdempotencyKey, &action.ClientRequestID, &action.UpstreamOperationID, &action.State, &action.Phase, &action.ErrorCode, &created, &updated, &action.Method, &action.Path, &action.Digest, &action.OrgID, &action.CredentialVersion, &action.UpstreamState, &action.Attempts, &nextAttempt) != nil {
			return nil, ErrRejected
		}
		action.NextAttemptAt = time.UnixMilli(nextAttempt).UTC()
		action.CreatedAt = time.UnixMilli(created).UTC()
		action.UpdatedAt = time.UnixMilli(updated).UTC()
		records = append(records, action)
	}
	if rows.Err() != nil {
		return nil, ErrRejected
	}
	return records, nil
}

// ActionByID reads a persisted intent.
func (s *Store) ActionByID(ctx context.Context, id string) (Action, error) {
	records, err := s.actions(ctx, "WHERE id=?", id)
	if err != nil {
		return Action{}, err
	}
	if len(records) != 1 {
		return Action{}, ErrNotFound
	}
	return records[0], nil
}

// ActionByClientRequestID resolves a retried client request to its original intent.
func (s *Store) ActionByClientRequestID(ctx context.Context, id string) (Action, error) {
	records, err := s.actions(ctx, "WHERE client_request_id=?", id)
	if err != nil {
		return Action{}, err
	}
	if len(records) != 1 {
		return Action{}, ErrNotFound
	}
	return records[0], nil
}

// ListUnfinishedActions returns all actions needing restart recovery.
func (s *Store) ListUnfinishedActions(ctx context.Context) ([]Action, error) {
	return s.actions(ctx, "WHERE state IN ('pending','submitting','tracking')")
}

var _ audit.Writer = (*Store)(nil)

// AuditRecord pairs an audit event with its persistent cursor.
type AuditRecord struct {
	Cursor int64
	Event  audit.Event
}

// WriteEvent appends a sanitized audit event.
func (s *Store) WriteEvent(ctx context.Context, event audit.Event) error {
	_, err := s.writer.ExecContext(ctx, "INSERT INTO audit_events (time,type,subject_id,credential_id,machine_id,action_id,result,request_id,source_address,detail) VALUES (?,?,?,?,?,?,?,?,?,?)", event.Time.UnixMilli(), event.Type, event.SubjectID, event.CredentialID, event.MachineID, event.ActionID, event.Result, event.RequestID, event.SourceAddress, event.Detail())
	if err != nil {
		return storageError(err)
	}
	return nil
}

// ReadAuditEvents reads ascending cursor pages; pass the last cursor for the next page.
func (s *Store) ReadAuditEvents(ctx context.Context, after int64, limit int) ([]AuditRecord, error) {
	if after < 0 || limit < 1 || limit > 1000 {
		return nil, ErrRejected
	}
	rows, err := s.reader.QueryContext(ctx, "SELECT id,time,type,subject_id,credential_id,machine_id,action_id,result,request_id,source_address,detail FROM audit_events WHERE id>? ORDER BY id LIMIT ?", after, limit)
	if err != nil {
		return nil, storageError(err)
	}
	defer rows.Close()
	records := []AuditRecord{}
	for rows.Next() {
		var record AuditRecord
		var timestamp int64
		var detail string
		event := &record.Event
		if rows.Scan(&record.Cursor, &timestamp, &event.Type, &event.SubjectID, &event.CredentialID, &event.MachineID, &event.ActionID, &event.Result, &event.RequestID, &event.SourceAddress, &detail) != nil {
			return nil, ErrRejected
		}
		event.Time = time.UnixMilli(timestamp).UTC()
		event.SetDetail(detail)
		records = append(records, record)
	}
	if rows.Err() != nil {
		return nil, ErrRejected
	}
	return records, nil
}

// FindAction resolves a client request while distinguishing absence from storage failure.
func (s *Store) FindAction(ctx context.Context, id string) (Action, bool, error) {
	action, err := s.ActionByClientRequestID(ctx, id)
	if err == ErrNotFound {
		return Action{}, false, nil
	}
	return action, err == nil, err
}
