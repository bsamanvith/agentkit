-- Agent messaging (T073): threads, and the messages of each, numbered
-- densely from 1 per thread by hop. kind is request, reply or notice; state
-- is queued, running or done. data holds the exact bytes SendMessage
-- accepted ('' when empty). Times are Unix nanoseconds; NULL is the zero
-- time (not delivered or processed yet, due at once, nobody holds the
-- lease).
CREATE TABLE threads (
    tenant      TEXT COLLATE "C" NOT NULL,
    id          TEXT COLLATE "C" NOT NULL,
    status      TEXT   NOT NULL DEFAULT 'open',
    stop_code   TEXT   NOT NULL DEFAULT '',
    stop_reason TEXT   NOT NULL DEFAULT '',
    hops        INTEGER NOT NULL DEFAULT 0,
    tokens      BIGINT NOT NULL DEFAULT 0,
    created_at  BIGINT NOT NULL,
    updated_at  BIGINT NOT NULL,
    PRIMARY KEY (tenant, id)
);

-- ListThreads: a tenant's threads by (created_at, id).
CREATE INDEX threads_tenant ON threads (tenant, created_at, id);

CREATE TABLE messages (
    tenant            TEXT COLLATE "C" NOT NULL,
    id                TEXT COLLATE "C" NOT NULL,
    thread_id         TEXT COLLATE "C" NOT NULL,
    hop               INTEGER NOT NULL,
    from_addr         TEXT    NOT NULL,
    to_addr           TEXT    NOT NULL,
    from_conversation TEXT COLLATE "C" NOT NULL DEFAULT '',
    to_conversation   TEXT COLLATE "C" NOT NULL,
    kind              TEXT    NOT NULL,
    text              TEXT    NOT NULL DEFAULT '',
    data              TEXT    NOT NULL DEFAULT '',
    in_reply_to       TEXT COLLATE "C" NOT NULL DEFAULT '',
    state             TEXT    NOT NULL DEFAULT 'queued',
    run_id            TEXT COLLATE "C" NOT NULL DEFAULT '',
    attempts          INTEGER NOT NULL DEFAULT 0,
    created_at        BIGINT  NOT NULL,
    delivered_at      BIGINT,
    processed_at      BIGINT,
    next_attempt_at   BIGINT,
    lease_owner       TEXT    NOT NULL DEFAULT '',
    lease_expires     BIGINT,
    PRIMARY KEY (tenant, id),
    UNIQUE (tenant, thread_id, hop)
);

-- MessageAdmin's ClaimDueMessage: the messages not done, in claim order;
-- and the queued messages of a mailbox (thread, conversation) by hop.
CREATE INDEX messages_due ON messages (next_attempt_at NULLS FIRST, created_at, tenant, id) WHERE state <> 'done';
CREATE INDEX messages_mailbox ON messages (tenant, thread_id, to_conversation, hop) WHERE state = 'queued';

-- Row-level security, as on every other table (0008).
ALTER TABLE threads ENABLE ROW LEVEL SECURITY;
ALTER TABLE threads FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON threads
    USING (tenant = current_setting('app.tenant', true))
    WITH CHECK (tenant = current_setting('app.tenant', true));
ALTER TABLE messages ENABLE ROW LEVEL SECURITY;
ALTER TABLE messages FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON messages
    USING (tenant = current_setting('app.tenant', true))
    WITH CHECK (tenant = current_setting('app.tenant', true));
