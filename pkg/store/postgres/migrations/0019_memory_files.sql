-- The memory context store's files (T122, pkg/memory/postgres), as the
-- SQLite backend's memory_files table. user_id is '' for a tenant-scoped
-- store. content is readable text (memory.CheckText); size is its bytes.
-- updated is Unix nanoseconds.
CREATE TABLE memory_files (
    tenant  TEXT COLLATE "C" NOT NULL,
    user_id TEXT COLLATE "C" NOT NULL,
    path    TEXT COLLATE "C" NOT NULL,
    content TEXT   NOT NULL,
    size    BIGINT NOT NULL,
    updated BIGINT NOT NULL,
    PRIMARY KEY (tenant, user_id, path)
);

-- Row-level security, as migration 0008 sets it on the other tables.
ALTER TABLE memory_files ENABLE ROW LEVEL SECURITY;
ALTER TABLE memory_files FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON memory_files
    USING (tenant = current_setting('app.tenant', true))
    WITH CHECK (tenant = current_setting('app.tenant', true));
