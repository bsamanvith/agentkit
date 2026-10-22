-- Runner pools (T105): the pool a run was placed in, where its agent runs.
-- Runs that exist already were placed by no pool.
ALTER TABLE runs ADD COLUMN pool TEXT NOT NULL DEFAULT '';
