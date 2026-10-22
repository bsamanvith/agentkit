-- Tasks (T071): the notification told when a run ends, at most one per run,
-- deleted with the run. state is pending, delivered or failed; attempts is
-- the JSON array of attempts so far. Times are Unix nanoseconds; NULL is the
-- zero time (next_attempt_at before the first attempt, lease_expires when
-- nobody holds the lease).
CREATE TABLE notifications (
    tenant          TEXT    NOT NULL,
    run_id          TEXT    NOT NULL,
    url             TEXT    NOT NULL,
    state           TEXT    NOT NULL,
    attempts        TEXT    NOT NULL DEFAULT '[]',
    next_attempt_at INTEGER,
    created_at      INTEGER NOT NULL,
    lease_owner     TEXT    NOT NULL DEFAULT '',
    lease_expires   INTEGER,
    PRIMARY KEY (tenant, run_id)
);

-- Admin's ClaimNotification: the pending notifications in claim order.
CREATE INDEX notifications_due ON notifications (next_attempt_at, created_at, tenant, run_id)
    WHERE state = 'pending';

-- ListRuns: a tenant's runs by (created_at, id), across conversations.
CREATE INDEX runs_tenant ON runs (tenant, created_at, id);
