-- The event logs of runs: one row per event, numbered densely from 1 per run.
-- Append only. data holds the exact bytes AppendEvents accepted ('' when
-- empty); created_at is Unix nanoseconds and never decreases along a log. The
-- run id is a label, with no foreign key to runs.
CREATE TABLE events (
    tenant     TEXT COLLATE "C" NOT NULL,
    run_id     TEXT COLLATE "C" NOT NULL,
    seq        BIGINT NOT NULL,
    type       TEXT   NOT NULL,
    data       TEXT   NOT NULL DEFAULT '',
    created_at BIGINT NOT NULL,
    PRIMARY KEY (tenant, run_id, seq)
);
