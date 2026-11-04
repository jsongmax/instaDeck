CREATE INDEX audit_events_credential_cursor ON audit_events(credential_id,id);
CREATE INDEX audit_events_machine_cursor ON audit_events(machine_id,id);
CREATE INDEX audit_events_result_cursor ON audit_events(result,id);
CREATE INDEX audit_events_time_cursor ON audit_events(time,id);
CREATE TABLE notification_channels (
 id TEXT PRIMARY KEY, type TEXT NOT NULL, name TEXT NOT NULL, enabled INTEGER NOT NULL,
 events TEXT NOT NULL, key_version INTEGER NOT NULL, nonce BLOB NOT NULL, ciphertext BLOB NOT NULL
);
