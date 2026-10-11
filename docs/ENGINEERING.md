# agentkit: Engineering Plan

> Status: proposal. How we design modules, how we test them, how we run the system locally, and the agent skills that encode all of it.
> The work to put this in place is tracked in [E01 foundations](../tasks/E01-foundations.md) and [E02 agent skills](../tasks/E02-agent-skills.md).

This complements [PLAN.md](PLAN.md) (what we build) with how we build it.

---

## 0. Build order

Build the product from the inside out. Add infrastructure when there is something real for it to run or guide, not before.

| Milestone | What | Epic | Proves |
|---|---|---|---|
| **M0** | `make check`, lint, architecture test, test helpers, minimal CI | [E01](../tasks/E01-foundations.md) (T001, T002) | Every later change is checked the same way |
| **M1** | `pkg/tool`: definition, `tool.Func[In, Out]`, registry, middleware, conformance suite | [E03](../tasks/E03-tool-core-and-mcp.md) (T017) | The core abstraction and all conventions on real code |
| **M2** | MCP server over stdio for any registry | E03 (T018) | Claude Code / Codex can call our Go tools: the first real end-to-end test. Independent of M3; can run in parallel |
| **M3** | `llm` + Anthropic adapter + harness + `pkg/flow` | [E04](../tasks/E04-llm-harness-flow.md) (T020-T025) | An agent loop and a multi-step flow run, with checkpoints and resume. **Current focus** |
| **M4** | `store` (SQLite) | to plan | Runs persist and resume |
| **M5** | `pkg/component`, `cmd/agentkit`, `fakellm`, image, Compose, smoke tests, full CI | E01 (T003-T008) | Mix-and-match deployment |
| **M6** | Tool protocol over ConnectRPC; remote registry | to plan | The `split` topology |

Skills ([E02](../tasks/E02-agent-skills.md)) grow alongside: the skill layout and review checklist come with M0/M1; each other skill is written right after the first piece of real code it describes.

---

## 1. Module design

### 1.1 One Go module, many packages

- A single `go.mod` at the root. What keeps packages independent is **package boundaries**, not separate modules.
- What a single module does **not** cost:
  - Binary size: the linker only includes packages that are actually imported. A binary that never imports `credentials/vault` contains no Vault code.
  - Build time: only imported packages are compiled.
- What it **does** cost, and how we handle it:
  - Everyone who imports any agentkit package gets our whole `go.mod` in their dependency graph. Go picks the highest required version of each shared dependency, so a heavy backend (AWS SDK, Kubernetes client) can raise the minimum version of something the user also depends on (gRPC, OpenTelemetry, protobuf).
  - Mitigations: prefer the standard library and thin clients (for example, call the Kubernetes Secrets REST API with `net/http` instead of pulling in `client-go`); keep each heavy backend in its own subpackage so it can be moved out cleanly.
  - If one backend still becomes a problem, give that subpackage its own nested `go.mod` (for example `pkg/credentials/aws/go.mod`). The import path stays the same, so this does not break users.
  - All packages share one version number. That is fine before 1.0.

### 1.2 Standard package shape

Every package under `pkg/` follows the same shape:

```
pkg/store/
  doc.go            package docs + the one-paragraph "what and why"
  store.go          the interface(s), shared types, sentinel errors
  options.go        functional options (if any)
  example_test.go   runnable Example: documentation that is also a test
  sqlite/           an implementation (its own deps stay here)
  postgres/         another implementation
  storetest/        in-memory fake + conformance suite (see 2.4)
```

- **Interfaces in the parent, implementations in subpackages.** Importing `pkg/credentials` gives you the interface and types; importing `pkg/credentials/vault` pulls in Vault. Heavy dependencies never leak upward.
- **`xxxtest` package for every interface** (like `net/http/httptest`, `testing/fstest`): a fake for consumers to use and a conformance suite for implementers to run.
- **`internal/`** for helpers shared across packages that are not public API.

### 1.3 Design rules

