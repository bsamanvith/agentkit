---
id: T060
title: "Embedders and semantic and hybrid memory search"
epic: E12
status: review
depends_on: [T059]
---

# T060: semantic and hybrid search

## Context

Search by meaning, not only by words: "my flight preferences" should find "I always take an aisle seat". Decisions: [E12](E12-skills-memory.md) (Embedder interface, vector index, hybrid by reciprocal rank fusion).

## Scope

- **`memory.Embedder`**: `Embed(ctx, texts []string, purpose Purpose) ([][]float32, error)` and `Model() (name string, dim int)`.
  - Implementations: an **OpenAI-compatible** embeddings endpoint (base URL, model, key from an env var named in settings); **Voyage AI**; and `memorytest.FakeEmbedder`. The fake is deterministic and is built so that listed paraphrase pairs are close and unrelated texts are far, for tests.
  - Batching, retries on 429 as the connectors do, and a body limit.
  - These are outbound HTTP clients like connectors: reuse T049's hermetic client and cassette pieces where they fit.
- **Vector index:**
  - in-memory brute-force cosine (the reference);
  - SQLite (vectors as blobs, brute force within a tenant scope) when T029 is on `main`.
  - Each stored vector carries its embedder's model and dimension; a mismatch is `ErrEmbedderMismatch`.
  - Re-embedding is an explicit command, never implicit.
- **Hybrid mode:** reciprocal rank fusion of keyword and semantic results, k = 60; results say which modes found them.
- **Config:** `agent.memory.index: {backend, embedder: {type, model, ...}, modes}`.

## References

