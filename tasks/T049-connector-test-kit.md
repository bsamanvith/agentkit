---
id: T049
title: "toolsettest: the connector test kit (recorded upstreams, conformance suite, honest fakes)"
epic: E10
status: done
depends_on: [T026, T018]
---

# T049: `pkg/toolset/toolsettest`

## Context

Every connector calls a paid API. We want two things (owner, 2026-10-12):

1. **Connector tests that are right by construction.** Each connector repeats the same checks: settings, key handling, error mapping, retries, size limits, leaks, MCP, no network in unit tests. A shared conformance suite does them once, the way `storetest` and `llmtest` do for stores and models. A connector passes it with one call, and cannot skip a check.
2. **Fakes of any tool or connector for every other test** (agent, API, flows, the binary), so no test outside `make test-live` ever pays.

The fakes stay honest the way `llmtest` keeps the fake model honest:
- their tool definitions come from the **real** connector;
- inputs and outputs are checked against the real schemas;
- the same suite runs against the fake.

A fake that drifts from the real connector fails.

Decisions: [E10](E10-search-connectors.md), "Testing connectors". T046–T048 adopt this kit as soon as it lands.

## Scope

### 1. Contract every connector factory follows (enforced by the suite)

- **Constructor options:** `WithHTTPClient`, `WithBaseURL`, `WithLookupEnv`, `WithClock`, `WithLogger`. No globals; no `http.DefaultClient`; no `os.Getenv`.
- **`base_url` setting:** `https` required unless the host is loopback. A config can point the connector at a local replay server, which is how binary-level tests avoid paying.
- **`Build` makes no network request.** Tool definitions are known without calling the API.
- **Tool names** are `<type>_<verb>`, each with a description, an object input schema and an output schema.

### 2. Recorded upstreams (cassettes)

- **One exchange per pair of files** in `testdata/cassettes/`:
  - `<name>.req.json` is the request: `{"method", "path", "query"?, "body"}`, with the body as JSON. Headers are not stored; the suite checks auth itself.
  - `<name>.res.http` is the response as `curl -i` prints it (the format `pkg/llm/anthropic/testdata` already uses).
- **`toolsettest.Upstream`** is an `httptest` server that serves cassettes:
  - a request matches a cassette when method, path and query match exactly and the body is JSON-equal (key order ignored);
  - an unmatched request fails the test with a diff against the closest cassette;
  - it records every request (headers included) for assertions;
  - it can also be scripted (status, headers, body, delay, hang) for the error matrix.
- **Recording:** with `-tags live` and `AGENTKIT_RECORD_CASSETTES=1`, `toolsettest.Record` runs a connector's calls against the real API through a recording proxy and writes the cassettes. It scrubs while writing:
  - only `Content-Type` and `Retry-After` response headers are kept;
  - no request headers are kept;
  - the key value anywhere in a body fails the recording.

  `make record-cassettes` runs it. Re-recording is the only way cassettes change, so they stay real.

### 3. The conformance suite: `toolsettest.RunConnector(t, toolsettest.Connector{...})`

The suite's own test API, roughly:

```go
type Connector struct {
    New       func(env Env) toolset.Factory  // the factory under test, wired to env
    Settings  json.RawMessage                // a valid configuration
    KeyEnv    string                         // env var with the API key; "" if none
    KeyOptional bool                         // true if the connector also runs keyless (Tavily auto)
    KeylessMarker http.Header                // header(s) a keyless request carries instead of the key
    Cassettes string                         // testdata/cassettes
    Calls     []Call                         // happy paths, each answered from cassettes, output golden-compared
    Invalid   []Call                         // inputs that must fail validation without any request
    // Optional, added in T049:
    ErrorBody func(message string) []byte    // the provider's error body carrying message (default {"error": message})
    MaxBodyBytes int64                       // the connector's body limit (default 8 MiB)
}
type Env struct { BaseURL string; HTTPClient *http.Client; LookupEnv func(string) (string, bool); Clock clock.Clock; Logger *slog.Logger }
type Call struct { Name, Tool string; Input json.RawMessage; Cassettes []string }
```

Checks the suite runs on every connector:

- **Definitions:** the type and tool names follow the rules; every tool has a description, an object input schema and an output schema; `Build` made zero requests.
- **Settings:** an unknown field fails `Build`; a literal `api_key` fails `Build`; a missing key fails naming `KeyEnv` (unless `KeyOptional`); `base_url` over plain `http` to a non-loopback host fails.
- **Happy calls** (every `Call`):
  - the output equals the golden file (`testdata/golden/<call>.json`, rewritten by `make golden`);
  - the output validates against the tool's output schema;
  - exactly the listed cassettes were requested;
  - every request carried the key in exactly one header (or the keyless marker), and nowhere else.
