-- Versioned items (T075), per tenant: agent definitions and the like. An
-- item names its published version and the last number it gave; each
-- version is immutable but for published_at, set the first time it is
-- published. data holds the exact JSON bytes SaveItem accepted. Times are
-- Unix nanoseconds; published_at is 0 for a version never published.
CREATE TABLE items (
    tenant     TEXT COLLATE "C" NOT NULL,
    kind       TEXT COLLATE "C" NOT NULL,
    name       TEXT COLLATE "C" NOT NULL,
    latest     BIGINT NOT NULL,
    published  BIGINT NOT NULL,
    created_at BIGINT NOT NULL,
    updated_at BIGINT NOT NULL,
    PRIMARY KEY (tenant, kind, name)
);

CREATE TABLE item_versions (
    tenant       TEXT COLLATE "C" NOT NULL,
    kind         TEXT COLLATE "C" NOT NULL,
    name         TEXT COLLATE "C" NOT NULL,
    version      BIGINT NOT NULL,
    data         TEXT   NOT NULL,
    author       TEXT   NOT NULL,
    message      TEXT   NOT NULL,
    created_at   BIGINT NOT NULL,
    published_at BIGINT NOT NULL,
    PRIMARY KEY (tenant, kind, name, version)
);

-- Row-level security, as migration 0008 sets it on the other tables.
ALTER TABLE items ENABLE ROW LEVEL SECURITY;
ALTER TABLE items FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON items
    USING (tenant = current_setting('app.tenant', true))
    WITH CHECK (tenant = current_setting('app.tenant', true));
ALTER TABLE item_versions ENABLE ROW LEVEL SECURITY;
ALTER TABLE item_versions FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON item_versions
    USING (tenant = current_setting('app.tenant', true))
    WITH CHECK (tenant = current_setting('app.tenant', true));

-- The version of its agent a run runs (T075); 0 for an agent that is not
-- versioned (from the config file) and for runs from before. A constant
-- default rewrites no rows, so row-level security needs no lifting here.
ALTER TABLE runs ADD COLUMN agent_version BIGINT NOT NULL DEFAULT 0;
