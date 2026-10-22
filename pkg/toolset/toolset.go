package toolset

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/bsamanvith/agentkit/pkg/credentials"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

// Errors reported by [NewCatalog] and [Build], checked with [errors.Is].
var (
	// ErrInvalidName reports a toolset name that is not safe as a URL path
	// segment: it must match ^[a-zA-Z0-9_-]{1,64}$.
	ErrInvalidName = errors.New("invalid toolset name")
	// ErrDuplicateName reports two specs with the same name.
	ErrDuplicateName = errors.New("duplicate toolset name")
	// ErrUnknownType reports a spec whose type is not in the catalog. The
	// error is a [*UnknownTypeError] listing the types that are.
	ErrUnknownType = errors.New("unknown toolset type")
	// ErrInvalidSettings reports settings a factory could not decode or
	// validate. Factories wrap it; [DecodeSettings] does so for them.
	ErrInvalidSettings = errors.New("invalid toolset settings")
	// ErrInvalidFilter reports an include/exclude list that is malformed,
	// has a pattern matching no tool, or leaves the toolset empty; or a
	// descriptions entry naming no kept tool, or empty.
	ErrInvalidFilter = errors.New("invalid toolset filter")
	// ErrDuplicateType reports two factories with the same type in one catalog.
	ErrDuplicateType = errors.New("duplicate factory type")
	// ErrInvalidFactory reports a nil factory or one with an unusable type.
	ErrInvalidFactory = errors.New("invalid factory")
	// ErrInvalidCredentialRef reports a spec's credential with an invalid
	// slot name or scope.
	ErrInvalidCredentialRef = errors.New("invalid toolset credential")
	// ErrNoCredentials reports a spec naming a credential slot that [Build]
	// has no source for: no [WithSources], or one that refused the slot
	// (no vault is configured, say).
	ErrNoCredentials = errors.New("no source for the toolset's credential")
)

var namePattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

// Factory builds the tools of one connector type from its settings.
type Factory interface {
	// Type is the name config uses to select this factory (the Spec's Type).
	// It must match ^[a-zA-Z0-9_-]{1,64}$.
	Type() string
	// Build decodes and validates in.Settings (raw JSON, possibly empty)
	// and returns the connector's tools. Unknown settings fields must be an
	// error: use [DecodeSettings]. Build is called once per spec, so two
	// instances never share state unless the factory shares it.
	Build(ctx context.Context, in BuildInput) ([]tool.Tool, error)
}

// BuildInput is what a [Factory] builds one toolset from.
type BuildInput struct {
	// Settings is the spec's settings, the factory's own copy.
	Settings json.RawMessage
	// Credentials is the source of the spec's credential slot, nil when
	// the spec names none. Tools call its Get or HTTPClient with their
	// call's context, which carries the caller's tenant and user; a
	// missing credential is credentials.ErrNotConnected, which a tool
	// returns as it is (the model reads "<toolset> is not connected for
	// this account").
	Credentials credentials.Source
}

// SourceProvider gives a toolset the [credentials.Source] of its slot.
// *credentials.Sources is one.
type SourceProvider interface {
	Source(toolset, slot string, scope credentials.Scope) (credentials.Source, error)
}

// SourceFunc is a [SourceProvider] as a function.
type SourceFunc func(toolset, slot string, scope credentials.Scope) (credentials.Source, error)

// Source implements [SourceProvider].
func (f SourceFunc) Source(toolset, slot string, scope credentials.Scope) (credentials.Source, error) {
	return f(toolset, slot, scope)
}

// BuildOption configures [Build].
type BuildOption func(*buildOptions)

type buildOptions struct {
	sources SourceProvider
}

// WithSources gives the specs that name a credential slot their sources.
// Without it, such a spec fails with [ErrNoCredentials] (or is skipped, if
// optional).
func WithSources(p SourceProvider) BuildOption {
	return func(o *buildOptions) { o.sources = p }
}

// DecodeSettings strictly decodes raw settings into v, a pointer to the
// factory's settings struct. Empty or null settings leave v untouched.
// Unknown fields and trailing data are errors matching [ErrInvalidSettings].
func DecodeSettings(raw json.RawMessage, v any) error {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidSettings, err)
	}
	if dec.More() {
		return fmt.Errorf("%w: unexpected data after settings object", ErrInvalidSettings)
	}
	return nil
}

// Catalog is an immutable set of factories keyed by type. Build one with
// [NewCatalog] in the binary that decides which connectors are compiled in.
type Catalog struct {
	factories map[string]Factory
	types     []string // sorted
}

// NewCatalog builds a catalog. It fails with [ErrInvalidFactory] for a nil
// factory or an invalid type, and with [ErrDuplicateType] if two factories
// share a type.
func NewCatalog(factories ...Factory) (*Catalog, error) {
	c := &Catalog{factories: make(map[string]Factory, len(factories))}
	for i, f := range factories {
		if f == nil {
			return nil, fmt.Errorf("%w: factory %d is nil", ErrInvalidFactory, i)
		}
		typ := f.Type()
		if !namePattern.MatchString(typ) {
			return nil, fmt.Errorf("%w: type %q must match %s", ErrInvalidFactory, typ, namePattern)
		}
		if _, dup := c.factories[typ]; dup {
			return nil, fmt.Errorf("%w: %q", ErrDuplicateType, typ)
		}
		c.factories[typ] = f
		c.types = append(c.types, typ)
	}
	sort.Strings(c.types)
	return c, nil
}

// Types returns the factory types in the catalog, sorted.
func (c *Catalog) Types() []string {
	if c == nil {
		return nil
	}
	return slices.Clone(c.types)
}

