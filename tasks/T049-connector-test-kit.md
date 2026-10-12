---
id: T049
title: "toolsettest: the connector test kit (recorded upstreams, conformance suite, honest fakes)"
epic: E10
status: todo
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

- **`toolsettest.Definitions(factory, settings)`** returns a connector's real tool definitions, using a dummy key and no network.
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
  - Where the provider publishes an OpenAPI spec (Exa, Parallel), the connector vendors it into `testdata/` and an offline test checks every cassette response against the spec's response schema.
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

- [ ] **Toy connector:** passes `RunConnector`; `toolsettest.Replay` and `NewFake` work for it in an agent-level test (a `llmtest.Fake` model calls the toy tools and gets cassette or scripted results).
- [ ] **Planted bugs:** every variant listed under "Self-test" makes the suite fail, each on the check meant to catch it.
- [ ] **Upstream matching:** JSON-equal bodies match regardless of key order; a mismatch fails with a readable diff; every request is recorded.
- [ ] **Recording** (`-tags live`, against a local stand-in server in the test, so no key is needed):
  - writes cassettes that replay to the same output;
  - drops all request headers and all but the allowed response headers;
  - refuses to write when the key appears in a body.
- [ ] **Fake input check:** a fake rejects input invalid against the real schema with `ErrInvalidInput`.
- [ ] **Fake output check:** a fake fails the test when a script returns output invalid against the real output schema.
- [ ] **Unscripted call:** a call with no script fails the test.
- [ ] **`FakeFactory` in a catalog:** substituted for a type in a `toolset.Catalog` and built from a config, it serves over MCP and to the agent, with no network.
- [ ] **Fake runs the suite:** a fake passes the applicable parts of the suite.
- [ ] **Archtest rule:** fails for a `connectors/x` package with no `RunConnector` test (fixture tree, like the existing archtest rule tests).
- [ ] **Docs:** `docs/CONNECTORS.md` and the `write-tests` skill section exist; their example code compiles (an `Example` test).
- [ ] Check IDs and skips: a skipped check is reported with its reason; an unknown check ID in `Skip` fails.
- [ ] Schema honesty: a toy variant with a declared output field that never appears in cassette outputs, or with an undeclared output field, fails the suite.
- [ ] Secret scan: a cassette containing a key-shaped string or `Set-Cookie` fails the repo-wide scan, and recording refuses a key in the path or query.
- [ ] Drift check (`-tags live`, against a local stand-in that changed one field's type and removed one field): reports both; an unchanged stand-in reports no drift.
- [ ] Synthetic cassettes are listed by the suite; recording drops an exchange whose mapped output fails the output schema.
- [ ] **Mutation list** recorded (the planted-bug variants plus at least: matcher ignoring the body, canary check skipping logs, hermetic client allowing other hosts, fake skipping output validation).

## Notes

- Keep the suite's checks **behavioral**:
  - error classes through `errors.Is`, never message text;
  - the single exception is "the missing-key error names the variable", which is the behavior asked for.
- `KeyOptional` covers Tavily's `auto` mode: without the key the suite expects the keyless marker header and no key header.
