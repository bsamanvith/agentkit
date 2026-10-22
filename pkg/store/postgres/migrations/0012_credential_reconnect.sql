-- A credential that needs reconnecting (T044, pkg/credentials/postgres), as
-- in SQLite's migration 0011: an OAuth2 refresh answered invalid_grant, so a
-- person has to connect the account again. Any other write of the
-- credential sets it false.
ALTER TABLE credential_blobs ADD COLUMN needs_reconnect BOOLEAN NOT NULL DEFAULT FALSE;
