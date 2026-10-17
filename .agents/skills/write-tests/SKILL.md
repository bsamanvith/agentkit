---
name: write-tests
description: Write or review tests for agentkit Go code. Use when adding tests for a task, a package or a bug fix, when choosing a test tier (unit, conformance, replay e2e, live, perf), or when tests break on changes that did not break any feature. Tests check outcomes that matter (behaviors and invariants), not implementation details.
---

# write-tests

**Goal: few tests that fail when a feature is wrong, and only then.** A test pins an outcome a caller depends on (what comes back, what is stored, what is sent, which error class), never how the code gets there. A hundred tests of one function's internals break on every refactor and still miss the bug on the path nobody named. Why, and the rules: [ENGINEERING §2](../../../docs/ENGINEERING.md#2-testing).

## Steps

1. **List the behaviors.** Take the task's acceptance criteria. Each is one observable behavior, so one test, at one tier ([§2.2](../../../docs/ENGINEERING.md#22-test-tiers)). Add only: errors a caller must handle, boundaries that bite (empty, cancel, concurrent), regressions. Write the criterion → test → tier list before writing code. Skip getters, option plumbing, generated code and anything a lower tier already proves ([§2.3](../../../docs/ENGINEERING.md#23-how-many-tests-one-per-behavior-not-per-function)).

2. **Assert outcomes through the public API.** Black box (`package foo_test`). Check return values, state, stored bytes, requests sent, events emitted, error identity with `errors.Is`/`errors.As`. Never a call count on an internal, a log line or an error string. Prefer a comparison to a hard-coded expectation when one exists: "the resumed run's requests equal an uninterrupted run's" survives prompt and format changes; a pasted JSON blob does not. Good and bad examples: [references/examples.md](references/examples.md).

3. **Fakes, not mocks.** Use the `xxxtest` fake (`llmtest.Fake`, `flow.NewMemory`, `agentstep.NewMemorySaver`). A new interface with several implementations gets a conformance suite in its `xxxtest` package, run by every implementation and by the fake ([§2.4](../../../docs/ENGINEERING.md#24-conformance-suites-the-main-anti-duplication-tool)).

4. **Name the invariants and check them after every test** ([§2.6](../../../docs/ENGINEERING.md#26-invariants-and-simulation-for-code-with-many-paths)). For each entry point, write down what holds for every call whatever the input: outputs valid, saved state reloads, what was sent is never changed later, nothing handed out shares memory with internal state, totals add up, no goroutine outlives the call. Put the checks in the one helper every test uses: per-call checks at return, cross-call checks (across a crash and a resume) registered with `t.Cleanup`. Check only what the docs promise, including who owns what; a guarantee you invent finds false bugs. Where an option holds one value (an observer), the helper must chain the test's own, not replace it. Under `t.Parallel()` you cannot count goroutines; check observable proxies instead (no events after return, in-flight counters in the fakes). Worked examples: [references/invariants.md](references/invariants.md).

5. **Code with many paths** (concurrency, cancellation, state machines, parsers, hostile input): add a seeded simulation through the same helper and a `Fuzz*` target over the seed. Vary what the consumer does (stop, panic, cancel, scribble on what it gets). Judge results against an independent restatement: a reference interpreter for whole outcomes, a second rule set for validators; never against the code itself. Assert the generator's case mix (a test that every kind of case occurs), because a bug often survives for lack of inputs, not checks. Fuzz a new invariant before trusting it: an over-strong one fails correct code.

6. **No timing.** No `time.Sleep`. Concurrency and crashes are driven from the test's side at a fixed point: a hook that cancels after the Nth save, a gate opened on an event, a server that holds a request until the client gives up. A gate that waits for something that *must* happen needs an escape that only gives up the forced order, so a bug fails instead of hanging. A wait for something that must *not* happen may only let a broken implementation pass, never fail a correct one; say so in a comment ([§2.5](../../../docs/ENGINEERING.md#25-rules-that-keep-tests-from-being-flaky-or-brittle)).

7. **Pick the tier for the outcome:**
   - Unit and conformance: almost everything.
   - **Replay e2e** when the outcome is about the real entry point: flags, files, signals, several processes. Build the binary and point it at a local server replaying responses recorded from the real API: [references/e2e-replay.md](references/e2e-replay.md).
   - **Live** (`//go:build live`) for the one thing only the real provider can answer; skip without credentials. Never assert on model wording beyond what the prompt forces.
   - **Perf**: a rerunnable `Benchmark*` for the hot path and a load test whose numbers go in the task's Notes; no timing asserts in unit tests ([§2.7](../../../docs/ENGINEERING.md#27-performance-tests), [references/perf.md](references/perf.md)).
   - **Evals** (scoring model quality): not yet. Do not write them until the eval tier is planned.

8. **Mutation self-check.** Plant, one at a time, the bugs a reviewer would look for, run the tests, restore. Each must fail a test (a compile error does not count). A survivor means a missing general check: add the invariant, not a test for that one case. Count a timeout or a race report as caught, and note catches that depend on timing (they are luck, not a check). Record the list and the catching tests in the task's Notes: [references/mutation-checklist.md](references/mutation-checklist.md).

   **A real bug found while testing** is reported to the code's owner with a deterministic failing test and the proposed fix. Do not commit a failing test to `main`, and do not weaken a check to hide it.

9. **Prune.** Delete tests that duplicate a lower tier, assert internals, or would break on a refactor that changes no behavior. Fewer, stronger tests is the target, not coverage.

10. **Run** `go test -race -shuffle=on -count=20` on the package (`-count=300` for anything that depends on goroutine order), then `make check`. Every test calls `t.Parallel()` unless it documents why not.

## Connectors

A connector (anything under `connectors/`) is tested with the kit in `pkg/toolset/toolsettest`; follow [docs/CONNECTORS.md](../../../docs/CONNECTORS.md), and the `write-connector` skill for writing, auth and running one locally:

- **The suite.** One `toolsettest.RunConnector` call, with semantic `Invalid` cases.
- **Cassettes.** Recorded with `make record-cassettes`, with goldens from `make golden`.
- **The provider's spec.** If it publishes an OpenAPI spec, vendor it with `make openapi-specs` and set `Connector.OpenAPI`; check `cassettes.openapi` validates every cassette against it.
- **Fuzz.** A `FuzzResponses` target per tool.
- **Live tests.** `Record` and `Drift` in `live_test.go`.
- **Your own tests.** Only for what is specific to the provider.
- **Skips.** A skipped check needs a reason in `Connector.Skip` and a line in CONNECTORS.md.
- **A generic bug.** When review finds one, add a suite check and a planted bug in the toy instead of a test in one connector.

Code that *uses* a connector never calls the real one. Use these instead:

- `toolsettest.NewFake`, built from `toolsettest.Definitions`;
- `toolsettest.FakeFactory` in a catalog;
- `toolsettest.Replay`, for the real connector answered from its cassettes.

## Done when

- Every criterion maps to a named test, listed in the task file.
- The invariants are written down and checked after every test through one helper.
- The mutation list is in the task's Notes, with no survivors.
- `-count=20` is clean and `make check` passes.
