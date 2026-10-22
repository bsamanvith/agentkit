package servers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
	"github.com/bsamanvith/agentkit/pkg/clock"
	"github.com/bsamanvith/agentkit/pkg/mcp/client"
	"github.com/bsamanvith/agentkit/pkg/mcp/oauth"
	mcpregistry "github.com/bsamanvith/agentkit/pkg/mcp/registry"
	"github.com/bsamanvith/agentkit/pkg/netguard"
	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

// Type is the toolset type of an MCP server added at run time.
const Type = client.Type

// Defaults for the options.
const (
	DefaultMaxToolsets    = 50
	DefaultConnectTimeout = 10 * time.Second
	// retryAfter is how long a server that could not be connected to is
	// left alone: until then its toolset reports the same failure.
	retryAfter = 30 * time.Second
)

var _ agentapi.ToolsetAdmin = (*Manager)(nil)

// Option configures [New].
type Option func(*Manager)

// WithGuard sets the guard every connection goes through (default:
// netguard.New with no allowed hosts, so only public https).
func WithGuard(g *netguard.Guard) Option { return func(m *Manager) { m.guard = g } }

// WithHeaderEnv names the variables of the server's environment a header
// value may use as "${NAME}", and how they are read (the binary passes
// os.LookupEnv). Without it no header may use one, so no header may carry
// a secret.
func WithHeaderEnv(names []string, lookup func(string) (string, bool)) Option {
	return func(m *Manager) {
		m.env = map[string]bool{}
		for _, n := range names {
			m.env[n] = true
		}
		m.lookupEnv = lookup
	}
}

// WithClock sets the clock of review times and retries (default: the real
// one).
func WithClock(c clock.Clock) Option { return func(m *Manager) { m.clk = c } }

// WithLogger sets the logger (default: none).
func WithLogger(l *slog.Logger) Option { return func(m *Manager) { m.log = l } }

// WithConnectTimeout bounds connecting to a server and listing its tools
// (default [DefaultConnectTimeout]).
func WithConnectTimeout(d time.Duration) Option { return func(m *Manager) { m.connectTimeout = d } }

// WithMaxToolsets caps the toolsets a tenant may add (default
// [DefaultMaxToolsets]).
func WithMaxToolsets(n int) Option { return func(m *Manager) { m.maxToolsets = n } }

// Manager keeps the MCP servers tenants add at run time, and the
// connections to them. Make one with [New] and close it when done.
type Manager struct {
	store          store.ToolsetStore
	guard          *netguard.Guard
	env            map[string]bool
	lookupEnv      func(string) (string, bool)
	clk            clock.Clock
	log            *slog.Logger
	connectTimeout time.Duration
	maxToolsets    int
	allow          *Allowlist
	registry       *mcpregistry.Client
	oauth          *oauth.Connector

	mu     sync.Mutex
	conns  map[connKey]*conn
	closed bool
}

// New returns a manager of the toolsets st keeps.
func New(st store.ToolsetStore, opts ...Option) (*Manager, error) {
	m := &Manager{
		store: st, env: map[string]bool{}, lookupEnv: func(string) (string, bool) { return "", false },
		clk: clock.Real(), connectTimeout: DefaultConnectTimeout, maxToolsets: DefaultMaxToolsets,
		conns: map[connKey]*conn{},
	}
	for _, o := range opts {
		o(m)
	}
	switch {
	case st == nil:
		return nil, errors.New("servers: no store")
	case m.connectTimeout <= 0 || m.maxToolsets <= 0:
		return nil, errors.New("servers: the connect timeout and the maximum of toolsets must be positive")
	}
	if m.guard == nil {
		g, err := netguard.New(nil)
		if err != nil {
			return nil, err
		}
		m.guard = g
	}
	if m.log == nil {
		m.log = slog.New(slog.DiscardHandler)
	}
	return m, nil
}

// Close closes every connection. The manager is not used after.
func (m *Manager) Close() error {
	m.mu.Lock()
	conns := slices.Collect(maps.Values(m.conns))
	m.conns, m.closed = map[connKey]*conn{}, true
	m.mu.Unlock()
	for _, cn := range conns {
		cn.close()
	}
	return nil
}

