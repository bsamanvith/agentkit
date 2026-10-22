-- Triggers (T072): what starts background runs on a schedule or when a
-- webhook is called. data holds the exact JSON bytes PutTrigger accepted;
-- version is compared under the row's lock, as toolsets' is. hook_id is ''
-- for a trigger with no webhook, and unique across tenants otherwise (the
-- index sees every tenant's rows, whatever row-level security shows).
-- last_firing_seq is the Seq of the trigger's newest firing ever started, so
-- that Seqs keep growing after old firings are dropped. Times are Unix
-- nanoseconds; NULL is the zero time.
CREATE TABLE triggers (
    tenant          TEXT COLLATE "C" NOT NULL,
    id              TEXT COLLATE "C" NOT NULL,
    data            TEXT    NOT NULL,
    hook_id         TEXT COLLATE "C" NOT NULL DEFAULT '',
    paused          BOOLEAN NOT NULL DEFAULT FALSE,
    next_fire_at    BIGINT,
    version         BIGINT  NOT NULL,
    created_at      BIGINT  NOT NULL,
    updated_at      BIGINT  NOT NULL,
    last_firing_seq BIGINT  NOT NULL DEFAULT 0,
    PRIMARY KEY (tenant, id)
);

CREATE UNIQUE INDEX triggers_hook ON triggers (hook_id) WHERE hook_id <> '';

-- Admin's DueTriggers: the triggers that may come due, in firing order.
CREATE INDEX triggers_due ON triggers (next_fire_at, tenant, id)
    WHERE NOT paused AND next_fire_at IS NOT NULL;

-- A trigger's firings, deleted with it: the newest store.MaxFirings.
CREATE TABLE firings (
    tenant        TEXT COLLATE "C" NOT NULL,
    trigger_id    TEXT COLLATE "C" NOT NULL,
    id            TEXT COLLATE "C" NOT NULL,
    seq           BIGINT NOT NULL,
    scheduled_for BIGINT,
    at            BIGINT NOT NULL,
    state         TEXT   NOT NULL,
    run_id        TEXT   NOT NULL DEFAULT '',
    reason        TEXT   NOT NULL DEFAULT '',
    ended_at      BIGINT,
    PRIMARY KEY (tenant, trigger_id, id)
);

CREATE INDEX firings_seq ON firings (tenant, trigger_id, seq);

-- Row-level security, as on every other table (0008).
ALTER TABLE triggers ENABLE ROW LEVEL SECURITY;
ALTER TABLE triggers FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON triggers
    USING (tenant = current_setting('app.tenant', true))
    WITH CHECK (tenant = current_setting('app.tenant', true));
ALTER TABLE firings ENABLE ROW LEVEL SECURITY;
ALTER TABLE firings FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON firings
    USING (tenant = current_setting('app.tenant', true))
    WITH CHECK (tenant = current_setting('app.tenant', true));
