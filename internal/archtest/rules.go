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
			"cmd/agentkit":         {"github.com/modelcontextprotocol/go-sdk/mcp"}, // its serve e2e test uses the SDK client
			"examples/mcp-gateway": {"github.com/modelcontextprotocol/go-sdk/mcp"}, // its test uses the SDK client
			"pkg/mcp/server":       {"github.com/modelcontextprotocol/go-sdk/jsonrpc", "github.com/modelcontextprotocol/go-sdk/mcp"},
			"pkg/tool":             {"github.com/google/jsonschema-go/jsonschema"},
			// The SQLite store's driver: pure Go, so binaries stay static
			// (T029). It also names the driver's error type, for SQLITE_BUSY.
			"pkg/store/sqlite": {"modernc.org/sqlite"},
			// The connector test kit validates schemas and checks connectors over MCP.
			"pkg/toolset/toolsettest": {"github.com/google/jsonschema-go/jsonschema", "github.com/modelcontextprotocol/go-sdk/mcp"},
			// Its tests validate every output against the declared output
			// schema; the connector itself uses net/http only.
			"connectors/tavily": {"github.com/google/jsonschema-go/jsonschema"},
			// Its spec test checks every request and cassette against
			// Parallel's OpenAPI schemas.
			"connectors/parallel": {"github.com/google/jsonschema-go/jsonschema"},
		},
		TestOnly: []string{
			"github.com/google/go-cmp/cmp",
		},
		ModuleImports: map[string][]string{
			// The store's interfaces and backends know messages, flow
			// checkpoints, tenants and time, and never the harness: the
			// runner turns harness events into entries (E05).
			"pkg/store": {
				"pkg/llm", "pkg/flow", "pkg/flow/flowtest", "pkg/tenancy",
				"pkg/clock", "pkg/clock/clocktest",
			},
			// The runner is the one package that knows both the harness and
			// the store (E05). Its tests run it on the memory store and the
			// fakes; that its own code never imports a store backend is
			// checked by its TestRunnerDoesNotImportABackend.
			"pkg/runner": {
				"pkg/harness", "pkg/store", "pkg/flow", "pkg/agentstep", "pkg/llm", "pkg/tool", "pkg/tenancy", "pkg/clock",
				"pkg/store/memory", "pkg/llm/llmtest", "pkg/clock/clocktest", "pkg/agentstep/agentsteptest", "internal/testkit",
			},
			// The agent API runs a harness and never a model adapter:
			// cmd/agentkit wires the model in (E09). Its tests use the demo
			// toolsets and the fakes.
			//
			// It serves requests, so it never imports a store backend
			// (pkg/store/memory, pkg/store/sqlite), tests included: a backend
			// is the only way to its store.Admin, the cross-tenant view that
			// only the runner's recovery loop may hold (T038). cmd/ wires
			// backends. T037 adds pkg/runner and pkg/store here, never a
			// backend.
			"pkg/agentapi": {
				"pkg/harness", "pkg/llm", "pkg/tool", "pkg/toolset", "pkg/component", "pkg/clock",
				"pkg/llm/llmtest", "pkg/clock/clocktest", "internal/testkit", "pkg/toolset/toolsettest",
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