- **Invalid inputs:** each gives `tool.ErrInvalidInput` and makes zero requests.
- **Error matrix** on the first call, with a scripted upstream:
  - 400 and 422 give `ErrInvalidInput`, with the provider's message capped at 300 characters;
  - 401 and 403 give `ErrToolFailed`;
  - 429 with `Retry-After` gives exactly one retry after the fake clock passes it, then `ErrToolFailed`;
  - 429 then 200 succeeds;
  - 500, 502 and 503 give `ErrToolFailed` after exactly one request;
  - a 200 with malformed JSON gives `ErrToolFailed`;
  - a body over the limit gives an error and reads at most limit + 1 bytes;
  - a hanging upstream with a `ctx` deadline gives the context's error;
  - connection refused gives `ErrToolFailed`.
- **Canary:** the key is set to a canary value, and the upstream echoes it in error bodies. The canary must appear in no output, error string, captured log line (debug level included) or MCP response.
- **Hermetic:** `Env.HTTPClient` refuses any host but the upstream. A connector that bypasses the injected client or base URL fails here, so unit tests can never reach a paid API.
- **Over MCP:** the built tools served by `pkg/mcp/server` and called with the SDK client list the same definitions and return the same output for one call.
- **Concurrency:** 16 parallel happy calls under `-race` all return correct results.
- **Fuzzing:** a helper `toolsettest.FuzzResponses(f, c, tool)` serves fuzzed bodies. Any result is either output-schema-valid or an error, and never a panic. Each connector adds a three-line fuzz target.

### 4. Fakes for other tests (so nothing else pays)

- **`toolsettest.Definitions(tb, newFactory, settings)`** returns a connector's real tool definitions, using a dummy key and no network. (As built: `newFactory` is a `func(Env) toolset.Factory`, the same as `Connector.New`, so the kit can inject the dummy key and a client that refuses every host.)
- **`toolsettest.NewFake(t, defs, ...Script)`** returns a `tool.Registry` with exactly those definitions:
  - **Scripts:** a static output, a function of the input, an error class, a sequence, or "replay this golden file".
  - **Input check:** every call's input is validated against the real input schema; invalid input gives `ErrInvalidInput`, as the real connector would.
  - **Output check:** every scripted output is validated against the real output schema, so a fake cannot return a shape the real connector never would.
  - **Unscripted call:** a call with no script fails the test (no silent defaults).
  - **Recording:** `Calls()` returns what was called, for assertions.
- **`toolsettest.FakeFactory(type, defs, scripts)`** returns a `toolset.Factory` with the real type name, to substitute in a `toolset.Catalog`. Config-driven tests (a `search.json`-style config in `cmd/agentkit` or agent API tests) then run with fakes. Add the smallest seam to `cmd/agentkit` that lets its tests pass a catalog; production keeps `newCatalog()`.
- **`toolsettest.Replay(t, c Connector)`** returns the real connector served from its cassettes, for tests that want real request and response code with no network.
- **The fake runs the suite too:** a test runs the definitions, schema and canary parts of `RunConnector` against a `FakeFactory` (the parts that apply to a fake), as `llmtest.RunModel` runs against `llmtest.Fake`.

### 5. Self-test and docs

- **A toy connector** in `toolsettest/internal/toy` (a tiny JSON API) passes the suite.
- **Planted bugs:** variants of it, each with one bug, must make the suite **fail**. A run that should fail and passes is a failure of this task. Mechanism: run the suite in a subprocess (`go test -run` with an env var selecting the variant) and assert it fails with the expected check named, or another way that keeps the suite on a real `*testing.T`. The variants:
  - leaks the key in an error;
  - retries a 500;
  - ignores the injected HTTP client;
  - accepts unknown settings;
  - no output schema;
  - sends a request for invalid input;
  - reads an unbounded body;
  - ignores `ctx`.
- **Docs:** `docs/CONNECTORS.md`, how to build and test a connector:
  - layout, options, settings, `base_url`;
  - cassettes and `make record-cassettes`;
  - the one suite call, connector-specific tests on top, the fuzz target, the live test;
  - and, for everyone else, how to fake a connector.

  Add a "Connectors" section to the `write-tests` skill pointing at it, and add §2.8 to `docs/ENGINEERING.md` (short; links `CONNECTORS.md`).
- **Enforcement:** an archtest rule fails when a package under `connectors/` has no test calling `toolsettest.RunConnector`, or no `testdata/cassettes`.


### 6. Lessons from other connector ecosystems (research 2026-10-12, built into this task)

