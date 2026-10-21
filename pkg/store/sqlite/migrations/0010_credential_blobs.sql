-- The credential vault's sealed blobs (T043, pkg/credentials/sqlite), in the
-- store's file. blob is ciphertext only: the vault seals before it stores.
-- user_id is "" for a tenant's shared credential. version is 1 on the first
-- write of a key and one more on each write after; writes compare it in the
-- UPDATE's WHERE. A deleted credential keeps its row with blob NULL, so its
-- version never repeats. Times are Unix nanoseconds, expiry NULL for none.
CREATE TABLE credential_blobs (
    tenant     TEXT    NOT NULL,
    user_id    TEXT    NOT NULL,
    slot       TEXT    NOT NULL,
    version    INTEGER NOT NULL,
    kind       TEXT    NOT NULL,
    blob       BLOB,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    expiry     INTEGER,
    PRIMARY KEY (tenant, user_id, slot)
);
