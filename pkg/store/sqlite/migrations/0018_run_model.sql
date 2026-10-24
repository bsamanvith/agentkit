-- The model a run runs (T125): "<provider>/<id>", chosen from the models
-- its agent allows. Runs that exist already ran their agent's only model.
ALTER TABLE runs ADD COLUMN model TEXT NOT NULL DEFAULT '';
