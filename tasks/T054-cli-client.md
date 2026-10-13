---
id: T054
title: "agentkit chat and agentkit ask: interactive and headless CLI clients"
epic: E11
status: done
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

## From T053's handoff (2026-10-12)

- **`ask --json`:** print `Stream.RawData()` for each event, so unknown fields survive.
- **Exit codes:** 0 on `run_done`, 1 on `run_error`, 2 on any client error. A failed run is not a Go error: the stream ends with `run_error`, then `io.EOF`.
- **Ctrl-C:** calls `Stream.Close()`, which cancels the run. If the run stopped before answering, the next message gets `ErrConversationInterrupted`; offer to resume with an empty message.
- **Retries:** retry `ErrOverCapacity` and `ErrShuttingDown` after `APIError.RetryAfter`.
- **No default timeout:** use the context, because an HTTP client `Timeout` also cuts streams.
- **Unknown message blocks** (`llm.Unknown`, from lenient decoding): show them as a placeholder such as `[image]`. `Message.Text()` already skips them.

## Acceptance criteria

- [x] `ask` against `agentkit serve` (replay e2e: the real binaries, replayed model): plain output is the answer; `--json` lines parse as events and end with `run_done`; exit codes 0/1/2/3 each produced by a real case. **`TestE2EAsk`** (`cli_e2e_test.go`): 0 plain and `-json`; 1 a `model_rejected` run; 2 wrong token, no token, unknown conversation, unknown agent, server down, no message, two messages, bad flag (and no model call for any of them); 3 a second ask on a conversation whose run is held. `-retry` (with `Retry-After`) is **`TestAskRetriesOverCapacity`** (`client_test.go`).
- [x] `ask --conversation` continues a conversation (the second answer depends on the first, scripted in the replay). **`TestE2EAsk`**: the second ask (`-json -conversation ID -`, the message on stdin) answers 469821644 = the first answer + 1, in the same conversation, and the model's request holds the first run's answer (`checkConversation`).
- [x] `chat` driven through a pseudo-terminal: two turns, a tool call shown, `/new` starts a new conversation, Ctrl-C during a run cancels it on the server and returns to the prompt, Ctrl-D exits 0. **`TestE2EChat`** (pty; plus Enter on an empty line resumes the cancelled run, and each conversation's state on the server is checked). In-process, with the real server: **`TestChatCtrlCCancelsRun`** (chat waits until the server has ended the run before it resumes), **`TestChatCtrlCAsTheRunStarts`** (Ctrl-C while the run's headers, or its first event, are held on the way: chat still says it can resume, and does), **`TestChatCtrlCAtPromptQuits`**, **`TestChatCommands`** (`/details`, `/history`, `/conv`, an unknown command, an unknown block shown as `[image]`).
- [x] Non-terminal stdout has no escape sequences. `checkCLIOutput` in **`TestE2EAsk`** (every ask, and the real `chat` binary with stdout on a pipe and `TERM=xterm`: prompt, `/history` tool lines, an error) and `checkClientOutput` in **`TestChatCommands`**, **`TestChatCtrlCCancelsRun`**, **`TestAskRetriesOverCapacity`** (chat with a pipe for stdout); **`TestE2EChat`** checks the opposite (a terminal gets `\x1b[`).
- [x] The token never appears in any output or log, including `-v` (canary). **`TestE2EAsk`** (every case, `-v` included, and the server's log via `startAgentServe`), **`TestE2EChat`** (the transcript and the server's log), the in-process tests (`testToken`), **`TestLiveAskSearch`**.
- [ ] Live (`-tags live`): `agentkit ask --json` against `agentkit serve` with real Claude and the Tavily toolset (keyless) returns `run_done` with a `tool_call` to `tavily_search`. **`TestLiveAskSearch`** (`cli_live_test.go`) is written and vetted with `-tags live`, **but not run: this session had no `ANTHROPIC_KEY`**. Left to the planner: `make test-live`.
- [x] Mutation list recorded (Notes).

## Notes

### Files

