package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/jsonschema-go/jsonschema"
)

// FuncTool is a [Tool] backed by a typed Go function. Build one with [Func].
type FuncTool[In, Out any] struct {
	def   Definition
	input *jsonschema.Resolved
	fn    func(context.Context, In) (Out, error)
}

// Func builds a tool from a Go function.
//
// The input schema is derived from In, which must be a struct (or a map with
// string keys): exported fields become properties, fields tagged omitempty or
// omitzero are optional, all others are required, unknown properties are
// rejected, and a `jsonschema:"..."` field tag becomes the property's
// description. The output schema is derived from Out when Out is an object;
// otherwise the definition has no output schema.
//
// Every call's input is validated against the schema before fn runs, and a
// panic in fn is returned as an error matching [ErrToolFailed] and [ErrPanic].
func Func[In, Out any](name, description string, fn func(ctx context.Context, in In) (Out, error)) (*FuncTool[In, Out], error) {
	if err := checkName(name); err != nil {
		return nil, err
	}
	if fn == nil {
		return nil, fmt.Errorf("%w: tool %q: nil function", ErrInvalidDefinition, name)
	}

	in, err := jsonschema.For[In](nil)
	if err != nil {
		return nil, fmt.Errorf("%w: tool %q: input type: %w", ErrInvalidDefinition, name, err)
	}
	if in.Type != "object" {
		return nil, fmt.Errorf("%w: tool %q: input type %T must be a struct or map, got schema type %q",
			ErrInvalidDefinition, name, *new(In), in.Type)
	}
	resolved, err := in.Resolve(nil)
	if err != nil {
		return nil, fmt.Errorf("%w: tool %q: input schema: %w", ErrInvalidDefinition, name, err)
	}
	inJSON, err := json.Marshal(in)
	if err != nil {
		return nil, fmt.Errorf("%w: tool %q: input schema: %w", ErrInvalidDefinition, name, err)
	}

	out, err := jsonschema.For[Out](nil)
	if err != nil {
		return nil, fmt.Errorf("%w: tool %q: output type: %w", ErrInvalidDefinition, name, err)
	}
	var outJSON json.RawMessage
	if out.Type == "object" {
		if outJSON, err = json.Marshal(out); err != nil {
			return nil, fmt.Errorf("%w: tool %q: output schema: %w", ErrInvalidDefinition, name, err)
		}
	}

	return &FuncTool[In, Out]{
		def: Definition{
			Name:         name,
			Description:  description,
			InputSchema:  inJSON,
			OutputSchema: outJSON,
		},
		input: resolved,
		fn:    fn,
	}, nil
}

// Definition implements [Tool].
func (t *FuncTool[In, Out]) Definition() Definition { return t.def.Clone() }

// Invoke implements [Tool]. Empty input is treated as {}.
func (t *FuncTool[In, Out]) Invoke(ctx context.Context, input json.RawMessage) (json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, contextError(t.def.Name, err)
	}
	in, err := t.decode(input)
	if err != nil {
		return nil, &Error{Tool: t.def.Name, Kind: ErrInvalidInput, Err: err}
	}

	out, err := t.call(ctx, in)
	if err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return nil, contextError(t.def.Name, cerr)
		}
		return nil, err
	}

	b, err := json.Marshal(out)
	if err != nil {
		return nil, &Error{Tool: t.def.Name, Kind: ErrToolFailed, Err: fmt.Errorf("encode output: %w", err)}
	}
	return b, nil
}

func (t *FuncTool[In, Out]) decode(input json.RawMessage) (In, error) {
	var in In
	if len(bytes.TrimSpace(input)) == 0 {
		input = json.RawMessage("{}")
	}
	var generic any
	if err := json.Unmarshal(input, &generic); err != nil {
		return in, fmt.Errorf("parse input: %w", err)
	}
	if err := t.input.Validate(generic); err != nil {
		return in, err
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return in, fmt.Errorf("decode input: %w", err)
	}
	return in, nil
}

func (t *FuncTool[In, Out]) call(ctx context.Context, in In) (out Out, err error) {
	defer func() {
		if v := recover(); v != nil {
			err = recovered(t.def.Name, v)
		}
	}()
	out, err = t.fn(ctx, in)
	if err != nil {
		err = &Error{Tool: t.def.Name, Kind: ErrToolFailed, Err: err}
	}
	return out, err
}
