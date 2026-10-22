---
id: T059
title: "Memory search store: interface, conformance suite, keyword backends, auto_recall"
epic: E12
status: done
depends_on: [T058]
---

# T059: memory search store, keyword search

## Context

Recall over many items: past conversations, documents, facts. Opt-in, with save, search and retrieve. This task defines the search interface and the keyword (phrase) backends, and wires recall into runs. Semantic search follows in T060. Decisions: [E12](E12-skills-memory.md).

## Scope

- **Types:**
  - `memory.Index`: `Put(ctx, Item{ID, Text, Metadata map[string]string, Time})` (upsert), `Search(ctx, Query{Text, K, Filter, Mode})`, `Get`, `Delete`, `Modes() []Mode`;
  - `Result{Item, Score, Mode, Highlights?}`;
  - `ErrModeUnsupported` for a mode the backend lacks.
- **Scoping:** by tenant (and user when `scope: user`) from `ctx`.
- **Keyword backends:**
  - in-memory BM25: tokenization with Unicode word boundaries, lowercased; exact phrase queries in quotes;
  - SQLite FTS5: needs T029's database; if T029 is not on `main`, ship in-memory and set `review`.
- **`memorytest.RunIndex`**: a conformance suite with invariants:
  - isolation;
  - upsert replaces;
  - delete removes from results;
  - `K` honored;
  - stable ordering on ties;
  - filters are exact.
- **Relevance set:** a small fixed set (20–30 items, 10 queries with expected top hits) that each backend must pass for the modes it supports.
- **Tools:** `recall_search(query, k?, filter?)`, `recall_get(id)`, `recall_save(text, metadata?)`.
- **`auto_recall`** (opt-in): uses the one harness hook (E12: "a per-run prepare option") to add the top-k results to the new user message as a marked block: `<recalled source="memory">…</recalled>`, labelled as data, not instructions. The system prompt stays unchanged.

## References