// record is a toolset as stored: store.ToolsetRecord.Data.
type record struct {
	Type      string            `json:"type"`
	URL       string            `json:"url"`
	Transport string            `json:"transport"`
	Auth      string            `json:"auth"`
	Headers   map[string]string `json:"headers,omitempty"` // values as given: fixed, or "${NAME}"
	Scope     string            `json:"scope"`
	Enabled   bool              `json:"enabled"`
	// Issuer is the authorization server of auth "oauth", pinned when the
	// toolset was added; OAuthClientID and OAuthClientSecret ("${NAME}")
	// a pre-registered client.
	Issuer            string          `json:"issuer,omitempty"`
	OAuthClientID     string          `json:"oauth_client_id,omitempty"`
	OAuthClientSecret string          `json:"oauth_client_secret,omitempty"`
	Server            wire.ServerInfo `json:"server"`
	// Tools are the decisions on tools, by the server's name.
	Tools map[string]review `json:"tools"`
}

// review is the decision on one tool. Approved and Rejected are
// fingerprints; at most one is set.
type review struct {
	Approved string `json:"approved,omitempty"`
	// Definition is the approved definition, as the server sent it, for
	// the diff once it changes.
	Definition json.RawMessage `json:"definition,omitempty"`
	Rejected   string          `json:"rejected,omitempty"`
	Kind       tool.Kind       `json:"kind,omitempty"`
	Risk       tool.Risk       `json:"risk,omitempty"`
	By         string          `json:"by,omitempty"`
	At         time.Time       `json:"at,omitzero"`
}

// load reads a toolset of ctx's tenant.
func (m *Manager) load(ctx context.Context, name string) (store.ToolsetRecord, record, error) {
	rec, err := m.store.GetToolset(ctx, name)
	switch {
	case errors.Is(err, store.ErrNotFound), errors.Is(err, store.ErrInvalid):
		return rec, record{}, fmt.Errorf("%w: %q", agentapi.ErrToolsetNotFound, name)
	case err != nil:
		return rec, record{}, err
	}
	r, err := decode(rec.Data)
	if err != nil {
		return rec, record{}, fmt.Errorf("toolset %q: stored data: %w", name, err)
	}
	return rec, r, nil
}

// decode reads a stored record.
func decode(data []byte) (record, error) {
	var r record
	if err := json.Unmarshal(data, &r); err != nil {
		return record{}, err
	}
	if r.Tools == nil {
		r.Tools = map[string]review{}
	}
	return r, nil
}

// save writes r over version ifVersion of the toolset (0: a new one).
func (m *Manager) save(ctx context.Context, name string, r record, ifVersion int64) (store.ToolsetRecord, error) {
	data, err := json.Marshal(r)
	if err != nil {
		return store.ToolsetRecord{}, err
	}
	rec, err := m.store.PutToolset(ctx, store.ToolsetRecord{Name: name, Data: data}, ifVersion)
	switch {
	case errors.Is(err, store.ErrConflict) && ifVersion == 0:
		return rec, fmt.Errorf("%w: %q", agentapi.ErrToolsetExists, name)
	case errors.Is(err, store.ErrConflict), errors.Is(err, store.ErrNotFound):
		return rec, fmt.Errorf("%w: toolset %q was changed or removed meanwhile; read it again", agentapi.ErrToolsetConflict, name)
	case errors.Is(err, store.ErrInvalid):
		return rec, fmt.Errorf("%w: toolset %q cannot be stored: %w", agentapi.ErrToolsetInvalid, name, err)
	}
	return rec, err
}

// Add implements [agentapi.ToolsetAdmin]: it checks the request, connects
// to the server, lists its tools and stores the toolset, enabled, with
// every tool new.
func (m *Manager) Add(ctx context.Context, by string, req wire.AddToolsetRequest) (wire.ServerToolset, error) {
	r, err := m.checkAdd(req)
	if err != nil {
		return wire.ServerToolset{}, err
	}
	if err := m.checkAllowed(ctx, r.URL); err != nil {
		return wire.ServerToolset{}, err
	}
	all, err := m.store.ListToolsets(ctx)
	if err != nil {
		return wire.ServerToolset{}, err
	}
	switch {
	case slices.ContainsFunc(all, func(t store.ToolsetRecord) bool { return t.Name == req.Name }):
		return wire.ServerToolset{}, fmt.Errorf("%w: %q", agentapi.ErrToolsetExists, req.Name)
	case len(all) >= m.maxToolsets:
		return wire.ServerToolset{}, fmt.Errorf("%w: a tenant may add %d toolsets; remove one first", agentapi.ErrToolsetInvalid, m.maxToolsets)
	}
	if r.Auth == wire.AuthOAuth {
		return m.addOAuth(ctx, by, req.Name, r)
	}
	k, err := key(ctx, req.Name)
	if err != nil {
		return wire.ServerToolset{}, err
	}
	// The probe's connection is kept for the toolset once it is stored.
	cn, err := m.dial(ctx, r)
	if err != nil {
		return wire.ServerToolset{}, err
	}
	r.Server = cn.server
	rec, err := m.save(ctx, req.Name, r, 0)
	if err != nil {
		cn.close()
		return wire.ServerToolset{}, err
	}
	m.keep(k, cn)
	m.log.Info("mcp server added", "tenant", k.tenant.ID(), "toolset", req.Name, "server", cn.where, "by", by)
	return m.show(ctx, rec, r, cn, nil), nil
}

