-- Toolsets added at run time (T096), per tenant. data holds the exact JSON
-- bytes PutToolset accepted. version is 1 when the toolset is created and one
-- more on each replace; writes compare it and set it in one transaction.
-- Times are Unix nanoseconds.
CREATE TABLE toolsets (
    tenant     TEXT    NOT NULL,
    name       TEXT    NOT NULL,
    data       TEXT    NOT NULL,
    version    INTEGER NOT NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (tenant, name)
);
