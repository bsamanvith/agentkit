-- The credential vault's sealed blobs (T043, pkg/credentials/postgres), as in
-- SQLite's migration 0009. blob is ciphertext only: the vault seals before it
-- stores. user_id is '' for a tenant's shared credential ("user" is a
-- reserved word). version is 1 on the first write of a key and one more on
-- each write after; writes compare it in the UPDATE's WHERE. A deleted
-- credential keeps its row with blob NULL, so its version never repeats.
-- Times are Unix nanoseconds, expiry NULL for none.
CREATE TABLE credential_blobs (
    tenant     TEXT COLLATE "C" NOT NULL,
    user_id    TEXT COLLATE "C" NOT NULL,
    slot       TEXT COLLATE "C" NOT NULL,
    version    BIGINT NOT NULL,
    kind       TEXT   NOT NULL,
    blob       BYTEA,
    created_at BIGINT NOT NULL,
    updated_at BIGINT NOT NULL,
    expiry     BIGINT,
    PRIMARY KEY (tenant, user_id, slot)
);

-- Row-level security, as migration 0008 sets it on the other tables.
ALTER TABLE credential_blobs ENABLE ROW LEVEL SECURITY;
ALTER TABLE credential_blobs FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON credential_blobs
    USING (tenant = current_setting('app.tenant', true))
    WITH CHECK (tenant = current_setting('app.tenant', true));
