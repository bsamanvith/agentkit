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
			// The agent API runs a harness and never a model adapter:
			// cmd/agentkit wires the model in (E09). Its tests use the demo
			// toolsets and the fakes.
			"pkg/agentapi": {
				"pkg/harness", "pkg/llm", "pkg/tool", "pkg/toolset", "pkg/component", "pkg/clock",
				"pkg/llm/llmtest", "pkg/clock/clocktest", "internal/testkit", "pkg/toolset/toolsettest",
			},
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
