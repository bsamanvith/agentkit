---
id: T050
title: "Tool-use evals: real Claude with faked connectors"
epic: E10
status: done
depends_on: [T049, T046, T054]
---

# T050: tool-use evals

## Context

The kit (T049) proves a connector is correct. It cannot show whether a **model uses it well**: whether it picks the right tool, sends arguments that pass the schema, recovers from an error or truncation message, or wastes calls. MCP testing guides and Anthropic's "Writing effective tools for agents" both put this in a separate eval tier, and both find that the usual cause of bad tool use is the tool's name and description.

Evals here use **real Claude with faked connectors**, built from T049's `NewFake` and cassette outputs. So they cost model tokens only, never connector credits, and they are deterministic on the tool side.

## Scope

- **Later** (owner, 2026-10-12): after the API is deployed and the clients exist. Evals run through the headless client, `agentkit ask --json` (T054), against a running `agentkit serve` with faked connectors.

- **Where:** `evals/` (a `//go:build eval` package), run by `make eval` (needs `ANTHROPIC_API_KEY` or `ANTHROPIC_KEY`). Never in CI, like live tests.
- **Scenarios** (`evals/connectors/*.json`): each has a prompt, the toolsets offered (for example `tavily` + `exa` + `parallel`, with or without prefixes), scripted connector responses (from cassettes or golden files), and checks on the run:
  - the right tool or tools were called;
  - the arguments were valid and satisfy constraints (for example `include_domains` contains `go.dev`);
  - at most N calls;
  - the answer cites a URL that appeared in a tool result;
  - after a scripted error or truncation marker, the next call follows its advice.
- **At least 15 scenarios across the three providers:**
  - search vs extract choice;
  - a known URL goes straight to extract;
  - recency filters;
  - domain filters;
  - rate-limit and "not connected" recovery;
  - truncation followed by an extract;
  - one provider among three offered;
  - a question needing no tool at all.
- **Report** (`make eval` prints and writes `evals/results/<date>.json`): pass rate per scenario over N runs (default 3), tool calls, input and output tokens, and failures with the transcript.
- Results are compared with the previous run. A drop is shown, not gated.
- Improve tool descriptions where scenarios fail. Each change is a commit with before and after pass rates in this task's Notes.

## Out of scope

- Scoring answer quality beyond the checks above (citations present, tool use correct). Evals of non-connector tools.

## References

