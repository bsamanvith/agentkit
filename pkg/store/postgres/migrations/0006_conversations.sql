-- Conversation records (T037): what a list of conversations shows, one row
-- per conversation that has one. The log (entries) exists without a record.
-- messages counts the log's messages, kept in the same transaction as every
-- append. Times are Unix nanoseconds; updated_at never decreases.
CREATE TABLE conversations (
    tenant     TEXT COLLATE "C" NOT NULL,
    id         TEXT COLLATE "C" NOT NULL,
    agent      TEXT COLLATE "C" NOT NULL,
    title      TEXT   NOT NULL DEFAULT '',
    messages   BIGINT NOT NULL DEFAULT 0,
    created_at BIGINT NOT NULL,
    updated_at BIGINT NOT NULL,
    PRIMARY KEY (tenant, id)
);

-- ListConversations: a tenant's records by (created_at, id).
CREATE INDEX conversations_created ON conversations (tenant, created_at, id);
