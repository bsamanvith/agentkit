package archtest

// ModuleRules returns the rules for this repository. Adding a third-party
// dependency to a package means adding a line here, so every new dependency
// is a visible, reviewed decision (docs/ENGINEERING.md §1.4).
func ModuleRules() Rules {
	return Rules{
		Module: "github.com/bsamanvith/agentkit",
		ThirdParty: map[string][]string{
			"internal/testkit": {"github.com/google/go-cmp/cmp"},
			"pkg/llm/anthropic": {
				"github.com/anthropics/anthropic-sdk-go",
				"github.com/anthropics/anthropic-sdk-go/option",
				"github.com/anthropics/anthropic-sdk-go/packages/param",
				"github.com/anthropics/anthropic-sdk-go/shared/constant",
			},
			// Its serve e2e test uses the SDK client; its migrate integration
			// test makes a schema to migrate with pgx.
			"cmd/agentkit":         {"github.com/modelcontextprotocol/go-sdk/mcp", "github.com/jackc/pgx/v5"},
			"examples/mcp-gateway": {"github.com/modelcontextprotocol/go-sdk/mcp"}, // its test uses the SDK client
			"pkg/mcp/server":       {"github.com/modelcontextprotocol/go-sdk/jsonrpc", "github.com/modelcontextprotocol/go-sdk/mcp"},
			"pkg/tool":             {"github.com/google/jsonschema-go/jsonschema"},
			// The SQLite store's driver: pure Go, so binaries stay static
			// (T029). It also names the driver's error type, for SQLITE_BUSY.
			"pkg/store/sqlite": {"modernc.org/sqlite"},
			// The Postgres store's driver and pool (T088). pgconn names the
			// server's error type, for SQLSTATEs.
			"pkg/store/postgres": {"github.com/jackc/pgx/v5", "github.com/jackc/pgx/v5/pgconn", "github.com/jackc/pgx/v5/pgxpool"},
			// The connector test kit validates schemas and checks connectors over MCP.
			"pkg/toolset/toolsettest": {"github.com/google/jsonschema-go/jsonschema", "github.com/modelcontextprotocol/go-sdk/mcp"},
			// Its tests validate every output against the declared output
			// schema; the connector itself uses net/http only.
			"connectors/tavily": {"github.com/google/jsonschema-go/jsonschema"},
			// Its spec test checks every request and cassette against
			// Parallel's OpenAPI schemas.
			"connectors/parallel": {"github.com/google/jsonschema-go/jsonschema"},
			// The Redis Streams bus (T089). maintnotifications is named
			// only to refuse go-redis's racy auto mode. The bus backends'
			// tests check for leaked goroutines with goleak.
			"pkg/bus/redis":  {"github.com/redis/go-redis/v9", "github.com/redis/go-redis/v9/maintnotifications", "go.uber.org/goleak"},
			"pkg/bus/memory": {"go.uber.org/goleak"},
			// The stream's tests run on the Redis bus too, and check for
			// leaked watches with goleak.
			"pkg/stream": {"github.com/redis/go-redis/v9", "github.com/redis/go-redis/v9/maintnotifications", "go.uber.org/goleak"},
		},
		TestOnly: []string{
			"github.com/google/go-cmp/cmp",
			"go.uber.org/goleak",
		},
		ModuleImports: map[string][]string{
			// The store's interfaces and backends know messages, flow
			// checkpoints, tenants and time, and never the harness: the
			// runner turns harness events into entries (E05). The Postgres
			// store's tests use the env gating helper.
			"pkg/store": {
				"pkg/llm", "pkg/flow", "pkg/flow/flowtest", "pkg/tenancy",
				"pkg/clock", "pkg/clock/clocktest", "internal/testkit",
			},
			// The runner is the one package that knows both the harness and
			// the store (E05). Its tests run it on the memory store and the
			// fakes; that its own code never imports a store backend is
			// checked by its TestRunnerDoesNotImportABackend.
			"pkg/runner": {
				"pkg/harness", "pkg/store", "pkg/flow", "pkg/agentstep", "pkg/llm", "pkg/tool", "pkg/tenancy", "pkg/clock",
				"pkg/store/memory", "pkg/llm/llmtest", "pkg/clock/clocktest", "pkg/agentstep/agentsteptest", "internal/testkit",
				// Benchmarks measure the real backend; TestRunnerDoesNotImportABackend
				// keeps it out of the package itself.
				"pkg/store/sqlite",
			},
			// The agent API runs a harness and never a model adapter:
			// cmd/agentkit wires the model in (E09). Its tests use the demo
			// toolsets and the fakes.
			//
			// It serves requests, so it never imports a store backend
			// (pkg/store/memory, pkg/store/sqlite, pkg/store/postgres), tests
			// included: a backend is the only way to its store.Admin, the
			// cross-tenant view that only the runner's recovery loop may hold
			// (T038). cmd/ wires backends; its tests get their store from
			// pkg/store/scoped, which hides the backend. T037 runs it on
			// pkg/runner over pkg/store, scoped by pkg/tenancy; pkg/flow is
			// for the decisions of a suspended flow run.
			"pkg/agentapi": {
				"pkg/harness", "pkg/llm", "pkg/tool", "pkg/toolset", "pkg/component", "pkg/clock",
				"pkg/runner", "pkg/store", "pkg/tenancy", "pkg/flow",
				"pkg/llm/llmtest", "pkg/clock/clocktest", "internal/testkit", "pkg/toolset/toolsettest",
				"pkg/store/scoped",
			},
			// The tenant-scoped view of a backend: the one package outside
			// cmd/ and the runner that opens one, and it returns only the
			// interfaces, never the backend or its Admin.
			"pkg/store/scoped": {
				"pkg/store", "pkg/flow", "pkg/clock", "pkg/store/memory", "pkg/store/sqlite", "pkg/store/postgres", "pkg/tenancy",
			},
			// Edge authentication knows tenants and nothing else: no server,
			// no store, no harness (E07). Tenancy never imports it back.
			"pkg/auth": {"pkg/tenancy"},
			// The wire format, shared by the server and its clients: messages
			// and usage, nothing else (E11).
			"pkg/agentapi/wire": {"pkg/llm"},
			// The agent API's client speaks the wire format over net/http.
			// Its tests run it against the real server (agentapi, the
			// harness, the demo toolsets and the fake model); that its own
			// code never imports the server or the harness is checked by
			// its TestClientDoesNotImportTheServer.
			"pkg/agentclient": {
				"pkg/agentapi/wire", "pkg/llm",
				"pkg/agentapi", "pkg/harness", "pkg/tool", "pkg/toolset", "pkg/llm/llmtest", "internal/testkit",
				"pkg/store/scoped",
			},
			// The event bus knows tenants (for topic names) and the clock,
			// nothing else: the store, the runner and the API build on it
			// (E18). Its tests use the env gating helper.
			"pkg/bus": {"pkg/tenancy", "pkg/clock", "pkg/clock/clocktest", "internal/testkit"},
			// The stream joins the store's logs and the bus (E18, T090): it
			// knows their contracts, never a backend. Its tests run on the
			// memory store and both buses.
			"pkg/stream": {
				"pkg/bus", "pkg/store", "pkg/tenancy", "pkg/clock",
				"pkg/bus/memory", "pkg/bus/redis", "pkg/store/memory", "pkg/clock/clocktest", "internal/testkit",
			},
			// The web component serves the embedded console build over
			// net/http and nothing else: it is a component by shape, and
			// knows no other part of the module (E11, T078). The console
			// reaches the server only through /v1.
			"pkg/web": {},
			// The MCP server serves requests: tools only, never a store
			// backend (and so never store.Admin; see pkg/agentapi above).
			"pkg/mcp/server": {"pkg/tool", "pkg/tool/tooltest", "internal/testkit"},
			// Connectors are toolset factories over plain net/http: the tool
			// abstraction, the factory contract and the clock, never the
			// server, the harness or another connector (E10). Their tests
			// add the fake clock and the golden helper.
			"connectors": {
				"pkg/tool", "pkg/toolset", "pkg/clock",
				"pkg/clock/clocktest", "internal/testkit",
				"pkg/toolset/toolsettest",
			},
		},
		// Connectors that do not yet run the connector test kit
		// (docs/CONNECTORS.md). Each entry goes when its connector adopts it.
		PendingConnectors: map[string]string{},
		// The agent API's wire types live in their own package so that
		// clients get them without the harness (E11, T053).
		SharedChildren: map[string][]string{"pkg/agentapi": {"pkg/agentapi/wire"}},
		Banned: []string{
			"github.com/golang/mock",
			"go.uber.org/mock",
			"github.com/stretchr/testify/mock",
			"github.com/vektra/mockery",
		},
	}
}
