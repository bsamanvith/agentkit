-- Commands sent to runs (T090): steering, interrupt answers, cancel. One row
-- per command, numbered densely from 1 per run; idempotency_key is the
-- sender's key. data holds the exact bytes SendCommand accepted ('' when
-- empty). Times are Unix nanoseconds; consumed_at is NULL while the command is
-- pending. Rows are written only for runs that exist, so a run's commands go
-- with it.
CREATE TABLE commands (
    tenant          TEXT COLLATE "C" NOT NULL,
    run_id          TEXT COLLATE "C" NOT NULL,
    seq             BIGINT NOT NULL,
    idempotency_key TEXT COLLATE "C" NOT NULL,
    type            TEXT   NOT NULL,
    data            TEXT   NOT NULL DEFAULT '',
    created_at      BIGINT NOT NULL,
    consumed_at     BIGINT,
    PRIMARY KEY (tenant, run_id, seq),
    UNIQUE (tenant, run_id, idempotency_key)
);

-- PendingCommands.
CREATE INDEX commands_pending ON commands (tenant, run_id, seq) WHERE consumed_at IS NULL;
