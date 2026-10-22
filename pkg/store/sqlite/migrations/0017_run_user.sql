-- The user a run was started for (T090): whose notifications say how it
-- goes. Runs that exist already were started for nobody in particular.
ALTER TABLE runs ADD COLUMN user_id TEXT NOT NULL DEFAULT '';
