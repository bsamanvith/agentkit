package common

import (
	"context"

	"github.com/bsamanvith/agentkit/connectors/internal/rest"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

// Limits adds keywords to input properties of a tool's schema, such as enums
// and ranges the code enforces, so the model sees them: property name to
// keywords.
type Limits = rest.Limits

// Func builds a tool with [tool.Func], then wraps it by [Checked]. Use it for
// every tool of a Google connector.
func Func[In, Out any](name, description string, fn func(context.Context, In) (Out, error), limits Limits, opts ...tool.FuncOption) (tool.Tool, error) {
	return rest.Func(name, description, fn, limits, opts...)
}

// Checked returns t with limits merged into its input schema, and with an
// [*InputError] from its function reported as [tool.ErrInvalidInput] rather
// than [tool.ErrToolFailed], so the model knows to change its input.
func Checked(t tool.Tool, limits Limits) (tool.Tool, error) { return rest.Checked(t, limits) }
