-- A credential that needs reconnecting (T044, pkg/credentials/sqlite): an
-- OAuth2 refresh answered invalid_grant, so a person has to connect the
-- account again. 1 marks it; any other write of the credential sets 0.
ALTER TABLE credential_blobs ADD COLUMN needs_reconnect INTEGER NOT NULL DEFAULT 0;