- [Voyage AI embeddings](https://docs.voyageai.com/docs/embeddings) (input_type query vs document)
- [OpenAI embeddings API](https://platform.openai.com/docs/api-reference/embeddings) (the OpenAI-compatible shape local servers copy)
- Cormack, Clarke and Büttcher, *Reciprocal Rank Fusion outperforms Condorcet and individual rank learning methods* (SIGIR 2009), k = 60
- [pgvector](https://github.com/pgvector/pgvector) and [sqlite-vec](https://github.com/asg017/sqlite-vec) for the later backends

## Acceptance criteria

- [x] **Suites:** the semantic and hybrid backends pass `memorytest.RunIndex`. On the relevance set, semantic finds the paraphrase cases (with `FakeEmbedder`), keyword still wins exact phrases, and hybrid passes both.
- [x] **Model mismatch:** an index written with embedder A and searched with embedder B (another model or dimension) gives `ErrEmbedderMismatch`.
- [x] **Embedder clients:** both pass a shared embedder conformance check against cassettes: batching, the 429 retry, the body limit, and the key never leaking (canary).
- [ ] **Live (`-tags live`, with the owner's chosen provider's key):** real embeddings rank a paraphrase above an unrelated item. If no key is set, the task stays `review`.
- [x] **Speed:** a 10k-item in-memory search finishes under 50 ms on a 4-core machine, measured by a `Benchmark` and recorded in Notes, not asserted.
- [x] **Mutation list** recorded (at least: cosine without normalization, fusion constant ignored, model check skipped).

## Notes

Status `review`: every criterion ran here except **Live**, which needs the owner's embedding key (E12 owner question 1: Voyage, OpenAI or a local model). `TestLiveParaphrase` (`pkg/memory/embed/embed_live_test.go`, `-tags live`) runs whichever of `VOYAGE_API_KEY` and `OPENAI_API_KEY` is set and skips with none. The embedding cassettes are **synthetic** (`"synthetic": true`, made from each API's documented shape), for the same reason: record them once a key exists.

### What was built

- `pkg/memory`: `Embedder` (`Embed(ctx, texts, purpose)`, `Model() (name, dim)`), `Purpose` (`query`, `document`), `ErrEmbedderMismatch`, `Reembedder`, `HybridKeywordText`, `RRFK` (60) and `HybridDepth` (`MaxK`); `IndexConfig.Embedder` and `IndexConfig.Modes`; `Result.Found` (the modes whose rankings had the item).
- `pkg/memory/internal/semantic`: `Embed` (calls the embedder, checks count, dimension and finiteness, normalizes), `Rank` (cosine over the scope, the model check on every vector of the scope), `Fuse` (RRF), the blob encoding.
- `pkg/memory/inmem.Index` and `pkg/memory/sqlite.Index` take an embedder: every Put embeds (after the limit check, so no paid call for an item that cannot be stored); semantic and hybrid searches. SQLite: index schema 2 adds `memory_vectors (seq, model, dim, vec)`; a version 1 file is upgraded on open (its items have no vector until re-embedded). `sqlite.Index.Reembed`.
- `pkg/memory/embed`: `NewOpenAI` (OpenAI and compatible servers) and `NewVoyage`: batching by count and bytes, one retry of a 429 after Retry-After on the injected clock, a body limit, the key only in `Authorization` and redacted from everything else, error classes (`ErrRejected`, `ErrUnauthorized`, `ErrRateLimited`, `ErrUnavailable`, `ErrBadResponse`), vectors put back in input order by `index`. `FuzzResponse`.
- `pkg/memory/embed/embedtest`: `Run`, the shared conformance check of embedding clients on `toolsettest.Upstream` (cassettes, hermetic client): model without a request, batches from exactly their cassettes, the key header, the 429 retry (and once only, cancel during the wait, a Retry-After over 10 s), no retry of 4xx/5xx, the body limit (bytes read counted), the canary in errors and logs, malformed responses, a hanging upstream.
- `pkg/memory/memorytest`: `RunIndex` now runs three times (as given; `Semantic` and `Hybrid` with a `FakeEmbedder` and those modes put in the config); `CheckSearch` checks `Found`, semantic scores in [-1, 1], and recomputes every hybrid search from keyword and semantic searches with its own RRF; semantic tests (scores, embedder calls, failures, lying embedders, upsert); `SimulateIndex` (keyword vs BM25, semantic vs cosine of the fake's vectors, hybrid via CheckSearch); `RunEmbedderChange` for persistent backends; `FakeEmbedder`, `FakeVector`, `FakeConcepts`, `FakeParaphrases`; `SemanticRelevance` (12 notes, 11 paraphrase queries sharing no word with their note) and `HybridRelevance` (both sets' items and queries).
- `cmd/agentkit`: `memory.index.embedder` (`type` `openai` or `voyage`, `model`, `dimensions`, `base_url`, `api_key_env`, `batch_size`) and `memory.index.modes`; `agentkit memory reembed [-config file] [-agent name]`. Both behind `!noagent` (`memory_embed.go`, `memory_reembed.go`, `memory_reembed_none.go`). Docs: QUICKSTART's `memory` row, COMPONENTS' seam table (an Embedder row), docs/SIZES.md.

### Decisions

- **Modes come from the config**, with the embedder: no embedder is `[keyword]`; with one the default is `[hybrid, keyword, semantic]` (hybrid passes both relevance sets, so it is the best default). Semantic or hybrid without an embedder is `ErrInvalid` at startup.
- **Every Put embeds when there is an embedder**, whatever the modes, so the vectors stay current if the modes change. A Put without one removes the item's old vector (it was of the old text).
- **Mismatch is per scope, at search time.** A semantic or hybrid search of a scope holding any vector of another model or dimension, or an item with no vector, is `ErrEmbedderMismatch` (naming the item and both models). Keyword search, Get, Put and Delete go on. Put does not refuse: it writes the current model's vector, so the scope converges as items are rewritten, and `Reembed` finishes it. Nothing re-embeds implicitly.
- **Reembed** walks every scope of the file in seq order, 64 texts per call, and writes a vector only if the item's text is unchanged (a Put meanwhile wrote its own). Safe while serving and to rerun.
- **Semantic ranks every item of the scope** (and filter): no similarity threshold, so auto recall in semantic or hybrid mode always adds the top k (see follow-ups). Scores are cosines of normalized float32 vectors; ties go newest first, then by ID, as in keyword mode.
- **Hybrid:** the top `HybridDepth` (= `MaxK`, 50) of each ranking fuse by RRF with k = 60; tied scores share the rank of the first of them (so equal items stay equal); the keyword half searches `HybridKeywordText` (the text, or its first 32 distinct words when it is too long a keyword query, so auto recall's raw message text still gets keyword matches). Highlights come from the keyword half. Hybrid results say `Mode: hybrid` and `Found: [keyword]`, `[semantic]` or both.
- **The fake embedder** hashes each word, or its concept from `FakeConcepts`, into one of 256 dimensions with a sign, stop words dropped, counts not normalized (so an index that forgets to normalize ranks long texts first, and `travel-log` in the relevance set catches it).
- **Embedding clients:** `Dimensions` may be 0 only for a model in `KnownModels`; it is sent to the provider only for a known model whose default it is not (OpenAI `dimensions`, Voyage `output_dimension`), never for an unknown (local) model. OpenAI-compatible servers may run keyless (no `Authorization`); Voyage requires a key. Defaults: 64 texts and 512 KiB per request, 16 MiB response cap, 30 s per request. `base_url` must be https unless loopback.
- **`connectors/internal/httpx` is not importable from `pkg/memory`** (Go's internal rule), so `pkg/memory/embed` has its own 40 lines of the same rules (`retryAfter`, `sleep`, the capped read). The tests reuse T049's `toolsettest.Upstream` (allowlisted for the `pkg/memory` tree in `internal/archtest`).
- **No vector extension:** vectors are blobs compared in Go (brute force within the scope), so no cgo and nothing new in any image.

### Speed (not asserted)

`BenchmarkSearch10k` (`pkg/memory/inmem`): 10,000 items in one scope, 1024-dimension vectors, K = 10, on this 4-core Intel Xeon @ 2.80 GHz (shared with other sessions, load 3 to 12): **semantic 14.6 to 14.8 ms per search** (best of three runs of 30; 44 ms in the most loaded run), 1.3 MB and 30 allocations; keyword 23.6 to 24.4 ms (T059's scan, which tokenizes nothing per search but walks every item's words); hybrid 40 to 51 ms (both, plus the fusion). The semantic cost is the dot products (about 45% of CPU in the profile: 40 MB of vectors read per search) and the sort of the scope's scores (about 14%).

### Mutation check

Each bug planted alone by a script (applied, the packages' tests run, restored); each made a test fail.

| Planted bug | Failing tests |
|---|---|
| Cosine without normalization (`Normalize` does not scale) | `SemanticScores`, `SemanticRelevance` (travel-log first), `HybridRelevance`, `SemanticUpsert`, `EmbedderCalls`, `EmbedderFails` |
| Fusion constant ignored (1/rank) | `TestFuseIsRRF`, every hybrid search through `CheckSearch`'s fusion check (`FiltersExact`, `Isolation`, `DeleteRemovesFromResults`, `HybridRelevance`) |
| Model check skipped in `Rank` | `TestIndexEmbedderChange` (another dimension panics, another model with the same dimension is compared), `TestIndexUpgradesVersion1` |
| Model name not compared (dimension only) | `TestIndexEmbedderChange` |
| Tied scores do not share a rank | `TestFuseIsRRF`, `Hybrid/FiltersExact`, `HybridRelevance`, `Hybrid/Simulation` |
| Semantic ranking ignores the filter | `FiltersExact`, `Simulation` |
| A vector of another dimension than `Model` accepted | `EmbedderMismatch/short`, `/long` |
| Hybrid without its keyword half | `HybridRelevance`, hybrid fusion checks |
| In-memory Put embeds before the limit check | `EmbedderCalls` |
| Query embedded as a document | `EmbedderCalls` |
| SQLite Put without an embedder keeps the old vector | `TestIndexEmbedderChange` |
| SQLite Reembed only fills missing vectors | `TestIndexEmbedderChange` |
| SQLite semantic search not scoped by tenant | `Isolation`, `Concurrent`, `Simulation` |
| Client retries a 5xx | `TestErrorClasses`, `TestDimensionsSent` |
| Key not redacted | `Canary` (both clients), `TestErrorClasses` |
| No body limit | `BodyLimit` (both) |
| Batch size ignored | `Batches`, `Retry/then200` (both) |
| Retry without waiting on the clock | `Retry/*` (both) |
| Voyage always sends `input_type: document` | `TestVoyageConformance/Batches` (the query cassette) |
| `cmd/agentkit` drops the configured embedder | `TestSemanticRecallThroughTheAPI` |

### Tests beyond the criteria

`TestIndexMatchesInMemory` (now every mode, scores included), `TestIndexUpgradesVersion1`, `TestModesComplement` (keyword finds none of the paraphrases; semantic and hybrid cannot exclude what lacks a phrase, keyword does), `TestFakeEmbedder`, `TestVectorsInInputOrder`, `TestBatchBytes`, `TestDimensionsSent`, `TestConfigErrors`, `TestErrorClasses`, `FuzzResponse` (20 s locally, no finding), `TestSemanticRecallThroughTheAPI` (an agent finds a note by meaning through the API, then `memory reembed`), `TestSemanticConfigErrors`, `TestMemoryReembedUsage`, `ExampleNewOpenAI`.

### For T0xx (whoever builds on this)

- A Postgres index (pgvector) passes `RunIndex` (all three runs) and `RunEmbedderChange`, ranks with `semantic.Rank`/`Fuse` or proves the same order (the suite recomputes every hybrid search), and stores model and dimension per vector.
- `memory.Reembedder` is what `agentkit memory reembed` needs; an index without it is reported as "keeps nothing to re-embed".
- New embedding API: a constructor passing `embedtest.Run` on its cassettes, and its `type` in `newEmbedder` (`cmd/agentkit/memory_embed.go`).

### Follow-ups (not in this task)

- Semantic auto recall adds the top k whatever their similarity: a minimum score (per model, cosines differ by provider) is wanted before semantic auto recall is used in earnest.
- Record the embedding cassettes (`make record-cassettes`) once a key exists; until then they are synthetic.
- `connectors/internal/httpx` could move to a shared `internal/httpx` so `pkg/memory/embed` reuses it instead of its copy.
- Keyword search over 10k items (24 ms) is now the slow half of hybrid: an inverted index for the in-memory backend if scopes grow.
- SQLite semantic search reads every vector of the scope per search (40 MB for 10k × 1024); a per-scope cache, or sqlite-vec with cgo, if that becomes the cost.
