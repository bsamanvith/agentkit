---
name: design-module
description: Shape a new agentkit Go package before writing it, or check an existing one's shape. Use when a task adds a package under pkg/ or connectors/, adds an interface or a second implementation of one, adds a deployable component or a remote client, or brings in a heavy dependency. Produces a short design note plus doc.go and the interface file as the first commit.
---

# design-module

**Goal: a package whose place in the layers, interface, errors and test package are decided before the first implementation, so dependency direction and wiring are right from the start.** The rules are in [ENGINEERING §1](../../../docs/ENGINEERING.md#1-module-design); this skill is the order to apply them in. It links them; it does not restate them.

Worked examples: `pkg/tool` (T017, the first package built this way: `tool.go`, `registry.go`, `tooltest/`, the remote `toolrpc/`), `pkg/llm` with `anthropic/` and `llmtest/` (T020's Design section), and `pkg/store` with `memory/`, `sqlite/`, `postgres/`, `storetest/` and the picker `storeopen/`.

## Steps

1. **Place it in the layers** ([§1.4](../../../docs/ENGINEERING.md#14-dependency-direction)). Say which layer (foundation, core interface, adapter, `cmd`), and list what it may import and what must never import it. Check that against `internal/archtest/rules.go`: a third-party import needs a line in `ThirdParty`; a package whose module imports must stay narrow (as `pkg/store` and `pkg/runner` do) gets a `ModuleImports` entry. Arrows only point down: if the new package needs something from above, the interface belongs lower, not the import higher.

2. **Define the smallest interface callers need** ([§1.3](../../../docs/ENGINEERING.md#13-design-rules)). Write it from the caller's side: what does the harness, runner or API call? One to four methods; `ctx` first on anything that does IO or blocks; accept interfaces, return concrete types; `New(required..., opts ...Option) (*T, error)` that validates. List the sentinel errors (`ErrNotFound`, `ErrInvalidInput`, ...) and which ones a caller is expected to branch on. Time and randomness come in as `clock.Clock` and an `io.Reader`. Decide ownership: what a caller gets back is a copy or immutable, what it passes in is copied if kept, and only the constructor can make a value (unexported fields, or the constructor validates what it is handed), so an invalid value cannot exist. Write that in the doc comments.

3. **Lay out the files and subpackages** ([§1.2](../../../docs/ENGINEERING.md#12-standard-package-shape)): interface and types in the parent, each implementation in its own subpackage with its own dependencies, and `<name>test` with a fake and, when there are or will be two implementations, a conformance suite ([§2.4](../../../docs/ENGINEERING.md#24-conformance-suites-the-main-anti-duplication-tool)). The parent never imports its children. No `init()`, no package-level state, no global registry.

4. **If it is a seam or deployable** ([§1.5](../../../docs/ENGINEERING.md#15-components-mix-and-match-deployment)): name the `component.Component` that serves it, what it depends on, and for each dependency the local value and the remote client (`pkg/rpc`, [COMPONENTS.md](../../../docs/COMPONENTS.md#adding-a-method-or-a-service)) that implements the same interface and runs the same suite. A seam with several implementations gets one picker and a row in [COMPONENTS.md](../../../docs/COMPONENTS.md#plugging-in-another-implementation), never a switch elsewhere ([AGENTS.md](../../../AGENTS.md#seams)). An optional backend goes behind a build tag with a `_none.go` twin. Even when it is not a seam, say which process of the split deployment (API, runner, toolsets) holds each piece of its state, and how another process that reports or uses it (`/readyz`, the API) gets it; if it needs a new remote method, that is part of the design.

5. **Isolate heavy dependencies** ([§1.1](../../../docs/ENGINEERING.md#11-one-go-module-many-packages)). Prefer the standard library or a thin client over an SDK. If a dependency is still needed, keep it in the subpackage that uses it, and measure the binary with and without it ([AGENTS.md Sizes](../../../AGENTS.md#sizes)). Check it per image, not only for the default binary: `make print-tags-<variant>` lists each image's tags, and `go list -deps -tags '<tags>' ./cmd/agentkit` shows whether that image already links the dependency. An image that does not gets either a build-tagged wiring file (`_none.go` twin) or a measured row. Say what changes in `go.mod` (a new requirement, or one moving from `// indirect` to direct) so `make check`'s tidy step is expected.

6. **Write it down, then commit the skeleton.** The design note goes in the task file's Notes (or `docs/design/<pkg>.md` if it is longer than a screen): layer and imports, the interface, errors, subpackages, component wiring, dependencies and their cost, and anything left open. Stubs in the skeleton only return a not-implemented error; they touch no field, so even the zero value cannot panic. The first commit is `doc.go` (what and why in one paragraph, which interface to use, the error classes) and the interface file, with an `example_test.go` stub if the API is already clear (else list it as owed). Every behavior the design adds (a config field, an error a caller sees, a limit, what `/readyz` reports) needs an acceptance criterion in the task: add the missing ones and decide the open questions that change them. `go build ./...`, `go test -count=1 ./internal/archtest/` and `make lint` must pass on the skeleton (no unused fields).

## Done when

- The note answers: which layer, what it imports, the interface, its errors and who owns what, the subpackages and `xxxtest`, local and remote wiring, heavy dependencies.
- Every behavior in the design has an acceptance criterion in the task.
- `doc.go` and the interface compile, and archtest passes with any new rule lines explained in a comment.
- A `review` of the skeleton finds nothing under boundaries or package shape.
