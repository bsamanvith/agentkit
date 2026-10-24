# Connectors: how to build and test one

A connector is a `toolset.Factory` for a provider's API (Tavily, Exa, Parallel, ...): it turns settings into tools the agent and the MCP server call. Every connector calls a paid API, so:

- one shared suite, `toolsettest.RunConnector`, checks everything connectors have in common, and a connector passes it with one call;
- no test outside `make test-live` ever reaches a provider. Connector tests replay recorded responses ("cassettes"), and every other test uses fakes that cannot drift from the real connector.

The kit is `pkg/toolset/toolsettest`. Its worked example is the toy connector in `pkg/toolset/toolsettest/internal/toy` (a tiny search API), and `example_test.go` there compiles the code on this page. Background: [ENGINEERING §2.8](ENGINEERING.md#28-connectors), [E10](../tasks/E10-search-connectors.md), [T049](../tasks/T049-connector-test-kit.md).

## 1. Layout

```
connectors/<name>/
  <name>.go            factory, options, settings, tools
  <name>_test.go       the suite, connector-specific tests, fuzz targets
  live_test.go         //go:build live: Record and Drift
  testdata/cassettes/  <exchange>.req.json + <exchange>.res.http, recorded
  testdata/golden/     <call>.json, the output of each happy call
  testdata/openapi.json the provider's OpenAPI spec, cut to the operations used (make openapi-specs)
  testdata/openapi-overlay.json  corrections to that spec, where the real API contradicts it (rare)
```

A provider with several products is a **family**: a directory with one connector package per toolset and a shared library package, such as `connectors/google/{common,gmail,calendar,drive}` ([E21](../tasks/E21-google-connectors.md)). `internal/archtest` checks connectors at any depth, and the library is named in `ConnectorLibraries` in `rules.go` so it is not held to the connector rules. A connector that acts for a user's account, not a deployment's key, takes the user's OAuth access token as its "key": `KeyEnv` names the variable the dev fallback reads, and the per-user path is a `TokenSource` option the connector tests itself.

## 2. The contract (the suite checks every point)

**Options.** `WithHTTPClient`, `WithBaseURL`, `WithLookupEnv`, `WithClock`, `WithLogger`. No globals: no `http.DefaultClient`, no `os.Getenv`, no `time.Sleep`. `WithBaseURL("")` means the default URL, because the suite passes `""` when it checks the `base_url` setting.

**Settings** are decoded strictly with `toolset.DecodeSettings`, so an unknown field fails `Build` with `toolset.ErrInvalidSettings`. They follow these rules:

- The key comes from the variable named by `api_key_env`. A literal `api_key` field fails.
- A missing key fails `Build` with an error naming the variable, unless the connector also runs keyless.
- `base_url` overrides `WithBaseURL`. It must be `https`, or `http` only for a loopback host. That lets a config point a connector at a local replay server.

**Per-tenant credentials** (T044). A spec may name a credential slot (`"credential": {"slot": ..., "scope": "tenant"|"user"}`); `Build` then gets `in.Credentials`, a `credentials.Source` of that slot only. A tool calls `src.HTTPClient(ctx, client, ...)` (or `src.Get(ctx)`) with its call's `ctx`, which picks the caller's tenant (and user) credential, refreshing OAuth2 tokens as needed. A `credentials.ErrNotConnected` is returned as it is (the model reads "<toolset> is not connected for this account"); an upstream error gives its status, never its body. `toolsettest.CredentialEcho` is the reference. A connector that sends an API key itself (in its own header, and redacting it from responses) uses `connectors/internal/apikey`: the caller's key from the slot when there is one, else the environment's (Exa, Parallel); `toolsettest.KeySource` and `toolsettest.AsUser` test it per user. The suite does not run slot-based connectors yet: the env-key rules above still apply to the ones that take keys from the environment.

**`Build` makes no request.** Tool definitions are known without calling the API.

**Tools.** Each is named `<type>_<verb>`, with a description, an object input schema and an output schema (`tool.Func` derives both).

**Calls:**

- **Key placement.** The key goes in exactly one header. It never goes in the URL, the body, an output, an error or a log line, at debug level included.
- **Input checks.** Validate the input before any request. Input the JSON schema accepts but the API would refuse (a limit over the maximum, a non-http URL) gives `tool.ErrInvalidInput`.
- **400 and 422** give `tool.ErrInvalidInput` carrying the provider's message, with the key redacted and capped at 300 characters, so the model can fix its input.
- **Every other failure** gives `tool.ErrToolFailed`.
- **429 with `Retry-After`** gets one retry after waiting on `Env.Clock`.
- **5xx is not retried**: the paid call may have run.
- **The body** is read through `io.LimitReader(body, limit+1)`, with a limit of 8 MiB unless the connector sets `MaxBodyBytes`.
- **`ctx`** is honored in every request and wait.

`connectors/internal/httpx` implements the body limit, the retry wait and redaction, and `connectors/internal/trunc` the truncation markers. Every connector uses them rather than its own copy.

**Keyless mode** (Tavily's `auto`). Without a key, the connector sends a marker header (`KeylessMarker`) and no key.

## 3. The test file

```go
func newFactory(e toolsettest.Env) toolset.Factory {
	return toy.New(
		toy.WithBaseURL(e.BaseURL), // "" means the default
		toy.WithHTTPClient(e.HTTPClient),
		toy.WithLookupEnv(e.LookupEnv),
		toy.WithClock(e.Clock),
		toy.WithLogger(e.Logger),
	)
}

func connector() toolsettest.Connector {
	return toolsettest.Connector{
		New:      newFactory,
		Settings: json.RawMessage(`{"api_key_env": "TOY_API_KEY"}`),
		KeyEnv:   "TOY_API_KEY",
		Calls: []toolsettest.Call{
			{Name: "search_go", Tool: "toy_search", Input: json.RawMessage(`{"query": "go", "max_results": 2}`), Cassettes: []string{"search_go"}},
			{Name: "get_go1", Tool: "toy_get", Input: json.RawMessage(`{"id": "go1"}`), Cassettes: []string{"get_go1"}},
		},
		Invalid: []toolsettest.Call{
			// Inputs the JSON schema accepts but the connector must refuse.
			{Name: "max_results_too_big", Tool: "toy_search", Input: json.RawMessage(`{"query": "go", "max_results": 99}`)},
			{Name: "bad_id", Tool: "toy_get", Input: json.RawMessage(`{"id": "../admin"}`)},
		},
		ErrorBody: toy.ErrorBody,
		Skip: map[toolsettest.CheckID]string{
			"errors.422": "the toy API never answers 422", // listed in docs/CONNECTORS.md
		},
	}
}

func TestConformance(t *testing.T) { toolsettest.RunConnector(t, connector()) }

func FuzzSearchResponses(f *testing.F) { toolsettest.FuzzResponses(f, connector(), "toy_search") }
```

`Connector` fields:

- **`Calls`** are the happy paths, at least one per tool. Each lists the cassettes it requests, in order. The suite compares its output with `testdata/golden/<Name>.json` (`make golden` rewrites it) and runs the error matrix on the first call.
- **`Call.Settings`** is merged over `Connector.Settings` for one call: each of its fields replaces the connector's. Use it for output that a setting turns on, such as an answer the provider only sends when asked. Record that call's cassettes with the same settings.
- **`Invalid`** must include semantic cases. `tool.Func` already rejects input the schema does not accept before connector code runs, so a schema-invalid input tests nothing of yours.
- **`ErrorBody`** formats the provider's error body (default `{"error": message}`), so the suite can check that a 400's message reaches the model.
- **`MaxBodyBytes`** is your body limit, if not 8 MiB.
- **`KeyOptional` and `KeylessMarker`** are for a connector that also runs without a key.
- **`OpenAPI`** is the provider's vendored OpenAPI spec, for check `cassettes.openapi` (see "Cassettes").
- **`Skip` and `Enable`** skip an opt-out check, with a reason, or run an opt-in one (see "The checks").

Add a fuzz target per tool. `FuzzResponses` fuzzes the 200 bodies, seeded from your cassettes. Any body must give schema-valid output or a classed error, and never a panic. `make fuzz` runs it.

`internal/archtest` enforces the minimum: every package under `connectors/` has a test calling `toolsettest.RunConnector`, a fuzz target calling `toolsettest.FuzzResponses`, and at least one cassette. A connector still adopting the kit is listed in `PendingConnectors` in `internal/archtest/rules.go`, with the reason. The entry must be removed once the connector passes, and the check fails until it is.

Connector-specific behavior goes in ordinary tests next to the suite, for example how a provider's odd field maps to the output. Do not re-test what the suite checks.

## 4. Cassettes

- **Format.** `<exchange>.req.json` is the request:
  ```json
  {"method", "path", "query"?, "body"?, "recorded_at"?, "synthetic"?}
  ```
  `<exchange>.res.http` is the response as `curl -i` prints it. Request headers are never stored; the suite checks authentication itself.
- **Matching.** A request matches when method, path and query are equal and the bodies are equal as JSON (key order ignored). An unmatched request fails with a diff against the closest cassette.
- **Recording.** `make record-cassettes` runs every `TestRecordCassettes` (`-tags live`, `AGENTKIT_RECORD_CASSETTES=1`). It calls `toolsettest.Record`, which:
  - runs your `Calls` against the real API through a recording proxy;
  - writes the files and stamps `recorded_at`;
  - replays them to check they give the same output.

  Then run `make golden` and review both diffs. Re-recording is the only way cassettes change.
- **Scrubbing.**
  - Only the `Content-Type` and `Retry-After` response headers are kept.
  - `Recording.Scrub` rewrites response bodies (account IDs, emails) before your connector or the file sees them.
  - Recording refuses to write, and fails, if the key appears in a path, query, body, response or log line, or if a file would hold a secret. Secrets means a known key format (`sk-ant-`, `tvly-`, ...), a credential-named field with a random value, or `Set-Cookie`.
  - `TestNoSecretsInCassettes` scans every `testdata/cassettes` in the repository the same way.
- **Schema gate.** An exchange whose output fails your output schema is not written. A cassette must show the connector working.
- **Synthetic cassettes.** A cassette written by hand (a rare error, a case the real API will not produce on demand) is marked `"synthetic": true`. The suite lists synthetic cassettes in its log, and a connector with any stays `review` until they are recorded. Keep rare failures (auth expiry, rate limits) in the suite's scripted error matrix, not in cassettes.
- **Drift.** `toolsettest.Drift` (in `live_test.go`, run by `make test-live`) makes the calls against the real API, writing nothing, and compares each response's shape with its cassette. The shape is JSON paths and types, not values. Added, removed and retyped fields fail the run. It also warns about cassettes recorded over 90 days ago.
- **OpenAPI validation.** Where the provider publishes an OpenAPI spec, vendor the operations you use in `testdata/openapi.json` and set `Connector.OpenAPI`. Check `cassettes.openapi` (`toolsettest.ValidateCassettes`) then validates every cassette offline:
  - it finds the operation by method and path (`{param}` templates match one segment), and checks the request body against its request schema and the response body against its schema for the status (the exact code, else `4XX`, else `default`);
  - a failure names the cassette, the JSON path and the rule: `cassette search_go: response 200 at $.hits[*].link: type: 7 has type "integer", want "string"`;
  - an operation or status the spec lacks fails, it is never skipped;
  - the spec is JSON, OpenAPI 3.0 (`nullable`, boolean `exclusiveMinimum`) or 3.1. A schema keyword the check does not know fails the spec, so a re-vendored spec cannot pass by being misread. `format` is not checked.

  This matters most for `synthetic` cassettes, which otherwise only repeat the code's own assumptions. To refresh the specs, run `make openapi-specs` (it fetches each provider's published spec, cuts it with `pkg/toolset/toolsettest/testdata/openapi/cut.go`, and writes `testdata/openapi.json`), then review the diff. A new connector adds its line to that target. Never edit a vendored spec by hand. Where the published spec contradicts the real API, put the correction in `testdata/openapi-overlay.json` and pass it to the cut with `-overlay` (Tavily's is the example). Each correction gives a JSON Pointer into the cut, the keywords to `set`, and a recorded (not synthetic) `cassette` whose response `shows` the behaviour. The cut fails if the cassette does not show it, or if the correction changes nothing (the provider fixed its spec: delete the correction). Each corrected schema carries `x-agentkit-overlay` in the vendored spec, so the diff shows what was changed and why. A connector whose provider publishes no spec skips the check with that reason. For a rig that checks every request it sends, `toolsettest.LoadOpenAPI(path)` and `(*OpenAPI).CheckRequest` apply the same schemas.

```go
//go:build live

func TestRecordCassettes(t *testing.T) {
	toolsettest.SkipUnlessRecording(t)
	toolsettest.Record(t, connector(), toolsettest.Recording{
		BaseURL:   toy.DefaultBaseURL,
		LookupEnv: os.LookupEnv,
	})
}

func TestDrift(t *testing.T) {
	toolsettest.Drift(t, connector(), toolsettest.Recording{BaseURL: toy.DefaultBaseURL, LookupEnv: os.LookupEnv})
}
```

## 5. The checks

Each check has an ID: its subtest path with dots. The subtest names are a contract, so a connector's CI output reads the same everywhere. After every subtest, whatever it checked, the suite also checks:

- the key appears in no output, error or log line;
- every keyed request carried the key in exactly one header and nowhere else, or the keyless marker and no key;
- every request came through `Env.HTTPClient`.

| ID | Stage | What it checks | Bug it catches |
|---|---|---|---|
| `definitions` | mandatory | type and tool names, descriptions, object input and output schemas, a happy call per tool, `Build` sends nothing | a tool with no output schema; a request in `Build` |
| `classified` | opt-in | every tool, under every settings the calls use, declares its `kind` and `risk` (`tool.WithMetadata`) | a tool that policies must treat as unclassified (`write`/`medium`), so a read-only run cannot use a search tool |
| `settings.unknown_field` | opt-out | an unknown settings field fails `Build` | settings decoded loosely: typos silently ignored |
| `settings.literal_api_key` | mandatory | a literal `api_key` fails `Build` | keys pasted into config files |
| `settings.missing_key` | opt-out | a missing key fails, naming the variable | a confusing error, or a run without a key |
| `settings.plain_http_base_url` | mandatory | `base_url` over plain http to a non-loopback host fails | a key sent in clear text |
| `settings.base_url` | opt-out | the `base_url` setting alone routes requests | a setting that is accepted and ignored |
| `settings.honored` | opt-out | each of `SettingCalls` (a tool, an input and the `Settings` under test) sends the requests in `testdata/golden/<Name>.request.json`, which differ from those sent without its settings | a setting that is accepted and ignored (a mode, a search depth) |
| `calls` | mandatory | each happy call: golden output, schema-valid, exactly its cassettes requested | wrong mapping, extra or missing requests, the key in a query string |
| `keyless` | mandatory | (`KeyOptional`) each happy call without a key sends the marker and no key | keyless mode sending nothing, or a stale key |
| `invalid` | mandatory | each invalid input gives `ErrInvalidInput` and sends nothing | a paid request for input that was always going to fail |
| `cassettes.openapi` | opt-out | every cassette's request and response bodies fit the provider's vendored OpenAPI spec (`Connector.OpenAPI`) for their operation and status | a hand-written (synthetic) cassette in a shape the real API never sends, so the tests pass against a fiction |
| `errors.400` | opt-out | `ErrInvalidInput` with the provider's message, capped | the model never learns why its input was refused |
| `errors.422` | opt-out | as 400 | as 400 |
| `errors.401` | opt-out | `ErrToolFailed`, one request | auth errors reported as bad input |
| `errors.403` | opt-out | as 401 | as 401 |
| `errors.500` | opt-out | `ErrToolFailed`, exactly one request | a retry of a call that may have run (and been billed) |
| `errors.502` | opt-out | as 500 | as 500 |
| `errors.503` | opt-out | as 500 | as 500 |
| `errors.no_retry_5xx` | opt-out | a 5xx with `Retry-After`, every wait on `Env.Clock` ended at once: `ErrToolFailed`, exactly one request | a retry of a 5xx that first waits on the clock, which `errors.500` never advances |
| `errors.429_retry_after` | opt-out | one retry, only after `Retry-After` passes on `Env.Clock` | hammering a rate-limited API; `time.Sleep` |
| `errors.429_then_200` | opt-out | a retry that succeeds returns the output | a retry that drops the result |
| `errors.retry_wait_ctx` | opt-out | cancelling `ctx` during the wait for `Retry-After` returns `context.Canceled` at once, the clock not advanced | a retry wait that ignores `ctx` |
| `errors.malformed_json` | opt-out | a 200 with broken JSON gives `ErrToolFailed` | a panic or empty output |
| `errors.oversized_body` | opt-out | a body over the limit fails, reading at most limit + 1 bytes | an unbounded read |
| `errors.hang` | opt-out | a hanging upstream returns the context's error at its deadline | ignoring `ctx` |
| `errors.attempt_timeout` | opt-out | with `AttemptTimeout.Settings` and a long `ctx`, a hanging upstream fails with `ErrToolFailed` at the connector's own timeout | a timeout setting that is accepted and ignored; `errors.hang` ends through `ctx` |
| `errors.connection_refused` | opt-out | `ErrToolFailed` | a raw transport error |
| `limits.rune_boundary` | opt-out | multi-byte content put at `Truncation.Field` of a call's cassette comes out as its first `Limit` characters, whole, then the marker | truncation by bytes, splitting a character; an off-by-one limit; a cut with no marker |
| `schema_honesty.undeclared` | opt-out | no happy call's output carries a field its output schema does not declare | a field the model gets but the schema never documented (an open schema passes it) |
| `schema_honesty.dead_fields` | opt-in | across the happy calls' outputs, every declared output field appears non-null at least once | a dead or misnamed field; for a field that depends on settings or rare inputs, it asks for a call that covers it |
| `canary` | mandatory | the key, echoed by the upstream in error bodies, reaches no output, error, MCP result or log line | a key leaked into an error message or a debug log |
| `hermetic` | mandatory | `Env.HTTPClient` refuses other hosts, and the calls use it | a connector that bypasses the injected client and reaches the real API from a unit test |
| `mcp` | opt-out | served over MCP, the same definitions and output; an invalid call is an error result | a tool that breaks over the wire |
| `concurrency` | opt-out | 16 parallel calls on one set of tools are all correct (under `-race`) | shared mutable state |

`RunFake` runs `definitions`, `calls`, `invalid`, `mcp` and `concurrency` against a fake.

### Skipping a check, and rolling one in

A connector may skip an opt-out check, with a reason, in its test: `Skip: map[toolsettest.CheckID]string{"errors.422": "the API never answers 422"}`.

- The skip shows as `--- SKIP` with the reason under `go test -v`.
- Naming a mandatory check, an unknown ID or no reason fails the suite.
- Every skip in the repository is listed below; add yours in the same change.

A new check rolls in, not crashes in. When a check would fail connectors that exist, it lands opt-in: a connector runs it by naming it in `Connector.Enable`, and the suite reports it skipped otherwise. Once every connector passes it, it becomes opt-out, and later mandatory. Enabling an unknown ID, or one that is not opt-in, fails the suite.

**Skips in the repository:**

| Connector | Check | Reason |
|---|---|---|
| `connectors/google/gmail` | `cassettes.openapi` | Google publishes Discovery documents for Gmail, not OpenAPI; the synthetic cassettes are checked against the API reference by hand until they are recorded |
| `connectors/google/gmail` | `settings.honored` | the only behavioural setting, `max_body_chars`, changes the output and not the request; `TestBodyLimitSetting` covers it |
| `connectors/google/gmail` | `limits.rune_boundary` | bodies are base64url in Gmail's responses, so the kit cannot put its multi-byte text there; `TestBodyIsCutAtARuneBoundaryAndSaysHowToGetMore` covers it |
| `connectors/google/drive` | `cassettes.openapi` | Google publishes Discovery documents for Drive, not OpenAPI; the synthetic cassettes are checked against the v3 reference by hand until they are recorded |
| `connectors/google/drive` | `settings.honored` | no setting changes a request: `read_only`, `allow_share` and `narrow_scopes` choose tools and scopes, the two limits cut output; `TestGatingFollowsSettings`, `TestScopes` and `TestReadFile` cover them |
| `connectors/google/drive` | `limits.rune_boundary` | the text of `drive_read_file` is a raw response body, not a JSON field, so the kit cannot put its multi-byte text there; `TestReadCutsAtARuneBoundary` covers it |

### Every generic bug becomes a check

When a bug in one connector could happen in any connector, do not only fix that connector. Do three things:

1. Add a check to the suite (opt-in or opt-out, as above).
2. Plant the bug in the toy (`toy.Bug...`).
3. Map it in `plantedBugs`, so `TestPlantedBugs` proves the check catches it.

The planted bugs show what the suite is for:

- leaking the key in an error or a log;
- retrying a 500;
- ignoring the injected client, a setting or `ctx`;
- accepting unknown settings or plain http;
- an unbounded body read;
- the key in the query;
- a request in `Build` or for invalid input;
- ignoring `Retry-After`;
- a 400 reported as a failure;
- keyless requests without the marker;
- a dead or undeclared output field.

## 6. Faking a connector in other tests

Tests of code that uses a connector (the agent, the agent API, flows, the binary) use fakes that stay honest:

```go
defs := toolsettest.Definitions(t, newFactory, json.RawMessage(`{"api_key_env": "TOY_API_KEY"}`))
fake := toolsettest.NewFake(t, defs,
	toolsettest.Golden("toy_search", "testdata/golden/search_go.json"),
	toolsettest.Sequence(
		toolsettest.Output("toy_get", map[string]string{"id": "go1", "title": "Go", "content": "Go."}),
		toolsettest.Error("toy_get", tool.ErrToolFailed),
	),
)
```

- **Real definitions.** `Definitions` builds the real connector with a dummy key and a client that refuses every host, so the fake has the real names and schemas.
- **Input checks.** The fake validates every input against the real input schema. Invalid input gives `ErrInvalidInput`, as the real connector would.
- **Output checks.** It validates every scripted output against the real output schema. Output the real connector could never return fails the test.
- **No silent defaults.** A call with no script, or past the end of a `Sequence`, fails the test.
- **Recording.** `Calls()` returns what was called.

Scripts:

| Script | Answers with |
|---|---|
| `Output(tool, v)` | `v`, every call |
| `OutputFunc(tool, fn)` | `fn` of the validated input; return a `*tool.Error` to fail |
| `Error(tool, kind)` | an error of class `tool.ErrInvalidInput` or `tool.ErrToolFailed`, or a context error |
| `Golden(tool, path)` | the JSON in a golden file: what the real connector returned |
| `Sequence(steps...)` | each step once, in turn |

To fake a connector in config-driven tests, put `toolsettest.FakeFactory(t, "<type>", defs, scripts...)` in a `toolset.Catalog` in place of the real factory. `cmd/agentkit` tests set `cli.catalog` to that catalog; production uses `newCatalog()`. For the real connector with no network, `toolsettest.Replay(t, connector())` serves it from its cassettes.

## 7. What the kit cannot show

Fakes and cassettes show the connector works. They cannot show the model uses it well, for example picking the wrong tool or sending arguments the schema rejects. That is the eval tier, [T050](../tasks/T050-connector-tool-evals.md): `make eval` ([evals/README.md](../evals/README.md)). A new connector's tools get a scenario there. The kit's fakes are what make those evals cheap.