// List implements [agentapi.ToolsetAdmin]. It connects to the servers it
// has no connection to yet, at once: a server that does not answer is
// unavailable, not an error.
func (m *Manager) List(ctx context.Context) ([]wire.ServerToolset, error) {
	all, err := m.store.ListToolsets(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]wire.ServerToolset, len(all))
	var wg sync.WaitGroup
	for i, rec := range all {
		wg.Go(func() {
			r, err := decode(rec.Data)
			if err != nil {
				m.log.Error("mcp server: stored data", "toolset", rec.Name, "err", err)
				out[i] = wire.ServerToolset{V: agentapi.WireVersion, Name: rec.Name, Type: Type, Status: wire.ToolsetUnavailable, Reason: "its stored data cannot be read; see the server log", Tools: []wire.ReviewTool{}}
				return
			}
			cn, err := m.connFor(ctx, rec.Name, r)
			out[i] = m.show(ctx, rec, r, cn, err)
		})
	}
	wg.Wait()
	return out, ctx.Err()
}

// Get implements [agentapi.ToolsetAdmin].
func (m *Manager) Get(ctx context.Context, name string) (wire.ServerToolset, error) {
	rec, r, err := m.load(ctx, name)
	if err != nil {
		return wire.ServerToolset{}, err
	}
	cn, err := m.connFor(ctx, name, r)
	if ctx.Err() != nil {
		return wire.ServerToolset{}, ctx.Err()
	}
	return m.show(ctx, rec, r, cn, err), nil
}

// Review implements [agentapi.ToolsetAdmin]. Each decision names the
// fingerprint of the definition it is about, which must be the one the
// server lists now ([agentapi.ErrToolsetConflict] otherwise). Rejecting a
// tool the server no longer lists, with no fingerprint, forgets it.
func (m *Manager) Review(ctx context.Context, name, by string, req wire.ReviewRequest) (wire.ServerToolset, error) {
	rec, r, err := m.load(ctx, name)
	if err != nil {
		return wire.ServerToolset{}, err
	}
	if len(req.Tools) == 0 {
		return wire.ServerToolset{}, fmt.Errorf("%w: no decisions", agentapi.ErrToolsetInvalid)
	}
	cn, err := m.connFor(ctx, name, r)
	var ce *connectionError
	if errors.As(err, &ce) {
		// The tools are listed, and so reviewed, through a connection.
		return wire.ServerToolset{}, &agentapi.AuthRequiredError{Auth: wire.AuthOAuth, Reason: ce.Error() + "; its tools are reviewed through a connection"}
	} else if err != nil {
		return wire.ServerToolset{}, err
	}
	live := cn.listed()
	now := m.clk.Now().UTC()
	seen := map[string]bool{}
	for _, d := range req.Tools {
		if seen[d.Name] {
			return wire.ServerToolset{}, fmt.Errorf("%w: two decisions on tool %q", agentapi.ErrToolsetInvalid, d.Name)
		}
		seen[d.Name] = true
		next, err := decide(d, live[d.Name], r.Tools[d.Name])
		if err != nil {
			return wire.ServerToolset{}, err
		}
		if next == nil {
			delete(r.Tools, d.Name)
			continue
		}
		next.By, next.At = by, now
		r.Tools[d.Name] = *next
	}
	rec, err = m.save(ctx, name, r, rec.Version)
	if err != nil {
		return wire.ServerToolset{}, err
	}
	return m.show(ctx, rec, r, cn, nil), nil
}