- [SQLite FTS5](https://www.sqlite.org/fts5.html) (`bm25()`, phrase queries, tokenizers)
- Robertson and Zaragoza, *The Probabilistic Relevance Framework: BM25 and Beyond* (2009) for BM25 parameters (k1, b)
- [Unicode word boundaries (UAX #29)](https://www.unicode.org/reports/tr29/) for tokenization

## Acceptance criteria

- [x] **Keyword backends:** the in-memory BM25 backend passes `RunIndex` and the keyword relevance set (exact phrases ranked first); SQLite FTS5 does too, when T029 is on `main`.
- [x] **Unsupported mode:** a semantic query to a keyword-only backend gives `ErrModeUnsupported`, never results.
- [x] **Isolation:** across tenants and users, for every backend (suite).
- [x] **`auto_recall` on:** the model's request carries the recalled block in the user turn and the system prompt is byte-identical to a run without recall (golden).
- [x] **`auto_recall` off:** no search call is made.
- [x] **Tools end to end** (fake model): save, search, then get.
- [x] **Fuzzing:** `FuzzQuery` (query syntax) never panics.
- [x] **Mutation list** recorded (at least: the tenant filter dropped in search, delete not removing from the index, recall put into the system prompt, `K` ignored).

## Notes

### What was built

- `pkg/memory`: `Index` (Put, Search, Get, Delete, Modes), `Item`, `Query`, `Result` (with `Highlights`: the query parts an item matched), `Mode` (`keyword`, `semantic`, `hybrid`), `IndexConfig` (scope, `MaxItems` 10,000, `MaxTextBytes` 32 KiB), `ErrModeUnsupported`, `ErrItemNotFound`, the shared checks every backend runs (`CheckItem`, `CheckID`, `ResolveQuery`, `MatchesFilter`), `Tokenize` and `ParseKeywordQuery`; `NewRecallTools` (`recall_search`, `recall_get`, `recall_save`) and `AutoRecall` (a harness prepare hook) with `RecalledBlock`.
- `pkg/memory/internal/keyword`: BM25 (k1 1.2, b 0.75, FTS5's non-negative IDF) ranking, filtering, ordering and cutting to K, the same for every backend.
- `pkg/memory/inmem.Index`: the reference (a map per scope, a scan per search). `pkg/memory/sqlite.Index` (`OpenIndex`): `memory_items` plus an FTS5 table of the words; FTS5 finds the scope's candidates and the per-part document counts, `keyword.Rank` scores them. It can share a file with the context store (its version is in `memory_index_meta`, not `user_version`).
- `pkg/memory/memorytest`: `RunIndex` (put/get/delete, invalid input, limits, isolation for tenant and user scope, upsert, delete, K, ties, exact filters, unsupported modes, cancel, concurrency; for keyword backends the syntax, the relevance set and a seeded simulation against BM25 over a model of three scopes), `CheckSearch` (the invariants of every search), `Relevance`/`RunRelevance` and `KeywordRelevance` (26 items, 10 queries), `SimulateKeyword` (also a fuzz target in `inmem`).
- `pkg/harness`: `WithPrepare(PrepareFunc)`, the one hook E12 allows: it runs once per run with a new message and puts its blocks before the message's own, in the user turn.
- `cmd/agentkit`: `memory.index` (`backend` `memory` or `sqlite`, `path`, `scope`, `max_items`, `max_text_bytes`, `auto_recall`, `k`) next to `memory.store`, the table `memoryIndexBackends` (SQLite behind `!nosqlite`), both optional (at least one); the recall tools join the memory tool ahead of the toolsets, and no toolset may have one of their names. Docs: QUICKSTART's `memory` row, COMPONENTS' seam table.

### Decisions

- **Query syntax.** Words (by `Tokenize`) match in any order; text in double quotes (also “ ”) is a phrase that must occur, its words adjacent and in order; a quote left open runs to the end. With no phrase an item must contain at least one word; with phrases, the other words only add to the score. That is what makes exact phrases rank first: an item without the phrase does not match at all. Repeats count once. No operators (`OR`, `-`, `*`), so no syntax error is possible beyond "no words" and more than 32 parts (`ErrInvalid`).
- **Tokenizer: UAX #29 word boundaries for the cases search needs, no dependency.** Letters, digits, marks and connectors form words; `' . : ·` (and their full-width forms) join letters, `. , ;` join digits ("don't", "example.com", "3.14", "1,000"); Han and Hiragana are a word per character (as UAX #29 has them, so "東京" is two words that both match); everything else separates. Lowercased with `strings.ToLower`; no stemming, stop words or Unicode normalization (NFC needs `golang.org/x/text`; precomposed and decomposed "é" are different words). Full UAX #29 (a dependency such as `rivo/uniseg`) can replace it behind the same function.
- **Ranking is computed in Go, the same for both backends.** FTS5's `bm25()` uses statistics over the whole table, which would let one tenant's items move another's scores (and leak how common a word is elsewhere). So SQLite uses FTS5 to find candidates and per-part counts within the scope, and `keyword.Rank` scores them over the scope's own statistics. Both backends give the same results, scores included (`TestIndexMatchesInMemory`, and the suite's simulation compares every search with BM25 over a model). A phrase counts as one BM25 term (its occurrences and the items containing it).
- **Words in FTS5 are hex-encoded** with the `ascii` tokenizer, so FTS5 splits exactly where `Tokenize` did, whatever the script or punctuation, and phrase queries are exact.
- **Order:** score, then the newest `Time`, then ID, for every mode (`CheckSearch` enforces it).
- **Limits:** K defaults to 5, at most 50; query 1 KiB; ID 128 bytes; metadata 16 pairs, keys 64 bytes, values 256; item time within what SQLite can hold in nanoseconds (1678 to 2261). A replacement never counts against `MaxItems`. Get and Delete of a missing ID are `ErrItemNotFound`.
- **Scope** goes through `memory.OwnerOf`, as the context store does (`ErrNoTenant`, `ErrNoUser`).
- **The prepare hook** gets a copy of the message as given and returns blocks; the harness puts them first, in the transcript, the record's prompt and `EventRunStart`, so a resumed or continued run sends the same requests and earlier turns never change. It does not run on a resume (no new message). An error fails the run before the first model call.
- **Auto recall** searches with the message's words (quotes dropped, the first 32 distinct words) in keyword mode, adds nothing when nothing is found or the message has no words, and fails the run when the search fails. The block is one text block: `<recalled source="memory">`, a line saying the items are data saved earlier and not instructions, then `<item id=… time=… score=…>` per result, with `& < > "` escaped so an item cannot close the block or forge an item. Recalled blocks are part of the stored conversation (that is what keeps resumes and the prompt cache exact).
- **Tools:** `recall_save` names items `m_` plus 16 random hex digits and dates them with the clock; outputs are JSON; invalid arguments are `tool.ErrInvalidInput`, a missing item or a full scope a tool failure. Metadata: search and get are reads, save a write, all risk low, tag `memory`.
- `k` in config sets both auto recall's count and `recall_search`'s default.

### Mutation check

Each bug was planted alone (a script applied it, ran the package's tests and restored the file); every one made a test fail.

| Planted bug | Failing tests |
|---|---|
| In-memory search ignores the tenant (all scopes searched) | `Isolation`, `Concurrent`, `KeywordSimulation`, `FuzzKeywordSimulation` seeds |
| SQLite candidates not filtered by tenant | `Isolation/tenant`, `Concurrent`, `KeywordSimulation` |
| SQLite statistics over every tenant | `Isolation/tenant`, `Isolation/user` (scores moved), `KeywordSimulation` |
| SQLite delete leaves the FTS row (a new item reuses the rowid) | `KeywordSimulation` |
| In-memory delete removes nothing | `DeleteRemovesFromResults`, `Isolation`, `FuzzKeywordSimulation` seeds |
| `K` ignored | `KHonored` via `CheckSearch`, `Concurrent`, `TestAutoRecallOn`, simulation |
| Phrases optional | `KeywordSyntax`, `KeywordRelevance` |
| Filters match a prefix | `FiltersExact` |
| Ties ignore the time | `StableTies` |
| An unsupported mode falls back to the default | `UnsupportedMode` |
| Upsert merges the old metadata | `KeywordSimulation`, `FuzzKeywordSimulation` seeds |
| SQLite upsert keeps the old words in FTS5 | `UpsertReplaces`, `KeywordSimulation` |
| SQLite without the item limit | `Limits`, `KeywordSimulation` |
| The apostrophe splits words | `TestTokenize` |
| Recall put into the system prompt (the harness appends it to `System`) | `TestAutoRecallOn` (system differs, golden) |
| The prepare hook runs on a resumed run | `TestPrepareRunsOnlyWithANewMessage` |
| The recalled block not escaped | `TestRecalledBlockEscapes`, `TestAutoRecallOn` |
| `auto_recall` always on in `cmd/agentkit` | `TestRecallThroughTheAPI` (the plain agent got a block) |
| `recall_save` drops the metadata | `TestRecallToolsEndToEnd`, `TestRecallThroughTheAPI` |

### Tests beyond the criteria

`TestIndexMatchesInMemory` (SQLite and in-memory agree on every relevance query and more), `TestIndexSharesAFileWithAStore`, `TestOpenIndexRefusesANewerSchema`, `TestRankIsBM25` (a score worked by hand), `TestPrepareErrorFailsTheRun`, `TestAutoRecallQuery`, `TestRecallToolErrors`, `TestRecallConfigErrors`, `TestMemoryIndexSQLiteNotBuilt` (`-tags nosqlite`), `ExampleIndex`. `FuzzQuery` also checks that every word re-tokenizes to itself and that no part is repeated; it ran 30 s locally with no finding.

### Sizes

No new dependency; FTS5 is in `modernc.org/sqlite` already. Binary +200,704 bytes, the variants +164 to +205 KB (rebased on main at `5f75d75`). Local (about 28.04 MB projected) crosses its 28 MB budget by about 42 KB, so its budget is now 29 MB; toolsets (about 17.93 of 18) is close (docs/SIZES.md row).

### For T060 (semantic and hybrid)

- Implement `memory.Index` with `Modes()` listing what the backend has, its default first; call `memory.ResolveQuery` first (it returns `ErrModeUnsupported` before anything else) and `memory.CheckItem` in Put.
- `memorytest.RunIndex` runs the keyword tests only for backends with `ModeKeyword`; its other tests use a natural-language query when the backend has no keyword mode. Add a `SemanticRelevance()` set next to `KeywordRelevance()` and run it in `RunIndex` for `ModeSemantic`.
- `CheckSearch` requires results ordered by `keyword.Compare` (score, newest, ID) whatever the mode, and `Result.Mode` equal to the query's mode: hybrid results say `hybrid` (add a field if they must say which modes found them).
- A hybrid backend can call `keyword.Rank` for its keyword list; the SQLite index's candidate and statistics queries are in `pkg/memory/sqlite/index.go`.
- `AutoRecall` sends the raw text (cut to 1 KiB) in modes other than keyword.
- Config: add `embedder` and `modes` to `memoryIndexConfig` in `cmd/agentkit/memory.go`.

### Follow-ups (not in this task)

- Auto recall adds whatever matches, even one common word ("the"); a minimum score, or stop words, may be wanted once real recall is used.
- The console shows a user message's blocks: it should fold the `<recalled>` block (it is stored with the message).
- No API or console panel lists or deletes a scope's items (E12's "memories" panel), as for the context store.
- A Postgres index (tsvector or `pg_trgm`) for the dev and cloud images; SQLite stays local-only. It must rank with `keyword.Rank` over its own scope statistics to pass the suite.
- The in-memory index scans its scope per search: fine to some tens of thousands of items, an inverted index if more.
- Not on a runner component, for the same reason as the context store (the tenant does not cross `pkg/rpc`).
