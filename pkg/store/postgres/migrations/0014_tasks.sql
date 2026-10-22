-- Tasks (T071): the notification told when a run ends, at most one per run,
-- deleted with the run. state is pending, delivered or failed; attempts is
-- the JSON array of attempts so far. Times are Unix nanoseconds; NULL is the
-- zero time (next_attempt_at before the first attempt, lease_expires when
-- nobody holds the lease).
CREATE TABLE notifications (
    tenant          TEXT COLLATE "C" NOT NULL,
    run_id          TEXT COLLATE "C" NOT NULL,
    url             TEXT   NOT NULL,
    state           TEXT   NOT NULL,
    attempts        TEXT   NOT NULL DEFAULT '[]',
    next_attempt_at BIGINT,
    created_at      BIGINT NOT NULL,
    lease_owner     TEXT   NOT NULL DEFAULT '',
    lease_expires   BIGINT,
    PRIMARY KEY (tenant, run_id)
);

-- Admin's ClaimNotification: the pending notifications in claim order.
CREATE INDEX notifications_due ON notifications (next_attempt_at NULLS FIRST, created_at, tenant, run_id)
    WHERE state = 'pending';

-- Row-level security, as on every other table (0008).
ALTER TABLE notifications ENABLE ROW LEVEL SECURITY;
ALTER TABLE notifications FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON notifications
    USING (tenant = current_setting('app.tenant', true))
    WITH CHECK (tenant = current_setting('app.tenant', true));

-- ListRuns: a tenant's runs by (created_at, id), across conversations.
CREATE INDEX runs_tenant ON runs (tenant, created_at, id);