// decide applies a decision on a tool the server lists as t (nil: it does
// not) to its review so far, old. A nil result forgets the tool.
func decide(d wire.ToolDecision, t *listedTool, old review) (*review, error) {
	switch {
	case t == nil && old.Approved == "" && old.Rejected == "":
		return nil, fmt.Errorf("%w: the server lists no tool %q", agentapi.ErrToolsetInvalid, d.Name)
	case t == nil && d.Decision == wire.DecisionReject && d.Fingerprint == "":
		return nil, nil // a removed tool, forgotten
	case t == nil:
		return nil, fmt.Errorf("%w: the server no longer lists tool %q; reject it with no fingerprint to forget it", agentapi.ErrToolsetConflict, d.Name)
	case d.Fingerprint != t.fp:
		return nil, fmt.Errorf("%w: tool %q is now %s, not %q: review it again", agentapi.ErrToolsetConflict, d.Name, t.fp, d.Fingerprint)
	}
	switch d.Decision {
	case wire.DecisionApprove:
		kind, risk := tool.Kind(d.Kind), tool.Risk(d.Risk)
		if kind != "" && !slices.Contains(tool.Kinds(), kind) {
			return nil, fmt.Errorf("%w: tool %q: kind %q is not one of %v", agentapi.ErrToolsetInvalid, d.Name, d.Kind, tool.Kinds())
		}
		if risk != "" && !slices.Contains(tool.Risks(), risk) {
			return nil, fmt.Errorf("%w: tool %q: risk %q is not one of %v", agentapi.ErrToolsetInvalid, d.Name, d.Risk, tool.Risks())
		}
		def, err := json.Marshal(t.def)
		if err != nil {
			return nil, err
		}
		return &review{Approved: t.fp, Definition: def, Kind: kind, Risk: risk}, nil
	case wire.DecisionReject:
		if d.Kind != "" || d.Risk != "" {
			return nil, fmt.Errorf("%w: tool %q: a rejected tool has no kind or risk", agentapi.ErrToolsetInvalid, d.Name)
		}
		return &review{Rejected: t.fp}, nil
	}
	return nil, fmt.Errorf("%w: tool %q: decision %q is not %q or %q", agentapi.ErrToolsetInvalid, d.Name, d.Decision, wire.DecisionApprove, wire.DecisionReject)
}

// Update implements [agentapi.ToolsetAdmin]. New headers take effect on
// the next use: the toolset connects again.
func (m *Manager) Update(ctx context.Context, name string, req wire.UpdateToolsetRequest) (wire.ServerToolset, error) {
	rec, r, err := m.load(ctx, name)
	if err != nil {
		return wire.ServerToolset{}, err
	}
	if req.Enabled == nil && req.Headers == nil {
		return wire.ServerToolset{}, fmt.Errorf("%w: nothing to change (enabled, headers)", agentapi.ErrToolsetInvalid)
	}
	if req.Enabled != nil {
		r.Enabled = *req.Enabled
	}
	if req.Headers != nil && r.Auth == wire.AuthOAuth {
		return wire.ServerToolset{}, fmt.Errorf(`%w: a toolset with auth "oauth" takes no headers`, agentapi.ErrToolsetInvalid)
	}
	if req.Headers != nil {
		if err := m.checkHeaders(req.Headers); err != nil {
			return wire.ServerToolset{}, err
		}
		r.Headers, r.Auth = req.Headers, wire.AuthHeaders
		if len(req.Headers) == 0 {
			r.Headers, r.Auth = nil, wire.AuthNone
		}
	}
	rec, err = m.save(ctx, name, r, rec.Version)
	if err != nil {
		return wire.ServerToolset{}, err
	}
	cn, err := m.connFor(ctx, name, r)
	return m.show(ctx, rec, r, cn, err), nil
}

// Delete implements [agentapi.ToolsetAdmin], and closes the toolset's
// connection.
func (m *Manager) Delete(ctx context.Context, name string) error {
	k, err := key(ctx, name)
	if err != nil {
		return err
	}
	err = m.store.DeleteToolset(ctx, name, 0)
	switch {
	case errors.Is(err, store.ErrNotFound), errors.Is(err, store.ErrInvalid):
		return fmt.Errorf("%w: %q", agentapi.ErrToolsetNotFound, name)
	case err != nil:
		return err
	}
	// Every connection of the toolset: the tenant's, and each user's.
	m.mu.Lock()
	var gone []*conn
	for ck, cn := range m.conns {
		if ck.tenant == k.tenant && ck.name == k.name {
			gone = append(gone, cn)
			delete(m.conns, ck)
		}
	}
	m.mu.Unlock()
	for _, cn := range gone {
		go func() { <-cn.ready; cn.close() }()
	}
	return nil
}

// Tools implements [agentapi.ToolsetAdmin]: the definitions agents are
// offered, under the server's names, sorted.
func (m *Manager) Tools(ctx context.Context, name string) ([]tool.Definition, error) {
	offered, err := m.offered(ctx, name)
	if err != nil {
		return nil, err
	}
	defs := make([]tool.Definition, len(offered))
	for i, o := range offered {
		defs[i] = o.def
	}
	return defs, nil
}

// key is the connection key of a toolset of ctx's tenant.
func key(ctx context.Context, name string) (connKey, error) {
	t, err := tenancy.Require(ctx)
	if err != nil {
		return connKey{}, err
	}
	return connKey{tenant: t, name: name}, nil
}
