---
id: T026
title: "pkg/toolset: build named toolsets from config"
epic: E03
status: todo
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

- [ ] Building two specs of different types gives two registries with the right tools. — `TestBuild`
- [ ] Two instances of the same type with different settings are independent. — `TestBuildTwoInstances`
- [ ] Duplicate names, unknown types (error lists the available types), invalid names and invalid settings are each a clear error. — `TestBuildRejects`
- [ ] `Include`/`Exclude` filters select tools; a filter matching nothing is an error. — `TestFilters`
- [ ] An `Optional` toolset whose factory fails is skipped and reported as a `Problem`; a required one fails `Build`. — `TestOptional`
- [ ] Spec JSON round-trips (the config format is a contract). — golden file
- [ ] The `demo` factory passes `tooltest.RunRegistry` through `Build`. — `TestDemoConformance`

## Notes

- archtest: `pkg/toolset` imports `pkg/tool` only.
