# Good and bad tests, from this repo

Each pair tests the same behavior. The bad one breaks on unrelated changes, or passes when the feature is broken.

## Assert the outcome, not the mechanism

Behavior: resuming a flow does not re-run an agent step that already finished.

- Bad: count calls to `saver.Load` or check that a private `resumed` flag was set. A refactor that loads differently breaks it; a bug that loads correctly and then calls the model anyway passes.
- Good (`pkg/agentstep.TestResumeDoesNotRerunFinishedAgentStep`): resume with a model that has **no** scripted replies and assert it got zero requests and the output is in state. That is what the caller pays for.

## Compare with a reference run instead of pasting the expected value

Behavior: a step killed after turn 2 resumes and sends what an uninterrupted run would.

- Bad: a golden JSON of the third request. It breaks on any prompt, tool or encoding change, none of which break resume.
- Good (`TestInterruptedStepResumesWithOneMoreCall`): run the same script uninterrupted, then interrupted and resumed, and `cmp.Diff` the two request lists. Only a resume bug changes the diff.

## Error identity, not message text

- Bad: `strings.Contains(err.Error(), "conflict")`.
- Good: `errors.Is(err, flow.ErrConflict)` and `errors.As(err, &ce)` to check `ce.Keys`.

## One behavior, one test; inputs in a table

- Bad: `TestNewEmptyName`, `TestNewNilHarness`, `TestNewNilSaver`, ... six tests of one behavior.
- Good (`TestNewRejectsBadConfig`): one table, each row must give `ErrInvalidConfig`.

## Crash and concurrency at a fixed point, never by timing

- Bad: start the run, `time.Sleep(50 * time.Millisecond)`, cancel. It flakes, and it may cancel before or after the turn you meant.
- Good: the recording saver calls `cancel` right after its 3rd `Append` (the prompt, turn 1, turn 2), so the "kill" is always right after turn 2. In the replay e2e test the server holds request 2 until the test sends SIGINT.

## Stable fragments for human-facing output

- Bad: a golden file of the whole `agentkit run` output. Durations, spacing and wording change freely.
- Good (`cmd/agentkit.TestRunStreamsEvents`): `mustContain` the parts a user relies on: `→ calculate {...}`, the tool result, `· turn 1: tool_use`, the usage numbers from the scripted reply.

## Golden files only for wire formats

- Good: `pkg/llm/anthropic/testdata/request.golden.json`, the request body Claude receives, regenerated with `make golden` and reviewed in the diff.
- Bad: golden files for internal structs, logs or CLI output.

## Fakes that obey the same contract as the real thing

- Good: `llmtest.Fake` passes `llmtest.RunModel`, the suite every adapter runs, so the fake cannot drift from real models. The file store in `cmd/agentkit` runs `flowtest.RunCheckpointer` and `agentsteptest.RunSaver`.
- Bad: a hand-rolled stub that returns whatever the test needs, with no suite tying it to the real implementation.
