---
id: T059
title: "Memory search store: interface, conformance suite, keyword backends, auto_recall"
epic: E12
status: todo
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

- [ ] **Keyword backends:** the in-memory BM25 backend passes `RunIndex` and the keyword relevance set (exact phrases ranked first); SQLite FTS5 does too, when T029 is on `main`.
- [ ] **Unsupported mode:** a semantic query to a keyword-only backend gives `ErrModeUnsupported`, never results.
- [ ] **Isolation:** across tenants and users, for every backend (suite).
- [ ] **`auto_recall` on:** the model's request carries the recalled block in the user turn and the system prompt is byte-identical to a run without recall (golden).
- [ ] **`auto_recall` off:** no search call is made.
- [ ] **Tools end to end** (fake model): save, search, then get.
- [ ] **Fuzzing:** `FuzzQuery` (query syntax) never panics.
- [ ] **Mutation list** recorded (at least: the tenant filter dropped in search, delete not removing from the index, recall put into the system prompt, `K` ignored).
