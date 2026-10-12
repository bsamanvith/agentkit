-- The conversation log: one row per entry, numbered densely from 1 per
-- conversation. The primary key makes create-if-absent a plain INSERT.
-- messages and record hold the exact bytes Append accepted (record is '' when
-- empty), as TEXT: JSONB would re-encode them. created_at is Unix nanoseconds
-- and never decreases along a log. Ids use the "C" collation, so they compare
-- as bytes, as in SQLite and in Go.
CREATE TABLE entries (
    tenant       TEXT COLLATE "C" NOT NULL,
    conversation TEXT COLLATE "C" NOT NULL,
    seq          BIGINT  NOT NULL,
    kind         TEXT    NOT NULL,
    messages     TEXT    NOT NULL,
    record       TEXT    NOT NULL DEFAULT '',
    run_id       TEXT    NOT NULL DEFAULT '',
    covers       BIGINT  NOT NULL DEFAULT 0,
    created_at   BIGINT  NOT NULL,
    PRIMARY KEY (tenant, conversation, seq)
);

-- Context finds the latest compaction without scanning the log.
CREATE INDEX entries_compactions ON entries (tenant, conversation, seq) WHERE kind = 'compaction';

-- History seeks by time. created_at never decreases along seq, so
-- (created_at, seq) order is seq order, read from the index without a sort.
CREATE INDEX entries_created ON entries (tenant, conversation, created_at, seq);
