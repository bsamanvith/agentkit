---
id: T051
title: "toolsettest: validate cassettes against a vendored OpenAPI spec"
epic: E10
status: done
depends_on: [T049, T047, T048]
---

# T051: OpenAPI validation of cassettes

## Context

T049 §6 (drift): where a provider publishes an OpenAPI spec, every cassette response should be checked offline against that spec's response schema. This matters most for `synthetic` cassettes, which agents wrote by hand and which Nango's experience says repeat the code's own assumptions. T049 left the shared helper out (handoff 2026-10-12). For now, T047 (Exa) and T048 (Parallel) each vendor their spec with their own offline test. This task turns that into one helper and moves them onto it.

## Scope

- **`toolsettest.ValidateCassettes(t, cassettesDir, specPath)`**: for each cassette, find the operation by method and path in a vendored spec (JSON or YAML, OpenAPI 3.0 or 3.1), then validate the response body against that operation's schema for the response status.
  - Handle `$ref`, `nullable` (3.0), `oneOf`/`anyOf`/`allOf`, and `additionalProperties`.
  - Report the cassette, the JSON path and the rule that failed.
- **Vendoring:** only the operations a connector uses go into `testdata/openapi.json`. Add a `make` target, or a documented `jq` line, that re-extracts them from the provider's published spec.
- **Wire it in:**
  - add an optional `Connector.OpenAPI` path, under check ID `cassettes.openapi` (opt-out once three connectors pass);
  - move Exa and Parallel onto it and delete their one-off tests;
  - Tavily if its docs' schema can be extracted.
- **Dependency:** reuse `google/jsonschema-go` if it handles the OpenAPI schema dialect after a small translation (3.0 `nullable`). Otherwise propose a dependency to the planner before adding one.

## Acceptance criteria

- [x] **Errors are reported:** a cassette whose response violates its operation's schema fails with cassette name, path and rule (toy spec and toy cassettes); a conforming one passes. `TestValidateCassettes` (toy spec `pkg/toolset/toolsettest/testdata/openapi.json`, toy cassettes): `conforming`, `conforming_400_and_default`, `wrong_type_behind_$ref` (`cassette x: response 200 at $.hits[*].link: type: 7 has type "integer", want "string"`), `missing_field`, `undeclared_field`, `request_body`, `response_not_JSON`. The toy itself passes the check: `TestToyConnector/cassettes/openapi`.
- [x] **Spec features:** `$ref`, `nullable` and `allOf` are resolved (table test with toy specs). `TestValidateCassettes/wrong_type_behind_$ref` and `/allOf_part_not_met`; `TestValidateCassettesOpenAPI30` (`nullable` null, absent, valued, with an enum, and null where not nullable; boolean `exclusiveMinimum`; `oneOf` fitting one, none and two); `additionalProperties: false` in `/undeclared_field`. A spec the check would misread fails: `TestValidateCassettesRefusesSpecs` (unknown keyword, `nullable` in 3.1, `nullable` without a type, external and dangling `$ref`, Swagger 2, YAML).
- [x] **Unknown operations:** a cassette whose method and path are not in the spec fails clearly; it is not skipped. `TestValidateCassettes/unknown_path`, `/unknown_method` (`DELETE /v1/items/go1 is not an operation in testdata/openapi.json`), and a status the spec lacks, `/status_not_in_the_spec`. Running the check without a spec fails too: `TestOpenAPICheckNeedsTheSpec`.
- [x] **Connectors moved:** Exa and Parallel use the helper through `Connector.OpenAPI`, and their one-off tests are removed. Both run `cassettes.openapi` (`TestConformance/cassettes/openapi` passes), and so does Tavily. Removed: Exa's `TestCassettesMatchSpec`, `TestSpecCheckerRejects` and its `specChecker`, and `testdata/openapi/` (cut.go, README, exa.json); Parallel's `spec_test.go` (`TestCassettesMatchSpec`, `TestSpecCheckRejects`, its loader). Kept, on the kit: Exa's `TestEnumsMatchSpec` and Parallel's rig invariant that every request it sends fits the request schema, both through `LoadOpenAPI` and `CheckRequest`.
- [x] **Mutation list** recorded (at least: `$ref` not followed, status ignored, `nullable` treated as required). 18 mutants, all killed: table in Notes.

## Notes

### As built

