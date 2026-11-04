package sqlite

import (
	"context"
	"encoding/json"
	"instadeck/internal/notify"
)

func (s *Store) ListChannels(ctx context.Context) ([]notify.Channel, error) {
	rows, err := s.reader.QueryContext(ctx, "SELECT id,type,name,enabled,events,key_version,nonce,ciphertext FROM notification_channels ORDER BY id")
	if err != nil {
		return nil, storageError(err)
	}
	defer rows.Close()
	values := []notify.Channel{}
	for rows.Next() {
		var c notify.Channel
		var events string
		if err = rows.Scan(&c.ID, &c.Type, &c.Name, &c.Enabled, &events, &c.Envelope.KeyVersion, &c.Envelope.Nonce, &c.Envelope.Ciphertext); err != nil {
			return nil, storageError(err)
		}
		if json.Unmarshal([]byte(events), &c.Events) != nil {
			return nil, ErrRejected
		}
		values = append(values, c)
	}
	return values, storageError(rows.Err())
}
func (s *Store) SaveChannel(ctx context.Context, c notify.Channel, create bool) error {
	events, _ := json.Marshal(c.Events)
	if create {
		_, err := s.writer.ExecContext(ctx, "INSERT INTO notification_channels(id,type,name,enabled,events,key_version,nonce,ciphertext)VALUES(?,?,?,?,?,?,?,?)", c.ID, c.Type, c.Name, c.Enabled, string(events), c.Envelope.KeyVersion, c.Envelope.Nonce, c.Envelope.Ciphertext)
		return storageError(err)
	}
	return requireOne(s.writer.ExecContext(ctx, "UPDATE notification_channels SET type=?,name=?,enabled=?,events=?,key_version=?,nonce=?,ciphertext=? WHERE id=?", c.Type, c.Name, c.Enabled, string(events), c.Envelope.KeyVersion, c.Envelope.Nonce, c.Envelope.Ciphertext, c.ID))
}
func (s *Store) DeleteChannel(ctx context.Context, id string) error {
	return requireOne(s.writer.ExecContext(ctx, "DELETE FROM notification_channels WHERE id=?", id))
}
