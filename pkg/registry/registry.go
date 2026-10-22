package registry

import (
	"bytes"
	"container/list"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"sync"

	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

// KindAgent is the store kind of agent definitions ([store.ItemVersion]).
const KindAgent = "agent"

// DefaultCacheSize is how many built agents a [Registry] keeps by default.
const DefaultCacheSize = 256

// validName is the rule for agent names (the agent API's).
var validName = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

// Builder builds the agent a definition describes, with its harness (or
// runner): the definition as saved, a JSON object whose "name" is name. It
// validates the definition in full, and refuses an invalid one with a
// *agentapi.DefinitionError naming the fields; any other error (but ctx's)
// refuses it as a whole. It must not keep or change definition.
type Builder func(ctx context.Context, name string, definition json.RawMessage) (agentapi.Agent, error)

// Registry keeps a tenant's agent definitions in a [store.ItemStore] and
// builds them to run. It is safe for concurrent use. Make one with [New].
type Registry struct {
	st       store.ItemStore
	build    Builder
	readOnly map[string]bool
	log      *slog.Logger

	mu    sync.Mutex
	size  int
	order *list.List // of *cached, most recently used first
	cache map[cacheKey]*list.Element
}

var _ agentapi.AgentRegistry = (*Registry)(nil)

type cacheKey struct {
	tenant, name string
	version      int64
}

type cached struct {
	key   cacheKey
	agent agentapi.Agent
}

// Option configures a [Registry].
type Option func(*Registry) error

// WithReadOnly names the agents of the config file: a save or a publish
// under one of their names is [agentapi.ErrAgentReadOnly].
func WithReadOnly(names ...string) Option {
	return func(r *Registry) error {
		for _, n := range names {
			r.readOnly[n] = true
		}
		return nil
	}
}

// WithCacheSize sets how many built agents the registry keeps, across
// tenants and versions (default [DefaultCacheSize]); the least recently
// used goes first. It must be at least 1.
func WithCacheSize(n int) Option {
	return func(r *Registry) error {
		if n < 1 {
			return fmt.Errorf("registry: cache size %d is not positive", n)
		}
		r.size = n
		return nil
	}
}

// WithLogger sets where the registry logs a published agent that no longer
// builds (default: nowhere).
func WithLogger(l *slog.Logger) Option {
	return func(r *Registry) error {
		if l == nil {
			return errors.New("registry: nil logger")
		}
		r.log = l
		return nil
	}
}

// New returns a registry over st that builds definitions with build.
func New(st store.ItemStore, build Builder, opts ...Option) (*Registry, error) {
	if st == nil || build == nil {
		return nil, errors.New("registry: nil store or builder")
	}
	r := &Registry{
		st: st, build: build, readOnly: map[string]bool{}, log: slog.New(slog.DiscardHandler),
		size: DefaultCacheSize, order: list.New(), cache: map[cacheKey]*list.Element{},
	}
	for _, o := range opts {
		if err := o(r); err != nil {
			return nil, err
		}
	}
	return r, nil
}

// checkName refuses a bad name, and with write a read-only one.
func (r *Registry) checkName(name string, write bool) error {
	switch {
	case !validName.MatchString(name):
		return fmt.Errorf("%w: agent name %q must match %s", agentapi.ErrAgentInvalid, name, validName)
	case write && r.readOnly[name]:
		return fmt.Errorf("%w: agent %q comes from the server's config file; change it there", agentapi.ErrAgentReadOnly, name)
	}
	return nil
}

// Save implements [agentapi.AgentRegistry]: it normalizes the definition
// (its "name" set to name, keys sorted), builds it to validate it, and
// stores it as the agent's next version, then publishes it if asked.
func (r *Registry) Save(ctx context.Context, by, name string, req wire.SaveAgentRequest) (wire.AgentVersion, error) {
	if err := r.checkName(name, true); err != nil {
		return wire.AgentVersion{}, err
	}
	def, err := normalize(name, req.Definition)
	if err != nil {
		return wire.AgentVersion{}, err
	}
	if _, err := r.validate(ctx, name, def); err != nil {
		return wire.AgentVersion{}, err
	}
	var ifLatest int64
	if req.IfLatest != nil {
		ifLatest = *req.IfLatest
		if ifLatest < 0 {
			return wire.AgentVersion{}, fmt.Errorf("%w: if_latest %d is negative", agentapi.ErrAgentInvalid, ifLatest)
		}
	} else {
		it, err := r.st.GetItem(ctx, KindAgent, name)
		switch {
		case err == nil:
			ifLatest = it.Latest
		case !errors.Is(err, store.ErrNotFound):
			return wire.AgentVersion{}, err
		}
	}
	v, err := r.st.SaveItem(ctx, store.ItemVersion{Kind: KindAgent, Name: name, Data: def, Author: by, Message: req.Message}, ifLatest)
	switch {
	case errors.Is(err, store.ErrConflict):
		return wire.AgentVersion{}, fmt.Errorf("%w: %w", agentapi.ErrAgentConflict, err)
	case errors.Is(err, store.ErrNotFound):
		return wire.AgentVersion{}, fmt.Errorf("%w: %w", agentapi.ErrAgentNotFound, err)
	case errors.Is(err, store.ErrInvalid):
		return wire.AgentVersion{}, fmt.Errorf("%w: %w", agentapi.ErrAgentInvalid, err)
	case err != nil:
		return wire.AgentVersion{}, err
	}
	published := false
	if req.Publish {
		it, err := r.st.PublishItem(ctx, KindAgent, name, v.Version)
		if err != nil {
			return wire.AgentVersion{}, fmt.Errorf("publishing version %d: %w", v.Version, err)
		}
		published = it.Published == v.Version
		if v, err = r.st.GetItemVersion(ctx, KindAgent, name, v.Version); err != nil {
			return wire.AgentVersion{}, err
		}
	}
	return versionView(v, published), nil
}

// validate builds def, turning a failure into a *agentapi.DefinitionError
// unless ctx is done.
func (r *Registry) validate(ctx context.Context, name string, def json.RawMessage) (agentapi.Agent, error) {
	a, err := r.build(ctx, name, slices.Clone(def))
	if err == nil {
		if (a.Harness == nil) == (a.Runner == nil) {
			return agentapi.Agent{}, errors.New("registry: the builder gave an agent with a harness and a runner, or neither")
		}
		a.Name = name
		return a, nil
	}
	if cerr := ctx.Err(); cerr != nil {
		return agentapi.Agent{}, cerr
	}
	var de *agentapi.DefinitionError
	if errors.As(err, &de) {
		return agentapi.Agent{}, err
	}
	return agentapi.Agent{}, &agentapi.DefinitionError{Fields: []wire.FieldError{{Message: err.Error()}}}
}

// normalize checks that a definition is a JSON object whose "name", if
// any, is name, and returns it with the name set and its keys sorted.
func normalize(name string, def json.RawMessage) (json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(def, &fields); err != nil || fields == nil {
		return nil, &agentapi.DefinitionError{Fields: []wire.FieldError{{Message: "the definition must be a JSON object"}}}
	}
	if raw, ok := fields["name"]; ok {
		var n string
		if err := json.Unmarshal(raw, &n); err != nil || n != name {
			return nil, &agentapi.DefinitionError{Fields: []wire.FieldError{{Field: "name", Message: fmt.Sprintf("must be %q, the agent's name, or left out", name)}}}
		}
	}
	fields["name"], _ = json.Marshal(name)
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false) // a prompt keeps its <, > and &
	if err := enc.Encode(fields); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(b.Bytes(), []byte("\n")), nil
}

