---
id: T060
title: "Embedders and semantic and hybrid memory search"
epic: E12
status: todo
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

- [ ] **Suites:** the semantic and hybrid backends pass `memorytest.RunIndex`. On the relevance set, semantic finds the paraphrase cases (with `FakeEmbedder`), keyword still wins exact phrases, and hybrid passes both.
- [ ] **Model mismatch:** an index written with embedder A and searched with embedder B (another model or dimension) gives `ErrEmbedderMismatch`.
- [ ] **Embedder clients:** both pass a shared embedder conformance check against cassettes: batching, the 429 retry, the body limit, and the key never leaking (canary).
- [ ] **Live (`-tags live`, with the owner's chosen provider's key):** real embeddings rank a paraphrase above an unrelated item. If no key is set, the task stays `review`.
- [ ] **Speed:** a 10k-item in-memory search finishes under 50 ms on a 4-core machine, measured by a `Benchmark` and recorded in Notes, not asserted.
- [ ] **Mutation list** recorded (at least: cosine without normalization, fusion constant ignored, model check skipped).
