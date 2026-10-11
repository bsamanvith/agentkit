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
		},
		Banned: []string{
			"github.com/golang/mock",
			"go.uber.org/mock",
			"github.com/stretchr/testify/mock",
			"github.com/vektra/mockery",
		},
	}
}
