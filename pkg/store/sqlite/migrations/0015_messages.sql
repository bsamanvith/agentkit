-- Agent messaging (T073): threads, and the messages of each, numbered
-- densely from 1 per thread by hop. kind is request, reply or notice; state
-- is queued, running or done. data holds the exact bytes SendMessage
-- accepted ('' when empty). Times are Unix nanoseconds; NULL is the zero
-- time (not delivered or processed yet, due at once, nobody holds the
-- lease).
CREATE TABLE threads (
    tenant      TEXT    NOT NULL,
    id          TEXT    NOT NULL,
    status      TEXT    NOT NULL DEFAULT 'open',
    stop_code   TEXT    NOT NULL DEFAULT '',
    stop_reason TEXT    NOT NULL DEFAULT '',
    hops        INTEGER NOT NULL DEFAULT 0,
    tokens      INTEGER NOT NULL DEFAULT 0,
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL,
    PRIMARY KEY (tenant, id)
);

-- ListThreads: a tenant's threads by (created_at, id).
CREATE INDEX threads_tenant ON threads (tenant, created_at, id);

CREATE TABLE messages (
    tenant            TEXT    NOT NULL,
    id                TEXT    NOT NULL,
    thread_id         TEXT    NOT NULL,
    hop               INTEGER NOT NULL,
    from_addr         TEXT    NOT NULL,
    to_addr           TEXT    NOT NULL,
    from_conversation TEXT    NOT NULL DEFAULT '',
    to_conversation   TEXT    NOT NULL,
    kind              TEXT    NOT NULL,
    text              TEXT    NOT NULL DEFAULT '',
    data              TEXT    NOT NULL DEFAULT '',
    in_reply_to       TEXT    NOT NULL DEFAULT '',
    state             TEXT    NOT NULL DEFAULT 'queued',
    run_id            TEXT    NOT NULL DEFAULT '',
    attempts          INTEGER NOT NULL DEFAULT 0,
    created_at        INTEGER NOT NULL,
    delivered_at      INTEGER,
    processed_at      INTEGER,
    next_attempt_at   INTEGER,
    lease_owner       TEXT    NOT NULL DEFAULT '',
    lease_expires     INTEGER,
    PRIMARY KEY (tenant, id),
    UNIQUE (tenant, thread_id, hop)
);

-- MessageAdmin's ClaimDueMessage: the messages not done, in claim order;
-- and the queued messages of a mailbox (thread, conversation) by hop.
CREATE INDEX messages_due ON messages (next_attempt_at, created_at, tenant, id) WHERE state <> 'done';
CREATE INDEX messages_mailbox ON messages (tenant, thread_id, to_conversation, hop) WHERE state = 'queued';