```go
func ValidateCassettes(tb testing.TB, dir, specPath string)
func LoadOpenAPI(file string) (*OpenAPI, error)
func (s *OpenAPI) CheckRequest(method, path string, body []byte) error

type Connector struct {
    // ...
    OpenAPI string // vendored spec (testdata/openapi.json); check cassettes.openapi
}
```

- **Check `cassettes.openapi`, opt-out,** subtest `cassettes/openapi`. It landed opt-in, enabled by Exa, Parallel and the toy, and went opt-out once Tavily passed with its overlay (below). It runs `ValidateCassettes` on every cassette in the directory, not only those the calls use, so Tavily's 400 and 401 cassettes are checked too.
- **Matching.** Operation by method and path; a `{param}` segment matches one segment. The response schema is the exact status, else `4XX`, else `default`. A missing operation or status is an error. The request body is checked too, as both one-off tests did.
- **Dialect.** The spec is translated to JSON Schema 2020-12 and validated by `google/jsonschema-go` (no new dependency):
  - `#/components/schemas/X` becomes `#/$defs/X`; any other `$ref` fails the spec. Each operation's request and response schemas are resolved once, with only the components they reach as `$defs` (with all of them, loading Exa's spec took 3.4 s under `-race`; now 0.3 s).
  - OpenAPI 3.0: `nullable: true` adds `"null"` to `type` and to `enum`; boolean `exclusiveMinimum`/`exclusiveMaximum` become 2020-12's numeric form.
  - OpenAPI annotations (`example`, `discriminator`, `xml`, `externalDocs`, `x-*`) are dropped. Any keyword not on the allowlist fails the spec, as Exa's checker did, so a re-vendored spec cannot pass by being misread.
  - `oneOf: X` becomes `allOf: [{anyOf: X}, {oneOf: X}]`. Same meaning, but jsonschema-go explains a failed `anyOf` alternative by alternative and a failed `oneOf` not at all. Exa's whole `SearchResponse` is a `oneOf`, so without this every Exa failure read "did not validate against any of [<anonymous schema> ...]".