// Versions implements [agentapi.AgentRegistry].
func (r *Registry) Versions(ctx context.Context, name string) (wire.AgentVersionList, error) {
	if err := r.checkName(name, false); err != nil {
		return wire.AgentVersionList{}, err
	}
	it, err := r.st.GetItem(ctx, KindAgent, name)
	if err != nil {
		return wire.AgentVersionList{}, notFound(err)
	}
	vs, err := r.st.ListItemVersions(ctx, KindAgent, name)
	if err != nil {
		return wire.AgentVersionList{}, notFound(err)
	}
	out := wire.AgentVersionList{V: wire.Version, Name: name, Published: it.Published, Latest: it.Latest, Versions: make([]wire.AgentVersion, 0, len(vs))}
	for _, v := range vs {
		out.Versions = append(out.Versions, versionView(v, v.Version == it.Published))
	}
	return out, nil
}

// Version implements [agentapi.AgentRegistry].
func (r *Registry) Version(ctx context.Context, name string, version int64) (wire.AgentVersion, error) {
	if err := r.checkName(name, false); err != nil {
		return wire.AgentVersion{}, err
	}
	v, err := r.st.GetItemVersion(ctx, KindAgent, name, version)
	if err != nil {
		return wire.AgentVersion{}, notFound(err)
	}
	it, err := r.st.GetItem(ctx, KindAgent, name)
	if err != nil {
		return wire.AgentVersion{}, notFound(err)
	}
	return versionView(v, it.Published == version), nil
}

// Publish implements [agentapi.AgentRegistry]. The version is built again
// first: one that no longer builds (a toolset it names was taken out of
// the config file since) is refused, so new runs never get an agent that
// cannot run.
func (r *Registry) Publish(ctx context.Context, name string, version int64) (wire.AgentVersionList, error) {
	if err := r.checkName(name, true); err != nil {
		return wire.AgentVersionList{}, err
	}
	if _, err := r.Agent(ctx, name, version); err != nil {
		return wire.AgentVersionList{}, err
	}
	if _, err := r.st.PublishItem(ctx, KindAgent, name, version); err != nil {
		return wire.AgentVersionList{}, notFound(err)
	}
	return r.Versions(ctx, name)
}

