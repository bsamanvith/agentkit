---
id: T112
title: "pkg/knowledge: knowledge bases as a toolset (ingest, chunk, retrieve), with a sample and evals"
epic: E12
status: done
depends_on: [T059, T060, T026]
---

# T112: knowledge bases

## Context

Owner request (2026-10-22): "Can we add some knowledge base and test again." Agents should answer from a body of documents they were given (retrieval-augmented generation), not only from the model and the web. E12 built the search store (`memory.Index`: keyword, semantic and hybrid search, T059 and T060); this task turns a folder of documents into a searchable base and gives it to agents as tools, then tests it end to end with a seeded sample.

## Scope

- **`pkg/knowledge`**: `Load` (a folder's `.md`, `.markdown` and `.txt` files; hidden names and symlinks skipped; size and UTF-8 checked), `Split` (passages along Markdown headings, outside code fences, at most a chunk size, cut at paragraph, line, space or character boundaries in that order, each with its heading path), `Base` (`New` indexes the passages in `memory/inmem`, with an optional embedder; `Search`, `Documents`, `Document`).
- **The `knowledge` toolset type** (`Factory`): settings `root`, `description`, `k`, `chunk_bytes`, `embedder`, `modes`; tools `knowledge_search`, `knowledge_list`, `knowledge_read` (all read, low risk). A relative root is relative to the config file's folder (`WithBaseDir`, passed by serve).
- **Scope:** a base is the deployment's, like a system prompt or skills: every caller searches the same passages whatever its tenant (the index is kept under a fixed internal tenant). Per-tenant documents added at run time stay the search store's (`recall_save`).
- **Sample and deployments:** the Northwind Bikes support handbook (`deploy/local/agent/knowledge`, invented facts) and an agent `support` in `deploy/local/agent/search.json` and `deploy/k8s/base/split.yaml`. The toolsets pod mounts the ConfigMap `agentkit-knowledge`, which `scripts/k8s-local.sh` makes from the sample; `scripts/smoke.sh` copies it next to the config; `scripts/try-agent.sh --docker` mounts it. Compose (T113, `deploy/compose/compose.yaml`) bind-mounts the same folder on its toolsets service, and the cluster's config is now `deploy/k8s/base/config.json`, shared by all three.
- **Evals:** scenarios may offer the `knowledge` toolset and run the `support` agent's prompt (read from `search.json`); new checks `answer_contains` and `cites_source`.

## Out of scope

- Documents added through the API or the console, and per-tenant bases (a follow-up on the search store, with T096's runtime toolsets as the model).
- Reloading a base when its folder changes; persisting embeddings across restarts (a base with an embedder embeds every passage at startup).
- Formats other than text (PDF, HTML, Office); auto-retrieval of passages into the user turn (the harness's prepare hook can take it later, as `auto_recall` does).

## Acceptance criteria

- [x] **Chunking:** passages follow headings (not inside code fences, not `#tag`), a heading with no text joins the next section, long sections are cut at the best break within the size, and together the passages are the document with only white space lost. (`TestSplit`, `TestSplitCutsAWordAtACharacterBoundary`, `TestSplitInvariantsOnTheHandbook`, `FuzzSplit`)
- [x] **Loading:** only the document extensions, no hidden names, no empty files, no symlinks out of the root; a file over 1 MiB or not UTF-8 fails with the file named. (`TestLoad`, `TestLoadRefuses`, `TestLoadTitleSkipsCode`, `TestToolsetStaysInItsRoot`)
- [x] **Retrieval:** keyword search ranks the passage that answers first for a fixed relevance set, finds a passage by its title and headings, and is the same for every tenant and user; with an embedder, every passage is embedded as a document, the query as a query, and a paraphrase finds its passage. (`TestSearchByKeyword`, `TestSearchFindsTheSectionsWords`, `TestSearchIsTheSameForEveryCaller`, `TestSemanticSearch`, `TestNewFailsWhenTheEmbedderDoes`)
- [x] **Tools:** search with the toolset's and the model's k, list with a prefix, read in parts at line breaks; bad input is `tool.ErrInvalidInput`; bad settings fail the build. (`TestToolsetTools`, `TestSearchTool`, `TestListTool`, `TestReadTool`, `TestReadToolRefusesAnOffsetInsideACharacter`, `TestToolsetSettings`)
- [x] **Through serve** (fake model): an agent with a knowledge toolset whose root is relative to the config searches it through the agent API; the sample handbook answers the smoke and eval questions. (`TestKnowledgeAgent`, `TestSampleKnowledgeBase`, `TestSearchConfigBuilds`)
- [x] **Split deployment** (`make smoke`, fake model): the support agent on the runner calls `knowledge_search` on the toolsets component and answers with the handbook's 12%. (`test/e2e` `TestKnowledge`; `make k8s-smoke` runs it on real Claude)
- [x] **Live** (`make eval EVAL_SCENARIOS='^knowledge-'`, Claude Haiku 5.5, 3 runs each): 100% on all three scenarios (`results/2026-10-22-2.json`). With the misleading descriptions (`-misleading-2.json`): error code 0%, restocking fee 67%, cites source 100%; an earlier misleading run had cites source at 33% and restocking fee at 0%. The support agent's system prompt says to search and cite, and it mostly wins over a tool description that says not to.
- [x] **Mutation list** recorded (below).

## Notes

### Decisions

- **A toolset, not an agent section.** A base is static, tenant-independent data, so it is an ordinary toolset: it runs where toolsets run (in process, the toolsets component, `/mcp/<name>`), per-key toolset auth applies, agents opt in by listing it, and `include`/`exclude` and `descriptions` work. An agent section (like `memory`) would have tied it to the agent component, which the split deployment's runner cannot use (T058's note).
- **The search store's in-memory index**, not a new one: BM25 and the embedders come with it, and the same relevance rules hold. Its tenant scope is satisfied with a fixed tenant, `knowledge`, so the caller's tenant never changes what is found.
- **Passages keep their heading line**, and the index also gets the title and section path in front of the text, so a long section's later passages are found by the section's words.
- **Relative roots are relative to the config file's folder**, as skills are (and unlike `files`, whose root is the working directory's): a config ships with its documents next to it.
- **Only source file names count as a citation** in evals' `cites_source`: titles and stems ("warranty") are words an answer has anyway. The support prompt asks for the file name.
- **check-deps:** the toolsets image may now link `pkg/memory`, `memory/inmem` and `memory/embed` (never `memory/sqlite`).

### Mutation check

Each bug was planted alone and the package's tests run, then restored.

| Planted bug | Failing tests |
|---|---|
| Headings inside code fences count | `TestSplit` (code fence case) |
| `#tag` is a heading | `TestSplit` (not headings case) |
| A heading with no text is a passage of its own | `TestSplit` (joins case) |
| No paragraph breaks (line breaks only) | `TestSplit` (paragraph break case) |
| No floor on breaks (a break at byte 2 is used) | `TestSplit` (first quarter case) |
| Cut inside a character | `FuzzSplit` seed |
| Hidden files loaded | `TestLoad`, `TestListTool`, `TestDocuments` |
| Symlinks followed | `TestToolsetStaysInItsRoot` |
| Title and headings not indexed | `TestSearchFindsTheSectionsWords` |
| The caller's tenant used for the index | `TestSearchIsTheSameForEveryCaller` and most search tests |
| Duplicate sources allowed | `TestNewRefuses` |
| The toolset's k ignored | `TestSearchTool` |
| Read parts not cut at line breaks | `TestReadTool` |
| An offset inside a character accepted | `TestReadToolRefusesAnOffsetInsideACharacter` |
| A bad query not invalid input | `TestSearchTool` |
| The list prefix ignored | `TestListTool` |
| The config folder ignored for a relative root | `TestKnowledgeAgent`, `TestSampleKnowledgeBase`, `TestSearchConfigBuilds` |
| The description left out of the tools | `TestToolsetTools` |

### Sizes

agentkit-toolsets +290,816 bytes (`pkg/memory` and its embedding clients, new to that image), the agent variants +123 to +131 KB, api and runner unchanged (docs/SIZES.md has the rows). No new module.
