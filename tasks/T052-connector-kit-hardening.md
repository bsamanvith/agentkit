---
id: T052
title: "toolsettest hardening: checks the connectors still test themselves, golden first run, fuzz speed"
epic: E10
status: done
depends_on: [T049]
---

# T052: connector kit hardening

## Context

Once all three connectors had adopted T049's kit, their mutation runs (T046–T048, chiefly T048's handoff) found planted bugs that the suite lets through. The connectors still catch them with their own tests. Per E10 ("every generic bug becomes a suite check", added through the staged rollout), they move into the kit. Two rough edges of the kit itself come with them.

## Scope

- **New checks.** Each one is opt-in first, with its own check ID. Turn it on for Tavily, Exa and Parallel, then make it opt-out once all three pass.
  - `errors.no_retry_5xx`: a planted "retry on 5xx" must fail. If the current error matrix lets it pass, find out why (for example, a fresh upstream per case hiding the second request) and fix the counting.
  - `errors.retry_wait_ctx`: a `Retry-After` wait must end when `ctx` is cancelled (cancel during the wait, by the fake clock; it must return the context's error without waiting out the clock).
  - `errors.attempt_timeout`: a per-attempt timeout separate from `ctx`. A hanging upstream with a long `ctx` must end at the connector's configured timeout. The current hang check ends through `ctx`, so an ignored timeout passes.
  - `limits.rune_boundary`: truncated content is valid UTF-8 and ends with the steering marker, using a cassette body built with multi-byte characters at the cut point.
  - `settings.honored`: a connector declares settings whose effect shows in the request (for example Parallel's `mode`, Tavily's `search_depth`), each as a `Call` with per-call `Settings` and a golden request. A planted "setting ignored" must fail.
- **Golden first run:** with `AGENTKIT_UPDATE_GOLDEN=1` and no golden file yet, the suite writes the golden instead of failing because it read the file first.
- **Fuzz speed:** `FuzzResponses` builds the connector once per fuzz target, not on every input. Measure execs/s before and after, and record both in Notes.

## Acceptance criteria

- [x] **New checks catch their bugs:** each check fails on a toy-connector variant planted with its bug, on that check's ID. `TestPlantedBugs` runs the toy with each bug in a subprocess and requires that check's subtest to fail: `/retry_5xx_after_wait` → `TestToyConnector/errors/no_retry_5xx`, `/wait_ignores_ctx` → `errors/retry_wait_ctx`, `/ignore_timeout` → `errors/attempt_timeout`, `/cut_bytes` → `limits/rune_boundary`, `/ignore_summary_setting` → `settings/honored`. `TestEveryPlantedBugIsChecked` keeps the bug list and the map in step.
- [x] **Connectors pass:** Tavily, Exa and Parallel pass all five checks, enabled through `Enable` first (commit `86ab2c3`): `TestConformance/settings/honored/<call>`, `/errors/no_retry_5xx/{500,502,503}`, `/errors/retry_wait_ctx`, `/errors/attempt_timeout`, `/limits/rune_boundary` in `connectors/{tavily,exa,parallel}`. Each declares three `SettingCalls` with golden requests, an `AttemptTimeout` and a `Truncation`.
- [x] **Rollout:** after the connectors pass, the checks are moved to opt-out and the stage table in `docs/CONNECTORS.md` is updated. `checkid.go` stages them `optOut`; the `Enable` lists no longer name them; `TestChecksTable` keeps the table in step, and `TestSkipsAreReported` reports each one skipped with its reason.
- [x] **Golden first run:** a fresh golden is written when `AGENTKIT_UPDATE_GOLDEN=1` (a test with a temp dir). `TestGoldenFirstRun` copies `testdata` without `golden/` to a temp dir, runs `TestToyConnector` there with `AGENTKIT_UPDATE_GOLDEN=1`, and compares every golden written with the committed one.
- [x] **Fuzz speed:** `FuzzResponses` builds the connector once per target, and execs/s before and after are recorded (Notes: about 3× on every target).
- [x] **Mutation list** recorded (the five bugs above, plus "golden read before write"). 6 kit mutants and 15 connector mutants, all caught: tables in Notes.

## Notes

### As built

```go
type Connector struct {
    // ...
    SettingCalls   []Call          // settings.honored: Settings per call, no cassettes
    AttemptTimeout *AttemptTimeout // errors.attempt_timeout
    Truncation     *Truncation     // limits.rune_boundary
}
type AttemptTimeout struct {
    Settings json.RawMessage // e.g. {"timeout": "300ms"}
    Timeout  time.Duration
}
type Truncation struct {
    Call, Field string        // a happy call; a JSON pointer into its first cassette's body
    Settings    json.RawMessage
    Limit       int           // characters kept before the marker
    Marker      *regexp.Regexp // default: E10's …[truncated N characters; …]
}
```

| Check | Stage | What it does | Planted toy bug it fails |
|---|---|---|---|
| `settings.honored` | opt-out | each `SettingCalls` entry's requests match `testdata/golden/<Name>.request.json` and differ from the requests the same input sends without its `Settings` | `ignore_summary_setting` (`summary` never sent) |
| `errors.no_retry_5xx` | opt-out | 500, 502, 503 with `Retry-After: 1`, the cassettes queued behind it; every wait on `Env.Clock` ended at once (`Advance(1h)` whenever a timer is pending); exactly one request, `ErrToolFailed` | `retry_5xx_after_wait` (waits 1 s on the clock, then retries) |
| `errors.retry_wait_ctx` | opt-out | two 429s; `ctx` cancelled once the `Retry-After` wait is pending; `context.Canceled` within 2 s real time, the clock not advanced, one request | `wait_ignores_ctx` (`<-clock.After(d)` only) |
| `errors.attempt_timeout` | opt-out | `AttemptTimeout.Settings`, a hanging upstream, `ctx` with `Timeout + 9s`; must fail with `ErrToolFailed` no sooner than `Timeout` and within `Timeout + 3s` | `ignore_timeout` (no `context.WithTimeout`) |
| `limits.rune_boundary` | opt-out | `é語😀` repeated `Limit + 64` times at `Truncation.Field`; the output string must be valid UTF-8 with no U+FFFD, keep exactly `Limit` characters of it, then match `Marker` | `cut_bytes` (`s[:max]`) |

- **Why a planted 5xx retry passed `errors.500`.** Not the counting: one upstream serves each case and counts every request. The matrix never advances the fake clock, and `Env.Clock` is the only way a connector may wait. A retry that first waits (honoring `Retry-After`, or a backoff) never sends request 2, so "exactly one request" held. The call then ended at the connector's own timeout or at the 10 s watchdog. Exa's timeout bounds the whole call, so with a timeout under the watchdog (2 s, tried) its 5xx-retry mutant passed `errors.500/502/503` outright. With the default 30 s it showed only as a watchdog failure. `errors.500` stays as it was (a fast retry); `errors.no_retry_5xx` drives the clock while the call waits.
- **Per-call settings.** `honored` builds twice, with the call's settings merged over `Connector.Settings` and without them. It answers every request with the tool's first cassette and compares the decoded requests (method, path, query, body). The golden alone would pass a setting that was ignored when the golden was written; the difference check would not. Connector calls: Tavily `search_depth_advanced`, `search_topic_news` (+`include_answer`), `extract_depth_advanced` (+`format: text`); Parallel `search_mode_basic`, `search_location` (+`max_chars_per_result`), `extract_fetch_policy`; Exa `search_type_fast`, `search_contents_text` (+`category`), `extract_max_age`. Each golden was read to see the setting in the body.
- **Cut check.** `json.Marshal` turns a split character into U+FFFD, so a byte cut shows as U+FFFD, not as invalid UTF-8; the check looks for both. It also catches an off-by-one limit and a missing marker. Tavily's minimum limit is 100, so the connectors use `Limit: 100`.
- **Attempt timeout.** All three connectors take `"timeout": "<duration>"`; Tavily and Parallel bound each attempt, Exa the whole call. Both pass, since the hang is the first attempt. `errors.hang` still covers `ctx`.
- **Golden first run.** `sameAsGolden` read the golden before writing it, so the first `make golden` for a new call failed. It now writes the golden when update mode is on and the file is missing.
- **MCP watchdog.** `callMCP` had no deadline. A connector that hung inside the canary's MCP call stalled the whole package until `go test -timeout`, which hides every FAIL line, so the first mutation run reported the Tavily and Parallel 5xx mutants as surviving. It is now bounded by the 10 s watchdog.
- **Fuzzing.** `FuzzResponses` builds the tools once, through an `http.RoundTripper` that answers each input's body for `https://fuzz.toolsettest.invalid` and fails the input on any other host. Each input still checks that the key is in no output, error or log line, and that each request carries it in exactly one header. Measured with `go test <pkg> -run='^$' -fuzz='^<target>$' -fuzztime=15s -fuzzminimizetime=1x`, 14 workers, same machine, execs / 15 s. The after runs started from a cached corpus the before runs had grown (about 620 inputs against 430), which costs them a little:

  | Target | Before | After | |
  |---|---|---|---|
  | toolsettest `FuzzToySearchResponses` | 10,965/s | 31,709/s | ×2.9 |
  | parallel `FuzzResponsesSearch` | 10,697/s | 33,089/s | ×3.1 |
  | tavily `FuzzResponses` | 7,707/s | 22,970/s | ×3.0 |
  | exa `FuzzResponses` | 6,905/s | 22,852/s | ×3.3 |

- **Not done here (out of scope):** the connector template in `docs/CONNECTORS.md` and its compiled copy, `example_test.go`, do not show `SettingCalls`, `AttemptTimeout` or `Truncation`. A connector copied from them fails the three checks with a message naming the field to set, or the check to skip with a reason. `tasks/STATUS.md` still lists T052 as "Now".

### Mutation list

Kit mutants, each caught by the test named:

| Mutation | Caught by |
|---|---|
| 5xx retried after a wait on the clock (toy `retry_5xx_after_wait`) | `TestPlantedBugs/retry_5xx_after_wait` → `errors/no_retry_5xx` |
| `Retry-After` wait ignores `ctx` (toy `wait_ignores_ctx`) | `TestPlantedBugs/wait_ignores_ctx` → `errors/retry_wait_ctx` |
| per-attempt timeout ignored (toy `ignore_timeout`) | `TestPlantedBugs/ignore_timeout` → `errors/attempt_timeout` |
| content cut by bytes (toy `cut_bytes`) | `TestPlantedBugs/cut_bytes` → `limits/rune_boundary` |
| a setting accepted and ignored (toy `ignore_summary_setting`) | `TestPlantedBugs/ignore_summary_setting` → `settings/honored` |
| golden read before write (the fix in `sameAsGolden` removed) | `TestGoldenFirstRun` (`settings/base_url` fails in the fresh run) |

Connector mutants, planted in each connector's source, `TestConformance` run, the file restored. Run with the checks opt-in and enabled, and again after the move to opt-out; all caught both times:

| Connector | Mutation | Caught by (`TestConformance/…`) |
|---|---|---|
| Tavily, Exa, Parallel | retry a 5xx after a clock wait | `errors/no_retry_5xx/{500,502,503}` (also `errors/{500,502,503}` and `canary` at the 10 s watchdog) |
| Tavily, Exa, Parallel | `Retry-After` wait ignores `ctx` | `errors/retry_wait_ctx` |
| Tavily, Exa, Parallel | truncation cuts bytes | `limits/rune_boundary` |
| Tavily, Exa, Parallel | `timeout` setting ignored | `errors/attempt_timeout` |
| Tavily | `search_depth` ignored | `settings/honored/search_depth_advanced` |
| Exa | `type` ignored | `settings/honored/search_type_fast` |
| Parallel | `mode` ignored | `settings/honored/search_mode_basic` |