| Rule | Why |
|---|---|
| Accept interfaces, return concrete types. Keep interfaces small (1-4 methods). | Easy to fake, easy to implement. |
| `New(required..., opts ...Option) (*T, error)`; validate in the constructor. | Invalid objects never exist; no `Init()` step. |
| No `init()`, no package-level mutable state, no global registries. Registries are values, built once and read-only afterwards. | Parallel tests, multi-tenant safety, no locks. |
| Optional backends behind build tags are registered in `cmd/agentkit` (one small file per tag), not by `init()` in the backend. Config naming a backend that was not compiled in fails at startup with a clear error. | Keeps the no-globals rule with build tags. |
| `ctx context.Context` is the first parameter of anything that does IO or blocks. | Cancellation, deadlines, tenant scope. |
| Exported sentinel errors / typed errors, wrapped with `%w`. Callers use `errors.Is` / `errors.As`. | Tests and callers branch on error identity, never message text. |
| Inject time and randomness (`clock.Clock`, `io.Reader` for rand) where behavior depends on them. | Token refresh, rate limits, budgets and retries become deterministic to test. |
| Whoever starts a goroutine owns stopping it. Long-lived types have `Close()` or `Shutdown(ctx)`. | No leaks, clean shutdown. |
| Server-capable packages expose `http.Handler` / gRPC service registration, never `ListenAndServe`. Only `cmd/` owns listeners, flags, and env. | Libraries stay embeddable; tests use `httptest`. |
| Every public package has at least one runnable `Example`. | Docs that cannot rot. |

### 1.4 Dependency direction

```
cmd/*  ──wires──▶  adapters (llm/anthropic, store/postgres, credentials/vault, mcp/client ...)
                          │
                          ▼
                core interfaces (harness, llm, tool, store, credentials, tenancy)
                          │
                          ▼
           foundations (transport, observability, config, clock, internal/*)
```

- Arrows only point down. `harness` depends on the `llm.Model` interface, never on `llm/anthropic`.
- Enforced by an **architecture test** (`internal/archtest`), which runs with the unit tests and reads the import graph from `go list`:
  1. Nothing in `pkg/` or `internal/` imports `cmd/`.
  2. A package never imports its own subpackages (the parent is the interface; children are implementations).
  3. Non-test code never imports test helpers (`*test` packages, `internal/testkit`).
  4. **Third-party imports are allowlisted per package.** Adding a dependency to a package means adding one line to the table in `archtest`, which makes every new dependency a visible, reviewed decision.
  5. Mocking frameworks are banned everywhere, tests included.

### 1.5 Components: mix-and-match deployment

Goal: any set of packages can run in one container, or be spread across containers that talk to each other, **without code changes**.

**One binary, components chosen by config.** Each deployable piece (`harness`, `tools`, `mcp-server`, ...) is built by an ordinary typed constructor and then handed to the runtime in `pkg/component`, which only needs this:

```go
type Component interface {
    Name() string
    Handler() http.Handler            // mounted under the shared server (ConnectRPC, MCP over HTTP, ...)
    Ready(ctx context.Context) error  // feeds /readyz
    Shutdown(ctx context.Context) error
}
```

The wiring is **explicit Go code in `cmd/agentkit`**, not a generic dependency bag. For each dependency there is one `switch` on the config:

```go
var tools tool.Registry
switch cfg.Harness.Tools.Mode {
case "local":
    tools = localRegistry               // built in this process
case "remote":
    tools = toolclient.New(cfg.Harness.Tools.URL)
}
h, err := harness.New(model, tools, store)
```

Wrong wiring is a compile error, not a runtime type assertion.

**Every dependency between components is an interface, and config picks local or remote:**

```yaml
# one container
components: [harness, tools, mcp-server]
harness:
  tools: { mode: local }

# two containers, same image
# container A
components: [harness]
harness:
  tools: { mode: remote, url: http://tools:8081 }
# container B
components: [tools, mcp-server]
```

- `local`: the harness gets the in-process `tool.Registry`. No network, no serialization.
- `remote`: the harness gets a ConnectRPC client that implements the same `tool.Registry` interface.
- Each remote client is tested with the **same conformance suite** as the local implementation (§2.4), so swapping one for the other is safe by construction.

**Communication between containers:**