- **New checks roll in, not crash in** (Airbyte, LangChain).
  - Airbyte found that a new suite requirement breaks every connector's build at once; LangChain tells users to pin its test package for the same reason.
  - So every check has an ID (`settings.unknown_field`, `errors.retry_5xx`, ...), and a connector may skip one by ID with a reason, written in its test (`Skip: map[CheckID]string`).
  - Skips are printed in every run and listed in `docs/CONNECTORS.md`.
  - Rollout: a new check starts opt-in, becomes opt-out, then cannot be skipped.
- **Schema honesty** (Airbyte: "every column contains at least one non-null value").
  - Across a connector's cassette outputs, every field in each output schema must appear at least once with a non-null value, and no output may carry a field the schema doesn't declare.
  - This catches dead or misnamed fields, which a pure schema check misses.
- **Secrets are not only in headers** (VCR tools: query strings and POST bodies leak too).
  - Recording also fails if the key appears in the path or query.
  - A repo-wide test scans every `testdata/cassettes` file for known key shapes (`sk-ant-`, `tvly-`, long high-entropy tokens next to `key`/`token`/`secret`) and for `Set-Cookie`.
  - A connector can add a `Scrub` hook for account IDs and emails in responses, applied before writing.
- **Recordings go stale silently** (mock drift).
  - A drift check (`-tags live`, run with `make test-live`) replays each cassette's request against the real API and compares the response's **shape** (JSON paths and types, not values) with the cassette, reporting added, removed and retyped fields.
  - Each cassette records its recording date; the drift run warns past 90 days.
  - Where the provider publishes an OpenAPI spec (Exa, Parallel), the connector vendors it into `testdata/` and an offline test checks every cassette response against the spec's response schema. (Built in T051: check `cassettes.openapi`, run by Tavily, Exa and Parallel.)
- **Generated fixtures repeat the code's assumptions** (Nango).
  - A fixture not recorded from the real API is marked `synthetic` in its `.req.json`.
  - The suite prints the synthetic ones.
  - A connector with synthetic cassettes stays `review` until they are re-recorded.
  - Recording keeps a cassette only if the connector's output from it validates against the output schema (Nango saves mocks only when validation passes).
- **Rare failures never show up in recordings.** Auth expiry, rate limits and partial failures stay synthetic in the error matrix (already in §3), not recorded.
- **Every generic bug becomes a suite check** (Meltano adds a check to its standard suite each time users hit a new error). `docs/CONNECTORS.md` states the rule: when a connector bug is generic, add the check here and roll it in per the first point above, instead of fixing one connector.
- **Mocks can't show model behavior**, such as picking the wrong tool or sending arguments the schema rejects. That is the eval tier, [T050](T050-connector-tool-evals.md), not this kit. The kit's fakes are what make those evals cheap.

## Out of scope

- Credential slots (T044); the suite gains a credential-source check then.
- OAuth connectors: the suite grows when the first one arrives.
- Converting T046–T048: each connector session adopts the kit in its own task.

## Acceptance criteria

Tests are in `pkg/toolset/toolsettest` unless named otherwise.

- [x] **Toy connector:** passes `RunConnector` (`TestToyConnector`, `TestToyConnectorAuto`); `Replay` and `NewFake` serve a `llmtest.Fake` model in the harness (`TestAgentWithReplayedConnector`, `TestAgentWithFake`).
- [x] **Planted bugs:** each of the 19 toy bugs fails the suite on the check meant to catch it, in a subprocess (`TestPlantedBugs`), and every bug has a check (`TestEveryPlantedBugIsChecked`).
- [x] **Upstream matching:** `TestUpstreamMatchesJSONBodies` (key order, recorded requests), `TestUpstreamMismatchDiff`, `TestUpstreamRequiresTheHermeticClient`, `TestHermeticClientRefusesOtherHosts`.
- [x] **Recording**, against a local stand-in, with no key and no tag (the tests run in `make check`, not only under `-tags live`):
  - replays to the same output and writes the committed cassettes byte for byte: `TestRecordWritesReplayableCassettes`;
  - drops request headers and all but the allowed response headers (`Set-Cookie`, `X-Account-Id`, `X-Request-Id`, `Date`): the same test;
  - refuses the key in a body, the path or the query: `TestRecordRefusesTheKey`.