func (c *Catalog) lookup(typ string) (Factory, bool) {
	if c == nil {
		return nil, false
	}
	f, ok := c.factories[typ]
	return f, ok
}

// UnknownTypeError is the error for a spec whose type is not compiled in.
type UnknownTypeError struct {
	Toolset   string   // the spec's name
	Type      string   // the requested type
	Available []string // types in the catalog, sorted
}

func (e *UnknownTypeError) Error() string {
	avail := "none"
	if len(e.Available) > 0 {
		avail = strings.Join(e.Available, ", ")
	}
	return fmt.Sprintf("toolset %q: unknown type %q (compiled-in types: %s)", e.Toolset, e.Type, avail)
}

// Is reports whether target is [ErrUnknownType].
func (e *UnknownTypeError) Is(target error) bool { return target == ErrUnknownType }

// Error is the error for one toolset that failed to build. It wraps the cause,
// so errors.Is matches the sentinels above and errors.As reaches the
// factory's own errors.
type Error struct {
	Toolset string // the spec's name
	Type    string // the spec's type
	Err     error
}

func (e *Error) Error() string {
	return fmt.Sprintf("toolset %q (type %q): %v", e.Toolset, e.Type, e.Err)
}

// Unwrap returns the cause.
func (e *Error) Unwrap() error { return e.Err }

// Problem reports an optional toolset that was skipped.
type Problem struct {
	Toolset string // the spec's name
	Type    string // the spec's type
	Err     error  // why it was skipped (a [*Error])
}

func (p Problem) Error() string { return "skipped " + p.Err.Error() }

// Build turns specs into one registry per toolset name.
//
// Mistakes in the specs themselves fail the build whatever Optional says: an
// invalid or duplicate name, a type not in the catalog, a malformed glob.
// Everything validated before any factory runs, so a bad config does no work.
//
// A toolset that fails while being built (the factory returns an error,
// settings are invalid, a filter matches nothing) fails the build if it is
// required. If Optional, it is left out of the result and reported as a
// [Problem]. So does a spec whose credential slot has no source
// ([ErrNoCredentials]); a malformed slot or scope is a mistake in the spec.
// A cancelled ctx always fails the build.
func Build(ctx context.Context, catalog *Catalog, specs []Spec, opts ...BuildOption) (map[string]tool.Registry, []Problem, error) {
	var o buildOptions
	for _, opt := range opts {
		opt(&o)
	}
	seen := make(map[string]bool, len(specs))
	factories := make([]Factory, len(specs))
	for i, s := range specs {
		if !namePattern.MatchString(s.Name) {
			return nil, nil, &Error{
				Toolset: s.Name, Type: s.Type,
				Err: fmt.Errorf("%w: must match %s", ErrInvalidName, namePattern),
			}
		}
		if seen[s.Name] {
			return nil, nil, &Error{Toolset: s.Name, Type: s.Type, Err: ErrDuplicateName}
		}
		seen[s.Name] = true

		f, ok := catalog.lookup(s.Type)
		if !ok {
			return nil, nil, &UnknownTypeError{Toolset: s.Name, Type: s.Type, Available: catalog.Types()}
		}
		factories[i] = f

		if err := checkGlobs(s.Include, s.Exclude); err != nil {
			return nil, nil, &Error{Toolset: s.Name, Type: s.Type, Err: err}
		}
		if c := s.Credential; c != nil {
			if err := errors.Join(credentials.CheckSlot(c.Slot), credentials.CheckScope(c.Scope)); err != nil {
				return nil, nil, &Error{Toolset: s.Name, Type: s.Type, Err: fmt.Errorf("%w: %w", ErrInvalidCredentialRef, err)}
			}
		}
	}

	registries := make(map[string]tool.Registry, len(specs))
	var problems []Problem
	for i, s := range specs {
		if err := ctx.Err(); err != nil {
			return nil, problems, err
		}
		reg, err := buildOne(ctx, factories[i], s, o.sources)
		if err == nil {
			registries[s.Name] = reg
			continue
		}
		if cerr := ctx.Err(); cerr != nil {
			return nil, problems, cerr
		}
		wrapped := &Error{Toolset: s.Name, Type: s.Type, Err: err}
		if !s.Optional {
			return nil, problems, wrapped
		}
		problems = append(problems, Problem{Toolset: s.Name, Type: s.Type, Err: wrapped})
	}
	return registries, problems, nil
}

func buildOne(ctx context.Context, f Factory, s Spec, sources SourceProvider) (tool.Registry, error) {
	// Each instance gets its own copy of the settings bytes.
	in := BuildInput{Settings: bytes.Clone(s.Settings)}
	if c := s.Credential; c != nil {
		if sources == nil {
			return nil, fmt.Errorf("%w: slot %q (no credential vault)", ErrNoCredentials, c.Slot)
		}
		src, err := sources.Source(s.Name, c.Slot, c.Scope)
		if err != nil {
			if errors.Is(err, ErrNoCredentials) {
				return nil, err
			}
			return nil, fmt.Errorf("%w: slot %q: %w", ErrNoCredentials, c.Slot, err)
		}
		in.Credentials = src
	}
	tools, err := f.Build(ctx, in)
	if err != nil {
		return nil, err
	}
	tools, err = filterTools(tools, s.Include, s.Exclude)
	if err != nil {
		return nil, err
	}
	tools, err = describeTools(tools, s.Descriptions)
	if err != nil {
		return nil, err
	}
	reg, err := tool.NewRegistry(tools)
	if err != nil {
		return nil, err
	}
	return reg, nil
}
