package common

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/bsamanvith/agentkit/pkg/tool"
)

// Limits adds keywords to input properties of a tool's schema, such as enums
// and ranges the code enforces, so the model sees them: property name to
// keywords.
type Limits map[string]map[string]any

// Func builds a tool with [tool.Func], then wraps it by [Checked]. Use it for
// every tool of a Google connector.
func Func[In, Out any](name, description string, fn func(context.Context, In) (Out, error), limits Limits, opts ...tool.FuncOption) (tool.Tool, error) {
	t, err := tool.Func(name, description, fn, opts...)
	if err != nil {
		return nil, err
	}
	return Checked(t, limits)
}

// Checked returns t with limits merged into its input schema, and with an
// [*InputError] from its function reported as [tool.ErrInvalidInput] rather
// than [tool.ErrToolFailed], so the model knows to change its input.
func Checked(t tool.Tool, limits Limits) (tool.Tool, error) {
	def := t.Definition()
	var schema map[string]any
	if err := json.Unmarshal(def.InputSchema, &schema); err != nil {
		return nil, fmt.Errorf("%w: %s: %w", tool.ErrInvalidDefinition, def.Name, err)
	}
	props, _ := schema["properties"].(map[string]any)
	for name, extra := range limits {
		p, ok := props[name].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%w: %s: limit on unknown input %q", tool.ErrInvalidDefinition, def.Name, name)
		}
		for k, v := range extra {
			p[k] = v
		}
	}
	b, err := json.Marshal(schema)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", tool.ErrInvalidDefinition, def.Name, err)
	}
	def.InputSchema = b
	return &checkedTool{inner: t, def: def}, nil
}

type checkedTool struct {
	inner tool.Tool
	def   tool.Definition
}

func (t *checkedTool) Definition() tool.Definition { return t.def.Clone() }

func (t *checkedTool) Invoke(ctx context.Context, input json.RawMessage) (json.RawMessage, error) {
	out, err := t.inner.Invoke(ctx, input)
	if ie, ok := errors.AsType[*InputError](err); ok {
		return nil, &tool.Error{Tool: t.def.Name, Kind: tool.ErrInvalidInput, Err: ie}
	}
	return out, err
}
