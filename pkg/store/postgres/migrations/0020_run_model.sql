-- The model a run runs (T125): "<provider>/<id>", chosen from the models
-- its agent allows. Runs that exist already ran their agent's only model. A
-- constant default rewrites no rows, so row-level security needs no lifting
-- here.
ALTER TABLE runs ADD COLUMN model TEXT COLLATE "C" NOT NULL DEFAULT '';
