-- Triggers (T072): what starts background runs on a schedule or when a
-- webhook is called. data holds the exact JSON bytes PutTrigger accepted;
-- version is compared and set in one transaction, as toolsets' is.
-- hook_id is '' for a trigger with no webhook, and unique across tenants
-- otherwise. last_firing_seq is the Seq of the trigger's newest firing ever
-- started, so that Seqs keep growing after old firings are dropped. Times are
-- Unix nanoseconds; NULL is the zero time.
CREATE TABLE triggers (
    tenant          TEXT    NOT NULL,
    id              TEXT    NOT NULL,
    data            TEXT    NOT NULL,
    hook_id         TEXT    NOT NULL DEFAULT '',
    paused          INTEGER NOT NULL DEFAULT 0,
    next_fire_at    INTEGER,
    version         INTEGER NOT NULL,
    created_at      INTEGER NOT NULL,
    updated_at      INTEGER NOT NULL,
    last_firing_seq INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (tenant, id)
);

CREATE UNIQUE INDEX triggers_hook ON triggers (hook_id) WHERE hook_id <> '';

-- Admin's DueTriggers: the triggers that may come due, in firing order.
CREATE INDEX triggers_due ON triggers (next_fire_at, tenant, id)
    WHERE paused = 0 AND next_fire_at IS NOT NULL;

-- A trigger's firings, deleted with it: the newest store.MaxFirings.
CREATE TABLE firings (
    tenant        TEXT    NOT NULL,
    trigger_id    TEXT    NOT NULL,
    id            TEXT    NOT NULL,
    seq           INTEGER NOT NULL,
    scheduled_for INTEGER,
    at            INTEGER NOT NULL,
    state         TEXT    NOT NULL,
    run_id        TEXT    NOT NULL DEFAULT '',
    reason        TEXT    NOT NULL DEFAULT '',
    ended_at      INTEGER,
    PRIMARY KEY (tenant, trigger_id, id)
);

CREATE INDEX firings_seq ON firings (tenant, trigger_id, seq);
