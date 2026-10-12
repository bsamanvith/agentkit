-- One flow checkpoint per run, replaced on every Save. body is the
-- checkpoint's JSON without its shape, which has its own column ('' stays '').
-- No foreign key to runs: flow checkpoints may belong to runs the RunStore
-- does not know.
CREATE TABLE checkpoints (
    tenant TEXT COLLATE "C" NOT NULL,
    run_id TEXT COLLATE "C" NOT NULL,
    shape  TEXT NOT NULL DEFAULT '',
    body   TEXT NOT NULL,
    PRIMARY KEY (tenant, run_id)
);
