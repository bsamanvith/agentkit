-- Runs and their leases. The lease lives on the run's row, so a claim is one
-- conditional UPDATE and the resumable indexes cover it. Times are Unix
-- nanoseconds; NULL is the zero time. pending is a JSON array of the sorted
-- paths ('' when none). retryable is 0 or 1 (a boolean in Postgres).
CREATE TABLE runs (
    tenant             TEXT    NOT NULL,
    id                 TEXT    NOT NULL,
    conversation       TEXT    NOT NULL,
    status             TEXT    NOT NULL,
    input_tokens       INTEGER NOT NULL DEFAULT 0,
    output_tokens      INTEGER NOT NULL DEFAULT 0,
    cache_read_tokens  INTEGER NOT NULL DEFAULT 0,
    cache_write_tokens INTEGER NOT NULL DEFAULT 0,
    error              TEXT    NOT NULL DEFAULT '',
    retryable          INTEGER NOT NULL DEFAULT 0,
    next_attempt_at    INTEGER,
    attempts           INTEGER NOT NULL DEFAULT 0,
    pending            TEXT    NOT NULL DEFAULT '',
    created_at         INTEGER NOT NULL,
    finished_at        INTEGER,
    lease_owner        TEXT    NOT NULL DEFAULT '',
    lease_expires      INTEGER,
    PRIMARY KEY (tenant, id)
);

-- At most one unfinished run per conversation, race-free across processes.
-- Create maps its violation to ErrUnfinished; Unfinished reads through it.
CREATE UNIQUE INDEX runs_unfinished ON runs (tenant, conversation)
    WHERE status NOT IN ('completed', 'rejected', 'canceled');

-- List: a conversation's runs by (created_at, id).
CREATE INDEX runs_conversation ON runs (tenant, conversation, created_at, id);

-- ListSuspended.
CREATE INDEX runs_suspended ON runs (tenant, created_at, id) WHERE status = 'suspended';

-- ListResumable (per tenant) and Admin's ListResumableAll (every tenant), both
-- walked in output order. Suspended and final runs, the bulk of the table over
-- time, are in neither.
CREATE INDEX runs_resumable ON runs (tenant, created_at, id) WHERE status IN ('running', 'failed');
CREATE INDEX runs_resumable_all ON runs (created_at, tenant, id) WHERE status IN ('running', 'failed');