- [x] **Fake input check:** `TestFakeChecksInput`.
- [x] **Fake output check:** `TestFakeChecksOutput`.
- [x] **Unscripted call:** `TestFakeUnscriptedCall` (plus `TestFakeSequenceAndCalls` for `Sequence` and `Calls()`).
- [x] **`FakeFactory` in a catalog:** `TestFakeFactoryInACatalog` (built from config, served over MCP, called by the agent); `cmd/agentkit` `TestRunWithAFakeCatalog` and `TestServeMCPWithAFakeCatalog` through the new `cli.catalog` seam.
- [x] **Fake runs the suite:** `TestFakeRunsTheSuite` (`RunFake`).
- [x] **Archtest rule:** `internal/archtest` `TestConnectorRules` (fixture tree: no suite, no fuzz target, no cassettes, a lookalike kit, a stale pending entry); `TestRepository` runs it on the repository.
- [x] **Docs:** `docs/CONNECTORS.md`, ENGINEERING §2.8 and the `write-tests` "Connectors" section; the example code compiles as `Example*` tests in `example_test.go`; `TestChecksTable` keeps the doc's check table in step with the code.
- [x] Check IDs and skips: `TestSkipsAreReported` (every skipped and every opt-in check is reported, with the reason), `TestSkips` (unknown ID, mandatory ID, no reason), `TestEnable`.
- [x] Schema honesty: `TestPlantedBugs/dead_output_field` and `/undeclared_output_field`; `TestCallSettings` (a field only a call's own settings reach).
- [x] Secret scan: `TestSecretScanFindsSecrets` (key shapes, high-entropy credential, `Set-Cookie`), `TestNoSecretsInCassettes` (the repository), `TestRecordRefusesSecretShapes`, `TestRecordRefusesTheKey/{path,query}`, `TestRecordScrub`.
- [x] Drift: `TestDriftReportsChangedFields` (a retyped and a removed field), `TestDriftUnchanged`, `TestDriftWarnsOldCassettes` (synthetic, undated, over 90 days), `TestShapeDiff`.
- [x] Synthetic and validation on record: `TestSyntheticCassettesAreListed`, `TestRecordDropsOutputFailingSchema`.
- [x] **Mutation list** recorded in Notes, no survivors.

## Notes

- Keep the suite's checks **behavioral**:
  - error classes through `errors.Is`, never message text;
  - the single exception is "the missing-key error names the variable", which is the behavior asked for.
- `KeyOptional` covers Tavily's `auto` mode: without the key the suite expects the keyless marker header and no key header.

### As built

```go
type Connector struct {
    New           func(env Env) toolset.Factory
    Settings      json.RawMessage
    KeyEnv        string
    KeyOptional   bool
    KeylessMarker http.Header
    Cassettes     string             // default testdata/cassettes
    Calls         []Call
    Invalid       []Call
    ErrorBody     func(message string) []byte
    MaxBodyBytes  int64              // default DefaultMaxBodyBytes (8 MiB)
    Skip          map[CheckID]string // opt-out checks, each with a reason
    Enable        []CheckID          // opt-in checks
}
type Call struct {
    Name, Tool string
    Input      json.RawMessage
    Cassettes  []string
    Settings   json.RawMessage // merged over Connector.Settings for this call
}
type Recording struct {
    BaseURL   string
    LookupEnv func(string) (string, bool)
    Client    *http.Client
    Dir       string
    Scrub     func(body []byte) []byte
}

func RunConnector(t *testing.T, c Connector)
func RunFake(t *testing.T, c Connector)
func FuzzResponses(f *testing.F, c Connector, toolName string)
func Record(tb testing.TB, c Connector, rec Recording)
func Drift(tb testing.TB, c Connector, rec Recording)
func SkipUnlessRecording(tb testing.TB)
func Replay(tb testing.TB, c Connector) tool.Registry
func Definitions(tb testing.TB, newFactory func(Env) toolset.Factory, settings json.RawMessage) []tool.Definition
func NewFake(tb testing.TB, defs []tool.Definition, scripts ...Script) *Fake
func FakeFactory(tb testing.TB, typ string, defs []tool.Definition, scripts ...Script) *FakeToolset
// Scripts: Output, OutputFunc, Error, Golden, Sequence. Upstream: NewUpstream, Script, Requests, Client, URL.
```

Check IDs and stages are in `checkid.go` and the table in `docs/CONNECTORS.md`:
- **Mandatory:** `definitions`, `settings.literal_api_key`, `settings.plain_http_base_url`, `calls`, `keyless`, `invalid`, `canary`, `hermetic`.
- **Opt-out:** `settings.unknown_field`, `settings.missing_key`, `settings.base_url`, the `errors.*` matrix, `schema_honesty.undeclared`, `mcp`, `concurrency`.
- **Opt-in:** `schema_honesty.dead_fields`. It failed Tavily on `answer` (filled only under `include_answer`) and `results[].author`, so it rolls in. Exa enables it already.

Archtest:
- `CheckConnectors` and `Rules.PendingConnectors` (exemptions, each with a reason, that fail once the connector passes).
- `connectors/parallel` is pending. Exa removed its entry when it adopted the kit.

### Changes during the task

- **Per-call settings** (planner's decision): `Call.Settings` was added. It is merged over the connector's settings rather than replacing them, so a call adding one field keeps `auth: auto` and stays keyless under `KeyOptional`.
- **OpenAPI validation** of cassettes is T051, not here.
- **The upstream is served in process.**
  - Every rig used to dial its `httptest` server, and every fuzz iteration left loopback ports in TIME_WAIT.
  - Measured on 5 s of `FuzzToySearchResponses`: before, +14,860 TIME_WAIT; after, +0.
  - Parallel fuzz runs from several sessions had exhausted the machine's ephemeral ports ("can't assign requested address" in every networked test).
  - The hermetic client now runs the upstream's handler in process. The listener stays, both for the URL and to catch a connector that bypasses the client. The context, hang and body-limit semantics are unchanged (`TestPlantedBugs`).

### Mutation list

Each mutation was planted, the tests run, and the code restored. All are caught:

| Mutation | Caught by |
|---|---|
| the 19 planted toy bugs | `TestPlantedBugs/<bug>`, each on its check |
| the matcher ignores the request body | `TestToyConnector/calls`, `TestRecordWritesReplayableCassettes` |
| the after-every-subtest canary check skips logs | `TestPlantedBugs/log_key_on_retry` (a bug planted for this: the key is logged only on a 429 retry, which the `canary` subtest never sees) |
| the hermetic client allows other hosts | `TestHermeticClientRefusesOtherHosts`, `TestToyConnector/hermetic` |
| the fake skips output validation | `TestFakeChecksOutput` |
| the fake skips input validation | `TestFakeChecksInput`, `TestFakeRunsTheSuite/invalid`, `TestAgentWithFake` |
| Record misses the key in the query | `TestRecordRefusesTheKey/query` |
| the secret scan misses `Set-Cookie` | `TestSecretScanFindsSecrets` |
| drift misses a retyped field | `TestDriftReportsChangedFields`, `TestShapeDiff` |
| `Skip` accepts a mandatory check | `TestSkips` |
| an opt-in check runs without `Enable` | `TestSkipsAreReported` |
| honesty ignores undeclared fields | `TestPlantedBugs/undeclared_output_field` |
| `Call.Settings` is ignored | `TestToyConnector/calls`, `TestRecordWritesReplayableCassettes` |
| synthetic cassettes are not reported | `TestSyntheticCassettesAreListed` |
| archtest matches the kit by path suffix (a lookalike passes) | `TestConnectorRules` |
| archtest skips the cassette check | `TestConnectorRules` |

### For connector authors

1. **Write `conformance_test.go`.** It holds `connector()`, `TestConformance` calling `toolsettest.RunConnector`, and one `FuzzResponses` target per tool. Wire `Env` into your options. `docs/CONNECTORS.md` has the template.
2. **Record cassettes** with `make record-cassettes`, from a live-tagged `TestRecordCassettes` calling `toolsettest.Record(t, connector(), toolsettest.Recording{...})`.
   - Add `Scrub` for account IDs and emails in responses.
   - Mark cassettes you didn't record `"synthetic": true`. The suite lists them, and the connector stays `review` until they are re-recorded.
3. **Add a `TestDrift`** calling `toolsettest.Drift` in `live_test.go`.
4. **Skip only with a reason** in `Connector.Skip`, and add the skip to the table in `docs/CONNECTORS.md`.
5. **Enable `schema_honesty.dead_fields`.** If a field only appears under a setting, add a `Call` with its own `Settings` and record its cassette.
6. **Drop your `PendingConnectors` entry** in `internal/archtest/rules.go` in the same commit.
7. **A bug review finds that could hit any connector** becomes a suite check plus a planted toy bug, not a test in your connector.

### For T044 (credential slots)

- The suite owns key handling: `settings.literal_api_key`, `settings.missing_key`, `canary`, and the per-request rule "the key in exactly one header, nowhere else" in `rig.checkRequest`.
  - When keys come from a credential source instead of `api_key_env`, extend `Env` with the source (as `LookupEnv` is now).
  - Add a mandatory `credentials.*` check that the connector reads the key only through it, at call time.
- Keep the canary approach: the suite hands out a unique key per run and fails on it anywhere it shouldn't be.
- `Record` refuses the key in the path, query, bodies and headers. It reads the key through `Recording.LookupEnv`, which becomes the credential source too.

