-- Conversation records (T037): what a list of conversations shows, one row
-- per conversation that has one. The log (entries) exists without a record.
-- messages counts the log's messages, kept in the same transaction as every
-- append. Times are Unix nanoseconds; updated_at never decreases.
CREATE TABLE conversations (
    tenant     TEXT    NOT NULL,
    id         TEXT    NOT NULL,
    agent      TEXT    NOT NULL,
    title      TEXT    NOT NULL DEFAULT '',
    messages   INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (tenant, id)
);

-- ListConversations: a tenant's records by (created_at, id).
CREATE INDEX conversations_created ON conversations (tenant, created_at, id);
