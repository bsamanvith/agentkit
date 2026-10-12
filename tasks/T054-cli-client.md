---
id: T054
title: "agentkit chat and agentkit ask: interactive and headless CLI clients"
epic: E11
status: todo
depends_on: [T053]
---

# T054: CLI client

## Context

Two ways to use a running agent from a terminal ([E11](E11-clients.md)): `chat` for a person, `ask` for scripts, our agentic tests and the evals (T050). Both are thin layers over `pkg/agentclient` (T053).

## Scope

- **`agentkit chat [--server URL] [--conversation ID]`**: a REPL. Streams the answer as it arrives; shows each tool call (name, short input) and its result (one line, truncated; `/details` shows the last one in full); prints usage after each turn. Commands: `/new` (new conversation), `/conv` (print the conversation ID), `/history` (the conversation so far), `/quit`. **Ctrl-C during a run cancels the run** (closing the stream), not the program; Ctrl-C at the prompt or Ctrl-D exits. Plain output when stdout is not a terminal (no colours, no cursor control).
- **`agentkit ask [--server URL] [--conversation ID] [--json] [message | -]`**: one run. Default output: the answer text only (tool calls to stderr with `-v`). `--json`: one event per line (JSONL), exactly the SSE `data` objects. Message from the argument or stdin (`-`). Exit codes: 0 `run_done`, 1 `run_error` (code printed to stderr), 2 usage or HTTP error, 3 `conversation_busy`/`over_capacity` (retryable; `--retry N` waits `Retry-After` and retries).
- Config: `--server` / `AGENTKIT_SERVER` (default `http://127.0.0.1:8080`), token from `AGENTKIT_API_TOKEN`; the token never printed (including `-v`).
- `scripts/try-agent.sh` and `docs/QUICKSTART.md` show `agentkit chat` and `agentkit ask --json` against the running server.

## Acceptance criteria

- [ ] `ask` against `agentkit serve` (replay e2e: the real binaries, replayed model): plain output is the answer; `--json` lines parse as events and end with `run_done`; exit codes 0/1/2/3 each produced by a real case.
- [ ] `ask --conversation` continues a conversation (the second answer depends on the first, scripted in the replay).
- [ ] `chat` driven through a pseudo-terminal: two turns, a tool call shown, `/new` starts a new conversation, Ctrl-C during a run cancels it on the server and returns to the prompt, Ctrl-D exits 0.
- [ ] Non-terminal stdout has no escape sequences.
- [ ] The token never appears in any output or log, including `-v` (canary).
- [ ] Live (`-tags live`): `agentkit ask --json` against `agentkit serve` with real Claude and the Tavily toolset (keyless) returns `run_done` with a `tool_call` to `tavily_search`.
- [ ] Mutation list recorded.
