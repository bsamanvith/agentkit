---
id: T026
title: "pkg/toolset: build named toolsets from config"
epic: E03
status: done
depends_on: [T017]
---

# T026: `pkg/toolset`

## Context

A deployment says in config which toolsets to serve and how each is configured; the binary must then build them. Go cannot load packages at runtime, so the split is ([PLAN.md `mcp`](../docs/PLAN.md#mcp)):

- **Build time** decides which connector packages are compiled into the image (all by default; build tags trim heavy ones).
- **Config** decides which toolsets run, under which names, with which settings.

## Design

- `toolset.Factory`: `Type() string` and `Build(ctx, settings json.RawMessage) ([]tool.Tool, error)`. Each connector package (later: `connectors/salesforce`, `connectors/gmail`, ...) provides one. The factory decodes `settings` into its own typed struct and validates it.
- `toolset.Catalog`: an explicit, immutable set of factories, built in `cmd/agentkit` (one small file per build-tagged connector). No global or `init()` registration.
- `toolset.Spec`: `Name` (also the URL path segment), `Type` (which factory), `Settings` (raw, for the factory), `Include`/`Exclude` (tool-name glob filters), `Optional` (a failing optional toolset is skipped and reported instead of failing startup).
- `toolset.Build(ctx, catalog, specs) (map[string]tool.Registry, []Problem, error)`.
  - Two instances of one type with different names are allowed (two Salesforce orgs).
  - Errors name the problem precisely: duplicate name, unknown type (listing the types compiled in), invalid settings, a filter matching no tools.
- A built-in `demo` factory (echo, current time, arithmetic) so examples and smoke tests have a real toolset with no external accounts.
- Config shape (decoded by `pkg/config` later; here only the Go structs and their JSON form):

```yaml
mcp:
  base_path: /mcp
  toolsets:
    - name: salesforce          # served at /mcp/salesforce
      type: salesforce
      settings: { instance_url: https://acme.my.salesforce.com }
    - name: salesforce-sandbox  # second instance of the same type
      type: salesforce
      settings: { instance_url: https://acme--sbx.my.salesforce.com }
    - name: gmail-readonly
      type: gmail
      include: [gmail_search, gmail_read*]
      optional: true
```

## Out of scope

- Real connectors (a later connector epic, after credentials, E07).
- Credentials: `Spec` gets a credentials reference field when E07 lands.

## Acceptance criteria

- [x] Building two specs of different types gives two registries with the right tools. — `TestBuild`
- [x] Two instances of the same type with different settings are independent. — `TestBuildTwoInstances`
- [x] Duplicate names, unknown types (error lists the available types), invalid names and invalid settings are each a clear error. — `TestBuildRejects`
- [x] `Include`/`Exclude` filters select tools; a filter matching nothing is an error. — `TestFilters`
- [x] An `Optional` toolset whose factory fails is skipped and reported as a `Problem`; a required one fails `Build`. — `TestOptional`
- [x] Spec JSON round-trips (the config format is a contract). — `TestSpecJSON` (golden file `testdata/specs.golden.json`)
- [x] The `demo` factory is checked through `Build`. — `TestDemoConformance` (see Notes: `RunRegistry` itself runs in `TestBuildConformance`)

## Notes

- archtest: `pkg/toolset` imports `pkg/tool` only (no new allowlist entry).

### Decisions

- **Spec mistakes always fail `Build`, even for an `Optional` spec**: an invalid or duplicate name, a type not in the catalog, a malformed glob. Only failures while *building* a toolset (factory error, invalid settings, a filter matching nothing, a registry error) are skipped and reported as a `Problem` when `Optional`. Reason: a typo in `type` on an optional toolset would otherwise make it vanish silently. If T003 wants "type not compiled in (build tag) = skip", add an explicit rule then. All specs are validated before any factory runs, so a bad config does no work (`TestBuildRejects`).
- **Filters**: `path.Match` globs, `include` applied first, then `exclude`. Every pattern must match at least one of the factory's tools (typo guard; for `exclude` a typo would silently leave a tool exposed), and at least one tool must remain.
- `Spec` decoding rejects unknown fields (a misspelled `inclde` must not expose every tool). Factories get the same behaviour from `toolset.DecodeSettings`.
- Each factory call gets its own copy of the settings bytes. A context error always fails `Build`, `Optional` or not.
- Errors: sentinels (`ErrInvalidName`, `ErrDuplicateName`, `ErrUnknownType`, `ErrInvalidSettings`, `ErrInvalidFilter`, `ErrDuplicateType`, `ErrInvalidFactory`); `*UnknownTypeError` lists the compiled-in types; `*Error{Toolset, Type, Err}` wraps build failures.
- The toolset name rule is duplicated from `pkg/tool` (and again in `pkg/mcp/server`, T018) to keep these packages independent of each other.
- `demo` lives in `pkg/toolset` (`toolset.Demo(toolset.DemoClock(fn))`). Tools: `echo` (setting `echo_prefix`), `current_time` (setting `utc_offset_minutes`, -720..840), `calculate` (add/sub/mul/div on int64; division by zero and overflow are tool failures).
- `TestDemoConformance` is demo-specific: `tooltest.RunRegistry` expects the echo/fail/block fixtures, which `demo` does not have. `TestBuildConformance` runs the full `RunRegistry` suite on a registry produced by `Build` (from a fixtures factory).
- `FuzzSpec` decodes arbitrary bytes into a `Spec`, checks the re-encode is stable, and runs `Build` on it (no panic).

### For T003 (wiring)

```go
catalog, err := toolset.NewCatalog(
    toolset.Demo(),          // built in
    // salesforce.Factory(), // one small build-tagged file per connector
)
regs, problems, err := toolset.Build(ctx, catalog, cfg.MCP.Toolsets) // cfg decodes into []toolset.Spec
// log each problem; then hand regs to server.New (T018) and mount it at cfg.MCP.BasePath.
```

`Spec` has snake_case JSON tags; `pkg/config` should decode YAML into it with unknown fields rejected.

### Mutation check

Each bug planted alone; each made a test fail (no compile errors).

| Planted bug | Caught by |
|---|---|
| duplicate names allowed | `TestBuildRejects/duplicate_name` |
| unknown type skipped | `TestBuildRejects` (`unknown type`, `spec mistakes fail even when optional`) |
| instances share settings | `TestBuildTwoInstances`, `TestOptional` |
| `Include` ignored | `TestFilters`, `ExampleBuild` |
| `Exclude` ignored | `TestFilters` |
| empty filter match accepted | `TestFilters/exclude_matches_nothing`, `.../one_of_several_includes_matches_nothing` |
| optional failure fails the build | `TestOptional` |
| required failure skipped | `TestOptional/required_failure_fails_the_build`, `TestBuildRejects` (settings cases) |
| unknown settings fields accepted | `TestBuildRejects/unknown_settings_field`, `TestOptional` |
| invalid name accepted | `TestBuildRejects` (name cases) |
