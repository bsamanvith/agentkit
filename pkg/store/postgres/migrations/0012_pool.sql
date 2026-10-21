-- Runner pools (T105): the pool a run was placed in, where its agent runs.
-- Runs that exist already were placed by no pool. A constant default
-- rewrites no rows, so row-level security needs no lifting here.
ALTER TABLE runs ADD COLUMN pool TEXT COLLATE "C" NOT NULL DEFAULT '';