// DeleteVersion implements [agentapi.AgentRegistry].
func (r *Registry) DeleteVersion(ctx context.Context, name string, version int64) error {
	if err := r.checkName(name, true); err != nil {
		return err
	}
	err := r.st.DeleteItemVersion(ctx, KindAgent, name, version)
	if errors.Is(err, store.ErrConflict) {
		return fmt.Errorf("%w: %w", agentapi.ErrVersionPublished, err)
	}
	return notFound(err)
}

// Agent implements [agentapi.AgentRegistry]: the agent built at version, or
// at its published version for 0, from the cache when it is there.
func (r *Registry) Agent(ctx context.Context, name string, version int64) (agentapi.Agent, error) {
	if err := r.checkName(name, false); err != nil {
		return agentapi.Agent{}, err
	}
	t, err := tenancy.Require(ctx)
	if err != nil {
		return agentapi.Agent{}, err
	}
	if version < 0 {
		return agentapi.Agent{}, fmt.Errorf("%w: version %d is negative", agentapi.ErrAgentInvalid, version)
	}
	if version == 0 {
		it, err := r.st.GetItem(ctx, KindAgent, name)
		if err != nil {
			return agentapi.Agent{}, notFound(err)
		}
		if it.Published == 0 {
			return agentapi.Agent{}, fmt.Errorf("%w: agent %q has no published version", agentapi.ErrAgentNotFound, name)
		}
		version = it.Published
	}
	k := cacheKey{tenant: t.ID(), name: name, version: version}
	if a, ok := r.cached(k); ok {
		return a, nil
	}
	v, err := r.st.GetItemVersion(ctx, KindAgent, name, version)
	if err != nil {
		return agentapi.Agent{}, notFound(err)
	}
	a, err := r.validate(ctx, name, v.Data)
	if err != nil {
		return agentapi.Agent{}, fmt.Errorf("agent %q version %d: %w", name, version, err)
	}
	a.Version = version
	r.keep(k, a)
	return a, nil
}

// Agents implements [agentapi.AgentRegistry]. A published version that no
// longer builds is left out, and logged.
func (r *Registry) Agents(ctx context.Context) ([]agentapi.Agent, error) {
	items, err := r.st.ListItems(ctx, KindAgent)
	if err != nil {
		return nil, err
	}
	var out []agentapi.Agent
	for _, it := range items {
		if it.Published == 0 || r.readOnly[it.Name] {
			continue
		}
		a, err := r.Agent(ctx, it.Name, it.Published)
		switch {
		case ctx.Err() != nil:
			return nil, ctx.Err()
		case err != nil:
			r.log.Warn("registry: a published agent does not build; it is left out", "agent", it.Name, "version", it.Published, "err", err)
			continue
		}
		out = append(out, a)
	}
	return out, nil
}

func (r *Registry) cached(k cacheKey) (agentapi.Agent, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.cache[k]
	if !ok {
		return agentapi.Agent{}, false
	}
	r.order.MoveToFront(e)
	return copyAgent(e.Value.(*cached).agent), true
}

func (r *Registry) keep(k cacheKey, a agentapi.Agent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if e, ok := r.cache[k]; ok { // built twice at once: keep the first
		r.order.MoveToFront(e)
		return
	}
	r.cache[k] = r.order.PushFront(&cached{key: k, agent: copyAgent(a)})
	for r.order.Len() > r.size {
		last := r.order.Back()
		delete(r.cache, last.Value.(*cached).key)
		r.order.Remove(last)
	}
}

// copyAgent copies what of an agent a caller might change.
func copyAgent(a agentapi.Agent) agentapi.Agent {
	a.Toolsets, a.RuntimeToolsets, a.Skills = slices.Clone(a.Toolsets), slices.Clone(a.RuntimeToolsets), slices.Clone(a.Skills)
	return a
}

// notFound turns the store's ErrNotFound into the API's.
func notFound(err error) error {
	if errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("%w: %w", agentapi.ErrAgentNotFound, err)
	}
	return err
}

func versionView(v store.ItemVersion, published bool) wire.AgentVersion {
	out := wire.AgentVersion{
		V: wire.Version, Name: v.Name, Version: v.Version, Published: published,
		Definition: slices.Clone(v.Data), Author: v.Author, Message: v.Message, CreatedAt: v.CreatedAt,
	}
	if !v.PublishedAt.IsZero() {
		at := v.PublishedAt
		out.PublishedAt = &at
	}
	return out
}
