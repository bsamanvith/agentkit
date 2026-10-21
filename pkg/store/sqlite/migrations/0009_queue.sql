-- The run queue (T099): a run's lane, its priority within the lane, and the
-- time it may start. Runs that exist already are in the default lane at
-- priority 0 and may start when they were created, so they are resumable
-- and claimable as before.
ALTER TABLE runs ADD COLUMN lane TEXT NOT NULL DEFAULT '';
ALTER TABLE runs ADD COLUMN priority INTEGER NOT NULL DEFAULT 0;
ALTER TABLE runs ADD COLUMN start_at INTEGER NOT NULL DEFAULT 0;
UPDATE runs SET start_at = created_at;

-- Admin's ClaimNext: the queued runs of a lane (background runs that are
-- running, or failed and retryable) in claim order, highest priority first,
-- then by the time each is due (store.Run.Due: the latest of its start, its
-- lease's expiry and its next attempt). The query names the same expression
-- and the same predicate, word for word, so that SQLite uses the index.
CREATE INDEX runs_queue ON runs (lane, priority DESC,
    max(start_at, coalesce(lease_expires, start_at), coalesce(next_attempt_at, start_at)), tenant, id)
    WHERE interactive = 0 AND (status = 'running' OR (status = 'failed' AND retryable = 1));
