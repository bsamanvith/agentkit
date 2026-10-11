# Replay e2e: the real binary, recorded responses

Worked example: `cmd/agentkit/e2e_test.go`.

## When

The outcome depends on the real entry point: flag parsing, files on disk, exit codes, signals, or state handed from one process to the next. Unit tests call functions; this tier runs the built binary with `os/exec`.

## How it is built

1. **Build once** per test process (`sync.Once`, `go build -o tmp/agentkit .`). Skip under `-short`, before setting anything else up.
2. **A local server stands in for the provider.** The binary gets `ANTHROPIC_BASE_URL=<server>`, a dummy key, an empty `HOME` and nothing else from the environment.
3. **The server replays fixtures in order** from `testdata/e2e/<scenario>/NN.http`. These are raw HTTP responses (status line, `Content-Type`, blank line, the SSE stream), the same form the adapter tests use. It keeps every request body with the name of the process that sent it.
4. **Crashes at a fixed point:** the server holds request N until the client gives up, and the test sends SIGINT when it arrives. No timing.
5. **Assertions are outcomes:** exit codes, stable output fragments, the state file's contents, and comparisons between processes. For example, the resumed process's first request is byte-equal to the one the killed process was waiting for, and turn 1 is never sent again. A shared `checkConversation` runs over all requests. The server also fails the test if any fixture went unused, which flags a changed number of model calls.

## Recording

```sh
AGENTKIT_E2E_RECORD=1 ANTHROPIC_API_KEY=... go test -count=1 -run TestE2E ./cmd/agentkit/
```

In record mode the server forwards each request to the real API with the real key and writes the response as the next fixture. The binary still only has the dummy key, and fixtures keep no account headers; check the diff. Re-record after changing prompts, tools, the adapter or the model. Recordings vary between runs, so assertions must hold for any reasonable model behavior: check structure and comparisons, not wording. If a scenario needs a property in the recording (a thinking block in turn 1), check the recording and record again if it is missing.