| Path | Protocol | Why |
|---|---|---|
| Component to component (ours) | ConnectRPC (gRPC protocol over HTTP/2) | Typed, streaming, one definition for Go and non-Go clients |
| To and from external agents and tools | MCP (stdio, Streamable HTTP) | Spec-compliant interop |
| Browser / curl | ConnectRPC JSON over HTTP/1.1 | No gRPC tooling needed |

We use only `connect-go`: one handler on a standard `net/http` server serves the gRPC, gRPC-Web and Connect protocols. No `grpc-go`, no second port.

- Plain h2c locally; mTLS between containers in real deployments, with certificates from `pkg/credentials`.
- Tenant and request scope travel in request metadata and are restored into `context.Context` by middleware on the receiving side.
- Addresses come from config (compose service names locally, Kubernetes Services in a cluster). No service-discovery system in v1.

**Images:**

- The default image is `agentkit` (everything in the core build) plus `fakellm` for dev and tests. Deployment shape is config, not image choice.
- Heavy optional backends sit behind build tags (for example `-tags aws,vault`) so the default image stays small. `make image TAGS=...` builds a variant.
- Size budget: measure the first real image (M5), then set the limit in CI. The pure-Go SQLite driver alone is several MB, so the earlier 10-20 MB target is a goal, not a promise.
- If you need a truly custom bundle, write a 20-line `main` that imports the components you want. They are ordinary packages.

---

## 2. Testing

### 2.1 Principle: test behavior through the public API

A test should fail only when a **feature** breaks, not when code is refactored.

- Tests live in `package foo_test` (black box) by default. White-box `package foo` tests are the exception and need a reason (e.g. a tricky internal algorithm).
- Assert on **observable outcomes**: return values, stored state, events emitted, bytes on the wire. Never assert that an internal method was called.
- **No mocking frameworks.** Use hand-written fakes from `xxxtest` packages. Fakes must pass the same conformance suite as real implementations, so they cannot drift from reality.

### 2.2 Test tiers

| Tier | What | Where | Run with | Needs | Budget |
|---|---|---|---|---|---|
| **Unit** | Pure logic, fakes, `httptest` servers | next to code | `make test` | nothing | whole repo < 30 s |
| **Integration** | Real backends: Postgres, Vault, LocalStack, real MCP servers | next to code, `//go:build integration` | `make test-integration` | `scripts/dev.sh up deps` | < 3 min |
| **E2E / smoke** | Real binaries in containers, black-box over HTTP/gRPC/MCP | `test/e2e`, `//go:build e2e` | `scripts/dev.sh smoke <topology>` | running topology | < 2 min per topology |
| **Live** | Real model providers (Anthropic, OpenAI-compatible) | next to adapter, `//go:build live` | `make test-live` | API keys | manual / nightly |

- Integration tests read connection strings from env (`AGENTKIT_PG_URL`, `AGENTKIT_VAULT_ADDR`, ...) and **skip** if unset, so a laptop without Docker still runs them cleanly. In CI `AGENTKIT_REQUIRE_INTEGRATION=1` turns those skips into failures, so nothing silently stops running.
- We do not use testcontainers; `scripts/dev.sh` already provides the dependencies, and it keeps the dependency list small.

### 2.3 How many tests: one per behavior, not per function

Each task file lists **acceptance criteria**. Each criterion maps to (roughly) one test. That is the test budget.

Write a test for:
- each acceptance criterion (the happy path of the feature);
- each error a caller is expected to handle (`ErrNotFound`, `ErrBudgetExceeded`, ...);
- boundaries that have bitten us or obviously will (empty input, cancellation mid-stream, concurrent access);
- every bug fix (regression test first, then the fix).

Do **not** write tests for:
- getters, setters, constructors with no logic, option plumbing;
- generated code (protobuf, ConnectRPC);
- wiring in `cmd/` that the e2e smoke suite already covers;
- the same behavior again at a higher tier. If a unit test proves it, the e2e test does not re-prove it.

Coverage is a signal we look at, not a target we chase.

### 2.4 Conformance suites (the main anti-duplication tool)

