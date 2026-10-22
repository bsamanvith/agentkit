-- The user a run was started for (T090): whose notifications say how it
-- goes. Runs that exist already were started for nobody in particular. A
-- constant default rewrites no rows, so row-level security needs no lifting
-- here.
ALTER TABLE runs ADD COLUMN user_id TEXT COLLATE "C" NOT NULL DEFAULT '';
