package sqlite

import (
	"context"
	"instadeck/internal/account"
)

var _ account.Repository = (*Store)(nil)

// CompareAndSwapCredential atomically updates a live credential at the expected revision.
func (s *Store) CompareAndSwapCredential(ctx context.Context, credential account.Record, expectedRevision int) error {
	result, err := s.writer.ExecContext(ctx, `UPDATE credentials SET name=?,key_version=?,nonce=?,ciphertext=?,organization_id=?,capabilities=?,status=?,updated_at=?,user_id=?,version=?,revision=?,enabled=? WHERE id=? AND revision=? AND status<>'deleted'`, credential.Name, credential.Envelope.KeyVersion, credential.Envelope.Nonce, credential.Envelope.Ciphertext, credential.OrganizationID, credential.Capabilities, credential.Status, credential.UpdatedAt.UnixMilli(), credential.UserID, credential.Version, credential.Revision, credential.Enabled, credential.ID, expectedRevision)
	if err != nil {
		return storageError(err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return storageError(err)
	}
	if count != 1 {
		return ErrConflict
	}
	return nil
}
