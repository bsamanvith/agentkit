-- The run queue (T099): a run's lane, its priority within the lane, and the
-- time it may start. Runs that exist already are in the default lane at
-- priority 0 and may start when they were created, so they are resumable
-- and claimable as before.
ALTER TABLE runs ADD COLUMN lane TEXT COLLATE "C" NOT NULL DEFAULT '';
ALTER TABLE runs ADD COLUMN priority INTEGER NOT NULL DEFAULT 0;
ALTER TABLE runs ADD COLUMN start_at BIGINT;

-- The backfill must reach every tenant's rows. Row-level security is forced
-- on the table's owner too (0008), and the owner is who migrates, so it is
-- lifted for this one statement; the transaction holds the table's lock
-- throughout, so no other session sees it lifted.
ALTER TABLE runs NO FORCE ROW LEVEL SECURITY;
UPDATE runs SET start_at = created_at;
ALTER TABLE runs FORCE ROW LEVEL SECURITY;
ALTER TABLE runs ALTER COLUMN start_at SET NOT NULL;

-- Admin's ClaimNext: the queued runs of a lane (background runs that are
-- running, or failed and retryable) in claim order, highest priority first,
-- then by the time each is due (store.Run.Due: the latest of its start, its
-- lease's expiry and its next attempt; GREATEST skips NULLs). A held lease
-- puts its run's due time in the future, so within a priority the running
-- runs sort after the due ones and a scan never steps over them.
CREATE INDEX runs_queue ON runs (lane, priority DESC,
    (GREATEST(start_at, lease_expires, next_attempt_at)), tenant, id)
    WHERE NOT interactive AND (status = 'running' OR (status = 'failed' AND retryable));
