---
id: T050
title: "Tool-use evals: real Claude with faked connectors"
epic: E10
status: todo
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

- [ ] `make eval` runs every scenario against real Claude with faked connectors, making no connector network request (the T049 hermetic client).
- [ ] Each check kind (tool chosen, arguments, call count, citation from a result, recovery after an error or truncation) has a scenario that fails when a deliberately misleading tool description is swapped in. This proves the checks can fail.
- [ ] The report gives per-scenario pass rate, calls and tokens; a regression against the previous result file is printed.
- [ ] A baseline run is recorded in Notes, with the description changes it led to.

## Notes

- Evals are noisy: judge pass rates over several runs, never one run. Keep scenarios small and specific (one behavior each), so a failure says what to fix.
