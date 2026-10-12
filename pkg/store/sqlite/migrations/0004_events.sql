-- The event logs of runs: one row per event, numbered densely from 1 per run.
-- Append only. data holds the exact bytes AppendEvents accepted ('' when
-- empty); created_at is Unix nanoseconds and never decreases along a log. The
-- run id is a label, with no foreign key to runs.
CREATE TABLE events (
    tenant     TEXT    NOT NULL,
    run_id     TEXT    NOT NULL,
    seq        INTEGER NOT NULL,
    type       TEXT    NOT NULL,
    data       TEXT    NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    PRIMARY KEY (tenant, run_id, seq)
);
