package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
)

// Registry is a set of tools, listed and invoked by name. The in-process
// implementation is [LocalRegistry]; remote clients implement the same
// interface. Implementations must be safe for concurrent use.
type Registry interface {
	// List returns the definitions of all tools, sorted by name.
	List(ctx context.Context) ([]Definition, error)
	// Invoke runs the named tool. An unknown name returns ErrNotFound.
	Invoke(ctx context.Context, name string, input json.RawMessage) (json.RawMessage, error)
}

// InvokeFunc runs one tool call. Middleware wraps it.
type InvokeFunc func(ctx context.Context, def Definition, input json.RawMessage) (json.RawMessage, error)

// Middleware wraps every call made through a [LocalRegistry]. It must treat def
// as read-only.
type Middleware func(next InvokeFunc) InvokeFunc

// Option configures [NewRegistry].
type Option func(*registryOptions)

type registryOptions struct {
	middleware []Middleware
}

// WithMiddleware adds middleware to every call. The first middleware given is
// the outermost: it runs first on the way in and last on the way out.
func WithMiddleware(mw ...Middleware) Option {
	return func(o *registryOptions) { o.middleware = append(o.middleware, mw...) }
}

// LocalRegistry is an in-process [Registry]. It is immutable after
// [NewRegistry] returns, so it needs no locking.
type LocalRegistry struct {
	byName map[string]entry
	defs   []Definition // sorted by name
}

type entry struct {
	def    Definition
	invoke InvokeFunc
}

var _ Registry = (*LocalRegistry)(nil)

// NewRegistry builds a registry of tools. It fails with [ErrInvalidDefinition]
// for a tool with an invalid name and [ErrDuplicateName] if two tools share a
// name. Panics in any tool are recovered and returned as [ErrToolFailed].
func NewRegistry(tools []Tool, opts ...Option) (*LocalRegistry, error) {
	var o registryOptions
	for _, opt := range opts {
		opt(&o)
	}

	r := &LocalRegistry{byName: make(map[string]entry, len(tools))}
	for i, t := range tools {
		if t == nil {
			return nil, fmt.Errorf("%w: tool %d is nil", ErrInvalidDefinition, i)
		}
		def := t.Definition()
		if err := checkName(def.Name); err != nil {
			return nil, err
		}
		if _, dup := r.byName[def.Name]; dup {
			return nil, fmt.Errorf("%w: %q", ErrDuplicateName, def.Name)
		}

		invoke := safeInvoke(t)
		for j := len(o.middleware) - 1; j >= 0; j-- {
			invoke = o.middleware[j](invoke)
		}
		r.byName[def.Name] = entry{def: def, invoke: invoke}
		r.defs = append(r.defs, def)
	}
	sort.Slice(r.defs, func(i, j int) bool { return r.defs[i].Name < r.defs[j].Name })
	return r, nil
}

// List implements [Registry]. The returned definitions are copies.
func (r *LocalRegistry) List(ctx context.Context) ([]Definition, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out := make([]Definition, len(r.defs))
	for i, d := range r.defs {
		out[i] = d.Clone()
	}
	return out, nil
}

// Invoke implements [Registry].
func (r *LocalRegistry) Invoke(ctx context.Context, name string, input json.RawMessage) (json.RawMessage, error) {
	e, ok := r.byName[name]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrNotFound, name)
	}
	return e.invoke(ctx, e.def, input)
}

// safeInvoke calls t, converting a panic into an error, so a misbehaving
// tool that was not built with Func cannot crash the caller.
func safeInvoke(t Tool) InvokeFunc {
	return func(ctx context.Context, def Definition, input json.RawMessage) (out json.RawMessage, err error) {
		defer func() {
			if v := recover(); v != nil {
				out, err = nil, recovered(def.Name, v)
			}
		}()
		return t.Invoke(ctx, input)
	}
}
