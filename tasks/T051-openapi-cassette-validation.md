---
id: T051
title: "toolsettest: validate cassettes against a vendored OpenAPI spec"
epic: E10
status: todo
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

- [ ] **Errors are reported:** a cassette whose response violates its operation's schema fails with cassette name, path and rule (toy spec and toy cassettes); a conforming one passes.
- [ ] **Spec features:** `$ref`, `nullable` and `allOf` are resolved (table test with toy specs).
- [ ] **Unknown operations:** a cassette whose method and path are not in the spec fails clearly; it is not skipped.
- [ ] **Connectors moved:** Exa and Parallel use the helper through `Connector.OpenAPI`, and their one-off tests are removed.
- [ ] **Mutation list** recorded (at least: `$ref` not followed, status ignored, `nullable` treated as required).
