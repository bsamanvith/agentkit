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
			"cmd/agentkit": {"github.com/modelcontextprotocol/go-sdk/mcp", "github.com/jackc/pgx/v5"},
			// fakellm's test proves the official SDK can talk to it (T004).
			"internal/fakellm":     {"github.com/anthropics/anthropic-sdk-go", "github.com/anthropics/anthropic-sdk-go/option"},
			"examples/mcp-gateway": {"github.com/modelcontextprotocol/go-sdk/mcp"}, // its test uses the SDK client
			"pkg/mcp/server":       {"github.com/modelcontextprotocol/go-sdk/jsonrpc", "github.com/modelcontextprotocol/go-sdk/mcp"},
			// Other MCP servers' tools (T094), on the SDK's client.
			"pkg/mcp/client": {"github.com/modelcontextprotocol/go-sdk/jsonrpc", "github.com/modelcontextprotocol/go-sdk/mcp"},
			// MCP OAuth for external servers (T095): the SDK's metadata
			// discovery and registration (oauthex, auth), x/oauth2 for PKCE,
			// the code exchange and refresh. Its tests run the SDK's client
			// and server against the fakes.
			"pkg/mcp/oauth": {
				"github.com/modelcontextprotocol/go-sdk/auth", "github.com/modelcontextprotocol/go-sdk/oauthex",
				"golang.org/x/oauth2", "github.com/modelcontextprotocol/go-sdk/mcp",
			},
			// JWT bearer tokens (T041): go-jose for JWS and JWK sets, in
			// this package only (E07's decision). Its fake provider signs
			// with it.
			"pkg/auth/jwt":         {"github.com/go-jose/go-jose/v4"},
			"pkg/auth/jwt/jwttest": {"github.com/go-jose/go-jose/v4"},
			// Protected resource metadata (RFC 9728) on the SDK's handler
			// and types; apart from auth/jwt so that only what serves MCP
			// links the SDK.
			"pkg/auth/resourcemeta": {"github.com/modelcontextprotocol/go-sdk/auth", "github.com/modelcontextprotocol/go-sdk/oauthex"},
			// Its tests serve an SDK server to add at run time (T096).
			"pkg/agentapi": {"github.com/modelcontextprotocol/go-sdk/mcp"},
			// Its tests drive the agents over MCP through the SDK's client
			// (T063); the package itself uses agentclient only.
			"pkg/agenttools": {"github.com/modelcontextprotocol/go-sdk/mcp"},
			// MCP servers added at run time (T096): the SDK's tool type
			// for definitions; its tests run SDK servers as the fakes.
			"pkg/mcp/servers": {"github.com/modelcontextprotocol/go-sdk/mcp"},
			"pkg/tool":        {"github.com/google/jsonschema-go/jsonschema"},
			// The SQLite store's driver: pure Go, so binaries stay static
			// (T029). It also names the driver's error type, for SQLITE_BUSY.
			"pkg/store/sqlite": {"modernc.org/sqlite"},
			// The memory context store on SQLite (T058): the same driver.
			"pkg/memory/sqlite": {"modernc.org/sqlite"},
			// The credential vault's Postgres backend (T043), on the
			// store's pool.
			"pkg/credentials/postgres": {"github.com/jackc/pgx/v5", "github.com/jackc/pgx/v5/pgxpool"},
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
				// A run's tool policy is stored with its prompt (T083).
				"pkg/policy",
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
				"pkg/runner", "pkg/runner/pool", "pkg/store", "pkg/tenancy", "pkg/flow",
				// A run request's tool policy, and the decisions in the
				// run's events (T083).
				"pkg/policy",
				// Runs' events and commands between replicas (T090): the
				// stream over the store and a bus, in memory unless serve
				// gives one.
				"pkg/stream", "pkg/bus", "pkg/bus/memory",
				"pkg/llm/llmtest", "pkg/clock/clocktest", "internal/testkit", "pkg/toolset/toolsettest",
				"pkg/store/scoped",
				// Its tests run agents on a remote runner over toolsets on
				// the wire: the split topology (T102).
				"pkg/rpc", "pkg/runner/runnerrpc", "pkg/tool/toolrpc", "pkg/tool/tooltest",
				// Edge authentication: API keys, scopes and the toolsets a
				// credential may use (T040).
				"pkg/auth", "pkg/auth/authtest",
				// Its tests add MCP servers at run time (T096) through
				// the manager, behind the SSRF guard.
				"pkg/mcp/servers", "pkg/netguard",
			},
			// The tenant-scoped view of a backend: the one package outside
			// cmd/ and the runner that opens one, and it returns only the
			// interfaces, never the backend or its Admin.
			"pkg/store/scoped": {
				"pkg/store", "pkg/flow", "pkg/clock", "pkg/store/memory", "pkg/store/storeopen", "pkg/tenancy",
			},
			// The table of store backends, opened from a spec: the one
			// package that imports every backend (each behind its build
			// tag). It returns the backend's Admin, so only cmd/ and scoped
			// (which hides it) import it.
			"pkg/store/storeopen": {
				"pkg/store", "pkg/flow", "pkg/clock", "pkg/store/memory", "pkg/store/sqlite", "pkg/store/postgres", "internal/specs",
			},
			// The table of bus backends, opened from a spec.
			"pkg/bus/busopen": {"pkg/bus", "pkg/clock", "pkg/bus/memory", "pkg/bus/redis", "internal/specs"},
			// The rule for matching a backend's spec, shared by storeopen
			// and busopen.
			"internal/specs": {},
			// fakellm stands alone: it speaks the Anthropic wire format
			// from its own types, so no model or harness change moves it.
			"internal/fakellm": {"internal/testkit"},
			// Edge authentication knows tenants and nothing else: no server,
			// no store, no harness (E07). Tenancy never imports it back.
			"pkg/auth": {"pkg/tenancy"},
			// JWT tokens (T041) add the clock for token times and the key
			// cache; their tests the shared suite and the fake clock.
			"pkg/auth/jwt": {"pkg/auth", "pkg/tenancy", "pkg/clock", "pkg/clock/clocktest", "pkg/auth/authtest"},
			// The metadata wraps auth's middleware; its tests use the fake.
			"pkg/auth/resourcemeta": {"pkg/auth", "pkg/tenancy", "pkg/auth/authtest"},
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
				// The toolsets added at run time (T096), from agentkit's
				// own MCP server.
				"pkg/mcp/servers", "pkg/mcp/server", "pkg/netguard",
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
			// The transport between components is net/http and JSON, and
			// knows no other part of the module (T102): the services that use
			// it (toolrpc, runnerrpc) know their interfaces.
			"pkg/rpc": {},
			// Toolsets on the wire: the tool abstraction over the transport.
			// Its tests run the registry conformance suite through it.
			"pkg/tool/toolrpc": {"pkg/tool", "pkg/rpc", "pkg/tool/tooltest"},
			// Runner pools (E20, T104): where a run's agent runs, over the
			// runner's Agent and Placement, never the store.
			"pkg/runner/pool": {"pkg/runner", "pkg/harness"},
			// Agents on the wire: the runner's Agent (a harness's events and
			// errors) over the transport, never the store: the runner that
			// keeps runs stays where the store is.
			"pkg/runner/runnerrpc": {
				"pkg/runner", "pkg/harness", "pkg/llm", "pkg/rpc", "pkg/policy",
				"pkg/llm/llmtest", "pkg/tool", "pkg/tool/tooltest", "pkg/clock/clocktest",
			},
			// The web component serves the embedded console build over
			// net/http and nothing else: it is a component by shape, and
			// knows no other part of the module (E11, T078). The console
			// reaches the server only through /v1.
			"pkg/web": {},
			// The MCP server serves requests: tools only, never a store
			// backend (and so never store.Admin; see pkg/agentapi above).
			"pkg/mcp/server": {"pkg/tool", "pkg/tool/tooltest", "internal/testkit"},
			// Its tests run against agentkit's own MCP server.
			"pkg/mcp/client": {"pkg/tool", "pkg/toolset", "pkg/mcp/server", "internal/testkit"},
			// The built-in toolsets (math, files) are factories like the demo:
			// the tool abstraction and the factory contract, nothing else.
			"pkg/coretools": {"pkg/tool", "pkg/toolset"},
			// Agent Skills (T057) are read through the tool abstraction and
			// nothing else; its tests run a harness on the fake model.
			"pkg/skill": {"pkg/tool", "pkg/harness", "pkg/llm", "pkg/llm/llmtest", "internal/testkit"},
			// MCP OAuth knows principals (to name a connection's owner) and
			// the clock, never the server, a store backend or the harness: the
			// vault backs its stores once T042 lands.
			"pkg/mcp/oauth": {"pkg/auth", "pkg/tenancy", "pkg/clock", "pkg/clock/clocktest", "pkg/mcp/oauth/oauthtest"},
			// MCP servers added at run time (T096): the API's admin
			// contract, the MCP client, the SSRF guard and the store's
			// toolset records, never a backend but in its tests.
			"pkg/mcp/servers": {
				"pkg/agentapi", "pkg/agentapi/wire", "pkg/clock", "pkg/mcp/client",
				"pkg/netguard", "pkg/store", "pkg/tenancy", "pkg/tool", "pkg/store/memory",
			},
			// Tool policies (T083) decide on a tool's metadata: the tool
			// abstraction and nothing else, so the harness, the runner and
			// the API can all share them.
			"pkg/policy": {"pkg/tool"},
			// The memory context store (E12, T058) knows tenants and the tool
			// abstraction, never the harness, a model or the store: memory
			// reaches the model as a tool. Its live test runs it with the
			// harness on Claude.
			"pkg/memory": {"pkg/tenancy", "pkg/tool", "pkg/harness", "pkg/llm", "pkg/llm/anthropic"},
			// The SSRF guard is the standard library only.
			"pkg/netguard": {},
			// The credential vault (T042) knows tenants and nothing else:
			// backends and the conformance suites add only the vault itself.
			"pkg/credentials":                 {"pkg/tenancy", "pkg/credentials/credentialstest", "pkg/credentials/memory"},
			"pkg/credentials/memory":          {"pkg/credentials", "pkg/tenancy", "pkg/credentials/credentialstest"},
			"pkg/credentials/credentialstest": {"pkg/credentials", "pkg/tenancy"},
			// The SQL backends (T043) keep their table in the store's own
			// file or database, on the store's connections: they import
			// their store backend for that, and cmd/agentkit picks them
			// behind the same build tags as the store (nosqlite, nopostgres).
			"pkg/credentials/sqlite": {
				"pkg/credentials", "pkg/credentials/internal/sqltime", "pkg/tenancy",
				"pkg/store/sqlite", "pkg/store", "pkg/credentials/credentialstest",
			},
			"pkg/credentials/postgres": {
				"pkg/credentials", "pkg/credentials/internal/sqltime", "pkg/tenancy",
				"pkg/store/postgres", "pkg/credentials/credentialstest", "internal/testkit",
			},
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
		// Shared code of connectors: the HTTP rules and truncation every
		// connector follows (connectors/internal), and each family's common
		// package. Not connectors, so not run through the kit (their own
		// tests cover them).
		ConnectorLibraries: []string{
			"connectors/internal/httpx", "connectors/internal/trunc",
			"connectors/google/common",
		},
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