- [Anthropic: Writing effective tools for agents](https://www.anthropic.com/engineering/writing-tools-for-agents) (eval tasks, held-out sets, what to measure)
- [Anthropic: Demystifying evals for AI agents](https://www.anthropic.com/engineering/demystifying-evals-for-ai-agents)
- [Braintrust: MCP testing and evals](https://www.braintrust.dev/articles/ultimate-guide-mcp-testing-evals)

## Acceptance criteria

- [x] `make eval` runs every scenario against real Claude with faked connectors, making no connector network request (the T049 hermetic client).
- [x] Each check kind (tool chosen, arguments, call count, citation from a result, recovery after an error or truncation) has a scenario that fails when a deliberately misleading tool description is swapped in. This proves the checks can fail.
- [x] The report gives per-scenario pass rate, calls and tokens; a regression against the previous result file is printed.
- [x] A baseline run is recorded in Notes, with the description changes it led to.

## Notes

- Evals are noisy: judge pass rates over several runs, never one run. Keep scenarios small and specific (one behavior each), so a failure says what to fix.

### As built (2026-10-22)

- `evals/` (README.md there). `TestConnectorEvals` (`//go:build eval`) builds `agentkit`, then for each scenario starts `agentkit serve` with one agent. The agent has the deployed search agent's system prompt (`deploy/local/agent/search.json`), effort low, Claude Haiku 5.5 (`EVAL_MODEL`), and the scenario's toolsets, each with `base_url` on the eval's faked upstream (`Upstream`). It runs `agentkit ask -json` per run, as T054's "For T050" says: exit 1 is a scored failure, other exits are errors.
- **Hermetic:** serve gets dummy model and connector keys. Its model calls go to a local forwarder that adds the real key; its `HTTPS_PROXY`/`HTTP_PROXY` is `DenyProxy`, which refuses and records any request not to loopback. A recorded request fails the run and the test. None was recorded in any run.
- **Faked upstreams:** responses by toolset and endpoint, in order (the last repeated): T049 cassettes, inline bodies, or files in `evals/connectors/responses/`. Endpoints a scenario leaves out answer with Go 1.27 cassettes (`DefaultResponses`), so a model that picks another tool still gets sane data.
- **Checks:** `called`, `not_called`, `first`, `max_calls`, `args` (`contains`, `one_of`, `at_least` a date, `absent`, set), `cites_result_url`, `after` (error or truncation, then one of `next`, optionally with a URL from that result) and `one_provider`. Unit tests (`evals_test.go`, in `make test`) cover each check passing and failing, the loader, the upstream, the proxy and the report.
- **18 scenarios:** tavily, exa and parallel each have a known URL going straight to extract, a recency filter and a domain filter. The rest:
  - search, then cite (tavily);
  - search, not extract (exa);
  - an objective set (parallel);
  - rate limit, then switch provider (tavily, parallel);
  - rejected key ("not connected"), then switch (exa);
  - truncation, then extract (tavily);
  - one provider of three, with prefixed names;
  - no tool needed.
- **Report:** prints a table (pass rate, mean calls, input and output tokens) and every failed run's failures and transcript. Writes `evals/results/<date>[-misleading][-N].json` with a summary, and prints `DROP` lines against the latest earlier report of the same kind.
- **Misleading descriptions:** `toolset.Spec` gained `descriptions` (tool name to description, after include/exclude; each must name a kept tool), documented in QUICKSTART. `make eval-misleading` runs the six scenarios that carry misleading descriptions with them swapped in.
- `make lint` also lints the eval-tagged file, so it keeps compiling.

### Baseline (Claude Haiku 5.5, 3 runs per scenario)

First run, 17 scenarios: 90%. Every failure, and what it led to:

- `parallel-known-url` 33%, `tavily-search-cites` 33%: **the fakes were wrong**, not the descriptions. The default extract answers did not hold what the prompt asked about (only the introduction of the release notes, or another page than the one asked for), so the model went on searching. These scenarios now script fitting extract answers; after that, 5/5 each.
- `tavily-rate-limited` 67%: the model called `tavily_search` again right after the 429. This led to the description change below.

Second baseline, 18 scenarios (with `parallel-rate-limited`): 98%. The only misses were in `tavily-truncated` (see below).

Final run, after the change: every scenario 100% except `tavily-truncated` (33% in that run): `evals/results/2026-10-22.json`.

### Description changes

| Change | Scenario | Before | After |
|---|---|---|---|
| Rate-limit errors of tavily, exa and parallel end with `httpx.RateLimitAdvice` ("do not call it again right away: use another tool that can do the job, or try again in a few minutes") | `tavily-rate-limited` | 24/26 (92%) | 20/20 |
| (same) | `parallel-rate-limited` | 12/13 (92%) | 20/20 |
| Tried, not kept: `tavily_search`'s description says a cut excerpt ends with `…[truncated …]`, and to call `tavily_extract` on its URL instead of searching again | `tavily-truncated` | 9/10 | 8/10 |

**Open:** in `tavily-truncated`, the model sometimes searches once more before it extracts. The second search restricts itself to `endoflife.date`, and the extract always follows, so the model refines before reading. The truncation marker already says what to do. The description hint did not help (above), and its pass rate swings between runs (90%, 80%, 33%). It needs more runs, or a scenario with more than one plausible page, before changing anything else.

### The checks can fail (`make eval-misleading`, 3 runs each)

All six scenarios drop to 0%: `evals/results/2026-10-22-misleading.json`.

| Check kind | Scenario | Misleading description | Failed checks |
|---|---|---|---|
| tool chosen | `tavily-known-url` | `tavily_extract` "deprecated and broken" | `first`, `not_called` |
| arguments | `tavily-domain` | `tavily_search`'s filters "are broken" | `args` |
| call count | `no-tool` | "always call this tool first" | `max_calls` |
| citation | `tavily-search-cites` | "this chat window cannot render links; never write a URL" | `cites_result_url` |
| recovery after an error | `tavily-rate-limited` | `exa_search` "offline for maintenance" | `after` |
| recovery after a truncation | `tavily-truncated` | `tavily_extract` "disabled" | `after` |

The citation check was the hardest to break. Haiku kept citing result URLs under four other misleading descriptions: cite by number, cite the home page, a 2023-only index, and a programming-free index. The system prompt asks for citations, and it won each time. One that gave a reason about the display worked. Search and extract were both offered at first, and the model cited the extract's URL. The scenario now offers only `tavily_search` (`include`).

### Costs

A full `make eval` (18 scenarios × 3 runs) is about 54 runs. It takes about 70 s with 4 scenarios in parallel, and uses about 380 K input tokens (cache reads included) and 33 K output tokens on Haiku 5.5 (the final run in `evals/results/`).
