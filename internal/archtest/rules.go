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
			"pkg/tool": {"github.com/google/jsonschema-go/jsonschema"},
		},
		TestOnly: []string{
			"github.com/google/go-cmp/cmp",
		},
		Banned: []string{
			"github.com/golang/mock",
			"go.uber.org/mock",
			"github.com/stretchr/testify/mock",
			"github.com/vektra/mockery",
		},
	}
}
