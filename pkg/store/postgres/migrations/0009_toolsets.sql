-- Toolsets added at run time (T096), per tenant. data holds the exact JSON
-- bytes PutToolset accepted. version is 1 when the toolset is created and one
-- more on each replace; writes compare it under the row's lock. Times are Unix
-- nanoseconds.
CREATE TABLE toolsets (
    tenant     TEXT COLLATE "C" NOT NULL,
    name       TEXT COLLATE "C" NOT NULL,
    data       TEXT   NOT NULL,
    version    BIGINT NOT NULL,
    created_at BIGINT NOT NULL,
    updated_at BIGINT NOT NULL,
    PRIMARY KEY (tenant, name)
);

-- Row-level security, as migration 0008 sets it on the other tables.
ALTER TABLE toolsets ENABLE ROW LEVEL SECURITY;
ALTER TABLE toolsets FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON toolsets
    USING (tenant = current_setting('app.tenant', true))
    WITH CHECK (tenant = current_setting('app.tenant', true));