- **`cmd/agentkit/client.go`** (new): what chat and ask share. Flags (`-server`, `-agent`, `-conversation`), `$AGENTKIT_SERVER`, the client (token from `$AGENTKIT_API_TOKEN`, User-Agent `agentkit-cli/<version>`), `style` (escape sequences only for a terminal, never with `$NO_COLOR` or `TERM=dumb`), `renderer` (a run's events as text), `details`, `history`.
- **`cmd/agentkit/ask.go`**, **`cmd/agentkit/chat.go`** (new): the subcommands.
- **`cmd/agentkit/main.go`**: the two dispatch lines, usage, package doc, a `term clientTerm` field on `cli` (stdin, Ctrl-C and the retry wait, replaced in tests), and SIGINT left out of `signal.NotifyContext` for `chat` (chat takes Ctrl-C itself).
- Tests: `cli_e2e_test.go` (replay e2e, the real binaries), `client_test.go` (in-process: the real `agentapi` server, a fake model), `cli_live_test.go` (`live`), `pty_test.go` + `pty_darwin_test.go` / `pty_linux_test.go` / `pty_other_test.go` (a pseudo-terminal for tests). Fixtures `testdata/e2e/cli-ask/`, `testdata/e2e/cli-chat/`.
- `docs/QUICKSTART.md` (§3, "From the terminal"), `scripts/try-agent.sh` (step 6 runs `ask -json` and `ask`; the summary shows `chat`).
- No go.mod change; no new dependency.

### Usage

```sh
export AGENTKIT_SERVER=http://127.0.0.1:8080 AGENTKIT_API_TOKEN=...
agentkit chat                                   # /help lists the commands
agentkit chat -agent search -conversation conv_…
agentkit ask "What is 48271 times 9733?"         # the answer only
agentkit ask -v "…"                              # + conversation, tool calls and usage on stderr
echo "Add one." | agentkit ask -conversation conv_… -
agentkit ask -json -agent search "…" | jq -c 'select(.type=="tool_call")'
agentkit ask -retry 5 "…"                        # waits Retry-After on busy/over capacity
```

### Decisions

- **Exit codes**: 0 `run_done`, 1 `run_error` (`run failed: <code>: <message>` on stderr), 2 usage, HTTP or stream error, 3 `conversation_busy`, `over_capacity` **and `shutting_down`** (all three are "try again later"), 130 Ctrl-C/SIGTERM (the run is cancelled on the server; the shell convention). A refused run on an interrupted conversation exits 2 with the command that resumes it.
- **`-retry N`** retries the three retryable refusals, waiting `Retry-After` (the server sends 2 s on `over_capacity`) or 1 s when there is none (a busy conversation has none). It never retries a started run.
- **Plain output is `run_done.answer`**, not the text deltas: the text before a tool call ("I will use the calculator.") is not the answer. `-v` shows the conversation and run IDs, tool calls and results, and usage on stderr, never the text.
- **`-json`** writes `Stream.RawData()` and a newline, one write per event, as each arrives (unknown fields kept).
- **`--agent`** was added to both (T062 landed first): it applies to a new conversation.
- **Chat's Ctrl-C** cancels the run's context, which is what `Stream.Close()` does (the server cancels the run). Chat then polls `GET /v1/conversations/{id}` (every 50 ms, at most 10 s) until it is no longer `running`, so the next message is not refused as busy; if the run stopped before answering (`interrupted`), it says so, and Enter on an empty line resumes it (an empty message). Ctrl-C at the prompt exits 130, Ctrl-D exits 0. A Ctrl-C just after Enter waits (up to 2 s) for the run's stream to open before cancelling, and chat takes the conversation from the response headers, so it can check and resume a run cancelled before its first event. Without that, under load, the Ctrl-C could abort the request or land before `run_start`, and chat said only "cancelled" (the `TestE2EChat` flake, fixed 2026-10-13).
- **Styling**: only when stdout is a character device (`os.File.Stat`), so no terminal library is needed; `NO_COLOR` and `TERM=dumb` turn it off.
- **Tool calls** show on one line each (`→ name input`, `← result` or `✗ error`, cut to 100/120 runes); `/details` shows the last call's input (indented) and its result in full. Unknown message blocks show as `[type]` in `/history`.

### The pseudo-terminal: stdlib only

`TestE2EChat` runs the real binary on a pseudo-terminal opened with `syscall` in test files (darwin: `/dev/ptmx`, `TIOCPTYGRANT`/`TIOCPTYUNLK`/`TIOCPTYGNAME`; Linux: `TIOCSPTLCK`/`TIOCGPTN`, `/dev/pts/N`; other systems skip). The child gets `Setsid`, `Setctty`, so the terminal's line discipline turns `\x03` into SIGINT and `\x04` into end of input, as for a person. `creack/pty` would have meant a go.mod change and an archtest rule, both outside this task's files, for about 60 lines. Chat's own logic is also tested in-process with injected stdin and Ctrl-C (`clientTerm`). Verified on darwin and on Linux (`golang:1.27.2` in Docker; `TestE2EChat` ran, not skipped).

### Fixtures

`testdata/e2e/cli-ask/` and `cli-chat/` are hand-written Anthropic responses (this session had no key), in the replay format. Re-record them with a key if the request format changes; the tests assert answers, not wording.

### Mutation list

Each mutant was planted by hand, the four test functions run (`TestE2EAsk|TestE2EChat|TestChat|TestAsk`), and the source restored. All caught:

| Mutant | Caught by |
|---|---|
| `run_error` exits 0 | TestE2EAsk |
| busy exits 2 instead of 3 | TestE2EAsk |
| everything retryable | TestE2EAsk |
| `-retry` ignores `Retry-After` | TestAskRetriesOverCapacity |
| `-json` skips `text_delta` | TestE2EAsk |
| plain prints every text delta | TestE2EAsk, TestAskRetriesOverCapacity |
| `-v` prints the token | TestE2EAsk |
| the stdin message ignored | TestE2EAsk |
| `-agent` not sent | TestE2EAsk |
| `-conversation` not sent | TestE2EAsk |
| always styled (a pipe gets escapes) | TestE2EAsk (chat on a pipe) |
| never styled | TestE2EChat |
| tool result not shown | TestChatCommands, TestE2EAsk, TestE2EChat |
| `/details` cuts the result | TestChatCommands |
| an unknown block dropped from `/history` | TestChatCommands |
| a long result not cut on its line | TestChatCommands |
| main keeps SIGINT (Ctrl-C kills chat) | TestE2EChat |
| Ctrl-C does not cancel the run | TestE2EChat |
| no wait for the server to end the run after Ctrl-C | TestChatCtrlCCancelsRun |
| Ctrl-C does not wait for the stream to open | TestChatCtrlCAsTheRunStarts (headers) |
| conversation not taken from the headers | TestChatCtrlCAsTheRunStarts |
| an empty line never resumes | TestChatCtrlCCancelsRun, TestE2EChat |
| `/new` keeps the conversation | TestE2EChat |
| chat forgets the conversation after a turn | TestChatCommands, TestChatCtrlCCancelsRun, TestE2EChat |
| Ctrl-C at the prompt ignored | TestChatCtrlCAtPromptQuits |
| Ctrl-D exits 130 | TestChatCommands, TestChatCtrlCCancelsRun, TestE2EChat |

### For T050

Evals should run a case as a subprocess:

```sh
AGENTKIT_SERVER=… AGENTKIT_API_TOKEN=… agentkit ask -json [-agent NAME] [-conversation ID] [-retry N] "message"   # or - for stdin
```

- **Read stdout line by line**; each line is one SSE `data` object as the server sent it: decode with `wire.Event` (`pkg/agentapi/wire`, whose decoding is additive). The first is `run_start` (its `conversation_id` continues the case with `-conversation`), the last `run_done` (`answer`, `turns`, `usage`) or `run_error` (`error.code`). Tool use is the `tool_call`/`tool_result` pairs (by `tool_use_id`).
- **Branch on the exit code**, not on stderr text: 0 done, 1 the run failed (the case's outcome: score it), 2 the harness is wrong (bad flag, token, server, agent: stop the suite), 3 try later (use `-retry N`, or space the cases; a conversation runs one run at a time), 130 interrupted.
- **Stderr** is for people; nothing on it is a format.
- The token comes from `$AGENTKIT_API_TOKEN` only; never put it in arguments (it would show in `ps`).
- Multi-turn cases: run `ask` once per user turn with the same `-conversation`.
