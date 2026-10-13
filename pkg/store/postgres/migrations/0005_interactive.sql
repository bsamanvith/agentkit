-- Interactive runs (T100): chat runs with a user on the other end. They are
-- never resumable, so the runs_resumable indexes keep listing what
-- ListResumable reads; runs that exist already are background runs.
ALTER TABLE runs ADD COLUMN interactive BOOLEAN NOT NULL DEFAULT false;
