-- Versioned items (T075), per tenant: agent definitions and the like. An
-- item names its published version and the last number it gave; each
-- version is immutable but for published_at, set the first time it is
-- published. data holds the exact JSON bytes SaveItem accepted. Times are
-- Unix nanoseconds; published_at is 0 for a version never published.
CREATE TABLE items (
    tenant     TEXT    NOT NULL,
    kind       TEXT    NOT NULL,
    name       TEXT    NOT NULL,
    latest     INTEGER NOT NULL,
    published  INTEGER NOT NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (tenant, kind, name)
);

CREATE TABLE item_versions (
    tenant       TEXT    NOT NULL,
    kind         TEXT    NOT NULL,
    name         TEXT    NOT NULL,
    version      INTEGER NOT NULL,
    data         TEXT    NOT NULL,
    author       TEXT    NOT NULL,
    message      TEXT    NOT NULL,
    created_at   INTEGER NOT NULL,
    published_at INTEGER NOT NULL,
    PRIMARY KEY (tenant, kind, name, version)
);

-- The version of its agent a run runs (T075); 0 for an agent that is not
-- versioned (from the config file) and for runs from before.
ALTER TABLE runs ADD COLUMN agent_version INTEGER NOT NULL DEFAULT 0;