When an interface has several implementations (Store: SQLite, Postgres, in-memory fake; credential backends: file, Vault, AWS, k8s; MCP transports: stdio, HTTP), we write the behavior tests **once**:

```go
// pkg/store/storetest/suite.go
func Run(t *testing.T, newStore func(t *testing.T) store.Store) {
    t.Run("CreateAndGetRun", ...)
    t.Run("GetMissingRunReturnsErrNotFound", ...)
    t.Run("TenantIsolation", ...)
}

// pkg/store/sqlite/sqlite_test.go
func TestConformance(t *testing.T) { storetest.Run(t, newTestSQLite) }
```

New implementation = one line of test code + passing the suite.

### 2.5 Rules that keep tests from being flaky or brittle

- **No `time.Sleep`.** Use `clocktest.Fake`, channels, or poll-with-deadline helpers.
- **No assertions on log lines or error message strings.** Use `errors.Is` / `errors.As`.
- **Table-driven** when the same behavior varies by input; separate tests when behavior differs.
- **Golden files** only for wire formats (MCP JSON-RPC, tool protocol JSON, provider request bodies). Stored in `testdata/`, regenerated with `go test ./... -update`, reviewed in the diff.
- **Recorded fixtures for LLM providers**: an `httptest` server replays `testdata/*.jsonl` responses. Unit tests never touch the network.
- **Fuzz** anything that takes untrusted input (tool input, JSON-RPC decoding, JWT parsing). The seed corpus runs with the unit tests; `make fuzz` runs each target for a short time and CI runs it on every PR.
- Every test calls `t.Parallel()` unless it has a documented reason not to.
- Always run with `-race -shuffle=on`. CI runs `-count=1`.

---

## 3. Local environments ("pods")

### 3.1 Approach

- **Docker Compose with profiles** is the default local runtime: fast, one file, no cluster needed.
- **kind** (Kubernetes in Docker) is optional and only for things that need real Kubernetes (the k8s secrets backend, manifests, probes). Same images, same config. It is built with the credentials work, the first thing that needs it.
- One `Dockerfile` producing distroless images: `agentkit` and `fakellm`. Topologies differ by config, not by image.
- **`fakellm`**: a small scripted model server speaking the Anthropic Messages API (subset). Every topology uses it by default, so local runs and e2e tests are deterministic and need no API key. Set `AGENTKIT_LLM=anthropic` plus a key to use the real provider.

### 3.2 Topologies

| Topology | Containers | Use it for |
|---|---|---|
| `allinone` (default) | `agentkit` (all components, SQLite), `fakellm` | Day-to-day dev, fastest loop |
| `split` | `harness` and `tools` (both the `agentkit` image, different config), `postgres`, `fakellm` | Remote tools over gRPC, Postgres, multi-tenancy |
| `mcp` | `split` + fixture MCP server(s) | MCP client/server/bridge |
| `secrets` | `allinone` + `vault` + `localstack` | Credential backends |
| `obs` | add-on: `otel-collector` + `jaeger` | Traces and metrics; combine with any topology |
| `deps` | `postgres`, `vault`, `localstack` only | Running integration tests from the host |

Fixed host ports (documented in `deploy/local/README.md`) so scripts and tests never guess.

### 3.3 The script

One entry point, `scripts/dev.sh`:

```
scripts/dev.sh up [topology...]     build images, start, block until healthy
scripts/dev.sh down [-v]            stop (and wipe volumes)
scripts/dev.sh status               containers, health, URLs
scripts/dev.sh logs [service]       follow logs
scripts/dev.sh smoke [topology]     run the e2e suite against what is running
scripts/dev.sh reset                down -v, then up the same topology
scripts/dev.sh run [config]         go run agentkit on the host (no Docker), SQLite in .data/
```

- `up` waits on container healthchecks (`/readyz`), so when it returns, the stack is usable.
- `run` is the fastest inner loop: `go run ./cmd/agentkit` with the `allinone` config, no image build at all.
- `scripts/kind.sh up|down|load|apply <topology>` mirrors this for Kubernetes, using kustomize overlays in `deploy/k8s/overlays/<topology>`.
- `Makefile` holds build, lint, and test targets and delegates environment work to the scripts.