- **Error text.** jsonschema-go reports schema locations, not instance paths. The JSON path is rebuilt from its chain of `validating <location>` steps: `properties/<k>` is `.k`, `items` is `[*]` (the element index is not known), and a `$ref` is a jump. Validation stops at a body's first violation.
- **Vendoring: `make openapi-specs`.** It fetches the three published specs and cuts each with `pkg/toolset/toolsettest/testdata/openapi/cut.go`. The cut keeps the named operations (request body, responses, JSON content's schema; `$ref`ed request bodies and responses are inlined) and every component schema they reference, verbatim, with a `_comment` naming the source and date.
  - The cutter reads YAML, JSON, or the ```` ```yaml ```` blocks of Tavily's Markdown pages, merging documents.
  - It is `//go:build ignore` in a `testdata` directory, like Exa's old cut.go, so `go.yaml.in/yaml/v4` stays an indirect dependency and go.mod is unchanged. For the same reason `LoadOpenAPI` reads JSON only (the scope said JSON or YAML): every vendored spec is cut to JSON anyway.
  - Re-cutting gave Exa's and Parallel's previous schemas exactly. Exa's file moved to `testdata/openapi.json` in real OpenAPI shape; its old cut had rewritten operations to `{request, response200}`. It now also has the error responses, with `ErrorResponse` and `X402PaymentChallenge`.

### What the specs found

- **Exa and Parallel: no synthetic cassette is wrong.** All 7 of Exa's and 4 of Parallel's pass their request and response schemas. Corrupting one of each fails the check (`search-deep` with a numeric `url`; `extract-full` without `extract_id`). `format` is not asserted, unlike Exa's old checker (`date-time`, `date`, `uri`). By hand, every Exa `publishedDate` is RFC 3339, and every Parallel `publish_date` is a date. One Parallel `url` is `""` on purpose, to test that results without a URL are dropped; the spec puts no format on it.
- **Tavily: the published spec is wrong, so the cut corrects it.** The spec is extracted from the docs pages' OpenAPI 3.0.3 blocks and vendored (`connectors/tavily/testdata/openapi.json`), and `Connector.OpenAPI` points at it. The real, recorded cassettes contradict it:
  - `answer` is `required` and `type: string`; `search-go-release` (no `include_answer`) has `"answer": null`.
  - `results[].raw_content` is `type: string`; both search cassettes have `null` (no `include_raw_content`).
  - `results[].published_date` is `type: string`, though its own description says it is null when no date is found; both search cassettes have `null`s.
  - Everything else fits, the extract cassettes and the 400 and 401 bodies included. Undeclared `follow_up_questions` passes, since the spec does not close its objects.

  The options were: (a) leave Tavily off until Tavily fixes its docs; (b) let the cutter apply a reviewed overlay; (c) a `Skip` reason once the check is opt-out. The planner chose (b):
  - `connectors/tavily/testdata/openapi-overlay.json` holds three corrections, each `nullable: true`. Each gives a JSON Pointer into the cut and a recorded cassette whose response shows the null (`search-go-release` for `answer` and `raw_content`, `search-news-answer` for `published_date`).
  - `cut.go -overlay` applies them in `make openapi-specs` and marks each corrected schema `x-agentkit-overlay`, which the check ignores. The vendored spec is still never edited by hand.
  - The cut fails when a correction cannot be backed or is stale: a synthetic cassette, a response that does not contain `shows`, a pointer not in the cut, or a correction that changes nothing (Tavily fixed its docs; then delete it). The last two were tried by hand: re-cutting the corrected spec, and a `shows` the cassette lacks.
  - With the overlay, `TestConformance/cassettes/openapi` passes for Tavily. With the spec as published, it fails on `search-go-release` (`$.results[*].raw_content`) and `search-news-answer` (`$.results[*].published_date`).
  - Every connector then passed, so `cassettes.openapi` went opt-out. Exa, Parallel and the toy dropped it from `Enable`; `TestSkipsAreReported` now covers it with the opt-out checks.
- **For T047 and T048:** their task files named the removed `TestCassettesMatchSpec` and `TestSpecCheckRejects`. They now say what replaced them.

### Mutation list

Each mutation was planted in `openapi.go` (one in `connector.go`), the tests run, and the code restored. All are caught:

| Mutation | Caught by |
|---|---|
| `$ref` not followed (dropped) | `TestValidateCassettes/wrong_type_behind_$ref`, `/allOf_part_not_met`, `/status_picks_the_schema`, `TestCheckRequest`, … |
| status ignored (always the 200 schema) | `TestValidateCassettes/status_picks_the_schema`, `/default_picks_the_schema`, `/status_not_in_the_spec`, `/conforming_400_and_default` |
| no `4XX`/`default` fallback | `TestValidateCassettes/default_picks_the_schema`, `/conforming_400_and_default` |
| `nullable` treated as required | `TestValidateCassettesOpenAPI30/nullable_is_absent` |
| `nullable` ignored (null refused) | `TestValidateCassettesOpenAPI30/nullable_is_null` |
| boolean `exclusiveMinimum` ignored | `TestValidateCassettesOpenAPI30/exclusive_minimum` |
| `allOf` silently dropped | `TestValidateCassettes/allOf_part_not_met`, `/wrong_type_behind_$ref` |
| `additionalProperties` silently dropped | `TestValidateCassettes/undeclared_field`, `TestCheckRequest`, Exa's `TestConformance/cassettes/openapi` |
| `oneOf` left as is (no reasons) | `TestValidateCassettesOpenAPI30/oneOf_fits_none` |
| `oneOf` turned into a bare `anyOf` | `TestValidateCassettesOpenAPI30/oneOf_fits_two` |
| unknown keyword ignored | `TestValidateCassettesRefusesSpecs/unknown_keyword`, `/nullable_in_3.1` |
| unknown operation skipped | `TestValidateCassettes/unknown_path`, `/unknown_method` |
| status missing from the spec skipped | `TestValidateCassettes/status_not_in_the_spec` |
| request body not checked | `TestValidateCassettes/request_body`, `TestCheckRequest` |
| path templates not matched | `TestValidateCassettes/conforming`, `TestToyConnector/cassettes/openapi`, `TestCheckRequest` |
| JSON path loses array steps | `TestValidateCassettes/wrong_type_behind_$ref`, `/allOf_part_not_met`, `TestValidateCassettesOpenAPI30/not_nullable` |
| check not wired into `RunConnector` | `TestSkipsAreReported` (`TestToyConnectorSkips/cassettes/openapi` not reported) |
| check passes without `Connector.OpenAPI` | `TestOpenAPICheckNeedsTheSpec` |