### 3.4 Layout

```
Dockerfile
Makefile
.golangci.yml
scripts/
  dev.sh
  kind.sh
deploy/
  local/
    compose.yaml          all services, gated by profiles
    config/*.yaml         agentkit config per topology and per container
    fakellm/*.jsonl       default scripts for fakellm
    README.md             ports, topologies, env vars
  k8s/
    base/
    overlays/<topology>/
cmd/
  agentkit/               the binary: parse config, start components
  fakellm/                scripted model server (dev/test only)
test/
  e2e/                    black-box smoke tests (//go:build e2e)
internal/
  testkit/                golden files, env gating, polling helpers
```

---

## 4. Agent skills

We keep coding-agent skills in the repo and improve them like code. They turn this document into steps an agent (or a person) can follow.

### 4.1 Where they live

- **`.agents/skills/<name>/SKILL.md`** is the single source. This is the open Agent Skills format (`name` + `description` front matter, Markdown body, optional `scripts/` and `references/`), read by Codex and other agents.
- **`.claude/skills`** is a symlink to `.agents/skills`, so Claude Code loads the same files.
- **`AGENTS.md`** at the root is the short repo guide every agent reads (commands, layout, links to these docs). **`CLAUDE.md`** just imports it (`@AGENTS.md`), so there is one copy.

### 4.2 The set

| Skill | Use it to | Built on |
|---|---|---|
| `plan` | Turn an idea into an epic and task files with testable acceptance criteria | §5, `tasks/README.md` |
| `design-module` | Shape a new package: interfaces, subpackages, `xxxtest`, component wiring | §1 |
| `write-tests` | Pick the tier, write one test per criterion, add or reuse a conformance suite | §2 |
| `implement-task` | Take a task from `todo` to `review`: branch, code, tests, `make check`, update the task file | §1, §2, §5 |
| `review` | Review a diff against our rules: boundaries, brittle tests, test budget, errors, ctx, goroutines | §1, §2 |
| `local-env` | Bring up a topology, run smoke tests, read logs | §3 |

### 4.3 Rules for skills

- **Point to the docs; don't copy them.** A skill says *what to do, in what order*, and links the section of this file that says *why*. Rules live in one place.
- **Short.** A skill body should fit on a couple of screens. Long reference material goes in `references/` and is loaded only when needed.
- **Agent-neutral.** No instructions that only one agent can follow. Where a skill can use another agent, it is optional and degrades gracefully. For example, `review` can ask for a second opinion from a different agent CLI if one is installed (`codex exec`, `claude -p`), read-only, and merges the findings. If none is installed, it skips that step.
- **Linted.** `make lint` checks the front matter, that `name` matches the directory, that every link resolves, and that `.claude/skills` points at `.agents/skills`.
- **Improve in the same PR.** When a review finds the same mistake twice, or a skill sent an agent the wrong way, fix the skill in the PR that fixes the code.
- **Written from real code.** A skill is written right after the first real piece of work it describes, using that work as its example, not ahead of time.

## 5. Workflow: tasks folder

- `tasks/` holds one Markdown file per task, plus epic files that reference tasks. Conventions and template are in [tasks/README.md](../tasks/README.md).
- A task's **acceptance criteria are its test list** (§2.3). A task is done when each criterion has a passing test (or a stated reason it is covered elsewhere) and `make check` is green.
- `make lint` checks the task files: no duplicate IDs, every `depends_on` exists, and each epic's checklist matches its tasks' `status`. Two branches that take the same ID fail CI; the second one to merge takes the next free number.

## 6. Decisions and open questions

Decided:
- Compose is the default local runtime; kind is opt-in and comes with credentials.
- ConnectRPC only for our protocol; official MCP Go SDK; `google/jsonschema-go` for schemas.
- Go: `go 1.26.0` minimum, `toolchain go1.27.2`.

Open:
- Image size budget: set after measuring the first real image (M5).
- Does `fakellm` need an OpenAI-compatible mode from day one, or only Anthropic?
- Which build tags gate which backends (decided per backend as each one lands).
