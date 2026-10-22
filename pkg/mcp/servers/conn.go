package servers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
	"github.com/bsamanvith/agentkit/pkg/mcp/client"
	"github.com/bsamanvith/agentkit/pkg/netguard"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

// connKey names a toolset of a tenant.
type connKey struct {
	tenant tenancy.Tenant
	name   string
}

// conn is the connection to a toolset's server, made with the settings
// that hash to key. ready is closed once it is made or has failed (err).
type conn struct {
	key   string
	where string // the server, for logs
	ready chan struct{}
	err   error
	at    time.Time // when it was made or failed

	c      *client.Client
	server wire.ServerInfo

	mu      sync.Mutex
	tools   map[string]*listedTool // as last listed, by name
	listErr error                  // the last listing's, when it failed
	wrapped map[string]tool.Tool   // by fingerprint
}

// listedTool is a tool as the server lists it, with its fingerprint.
type listedTool struct {
	def *mcp.Tool
	fp  string
}

func (cn *conn) close() {
	if cn.c != nil {
		_ = cn.c.Close()
	}
}

// listed returns the tools as last listed. The map is the conn's: read it
// only.
func (cn *conn) listed() map[string]*listedTool {
	cn.mu.Lock()
	defer cn.mu.Unlock()
	return cn.tools
}

// setTools records a listing.
func (cn *conn) setTools(defs []*mcp.Tool) {
	tools := map[string]*listedTool{}
	for _, d := range defs {
		if d == nil {
			continue
		}
		fp, err := client.Fingerprint(d)
		if err != nil {
			continue // cannot be pinned, so never offered
		}
		tools[d.Name] = &listedTool{def: d, fp: fp}
	}
	cn.mu.Lock()
	cn.tools, cn.listErr = tools, nil
	cn.mu.Unlock()
}

// wrap returns the callable tool of a listed definition, made once per
// fingerprint.
func (cn *conn) wrap(t *listedTool) (tool.Tool, error) {
	cn.mu.Lock()
	defer cn.mu.Unlock()
	if w, ok := cn.wrapped[t.fp]; ok {
		return w, nil
	}
	w, err := cn.c.Wrap(t.def)
	if err != nil {
		return nil, err
	}
	if cn.wrapped == nil {
		cn.wrapped = map[string]tool.Tool{}
	}
	cn.wrapped[t.fp] = w
	return w, nil
}

// connFor returns the connection of a toolset of ctx's tenant with its
// settings r, connecting when there is none with these settings, or when
// the last attempt failed long enough ago. Callers waiting for one being
// made wait for it, or for ctx.
func (m *Manager) connFor(ctx context.Context, name string, r record) (*conn, error) {
	k, err := key(ctx, name)
	if err != nil {
		return nil, err
	}
	header, settings, err := m.settings(r)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, client.ErrClosed
	}
	cn := m.conns[k]
	if cn != nil && cn.key == settings && !cn.stale(m.clk.Now()) {
		m.mu.Unlock()
		select {
		case <-cn.ready:
			return cn, cn.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	fresh := &conn{key: settings, where: r.URL, ready: make(chan struct{})}
	m.conns[k] = fresh
	m.mu.Unlock()
	if cn != nil {
		go func() { <-cn.ready; cn.close() }()
	}
	m.connect(ctx, fresh, m.clientConfig(ctx, r, header))
	return fresh, fresh.err
}

// stale reports whether a connection that failed may be tried again.
func (cn *conn) stale(now time.Time) bool {
	select {
	case <-cn.ready:
		return cn.err != nil && now.Sub(cn.at) >= retryAfter
	default:
		return false
	}
}

// dial connects to a server for Add, outside the cache: [Manager.keep]
// puts the connection there once the toolset is stored.
func (m *Manager) dial(ctx context.Context, r record) (*conn, error) {
	header, settings, err := m.settings(r)
	if err != nil {
		return nil, err
	}
	cn := &conn{key: settings, where: r.URL, ready: make(chan struct{})}
	m.connect(ctx, cn, m.clientConfig(ctx, r, header))
	if cn.err != nil {
		return nil, cn.err
	}
	return cn, nil
}

// keep caches a connection made by dial, closing the one it replaces.
func (m *Manager) keep(k connKey, cn *conn) {
	m.mu.Lock()
	old := m.conns[k]
	if m.closed {
		m.mu.Unlock()
		cn.close()
		return
	}
	m.conns[k] = cn
	m.mu.Unlock()
	if old != nil {
		go func() { <-old.ready; old.close() }()
	}
}

// connect makes cn: connects, lists the tools, and watches for changes.
// It is bounded by the connect timeout, not by ctx, so a caller that gives
// up does not fail the others waiting.
func (m *Manager) connect(ctx context.Context, cn *conn, cfg client.Config) {
	defer close(cn.ready)
	sniff := cfg.HTTPClient.Transport.(*authSniffer)
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), m.connectTimeout)
	defer cancel()
	c, err := client.Connect(ctx, cfg)
	var defs []*mcp.Tool
	if err == nil {
		if defs, err = c.Tools(ctx); err != nil {
			_ = c.Close()
		}
	}
	cn.at = m.clk.Now()
	if err != nil {
		cn.err = connectError(err, sniff)
		m.log.Warn("mcp server: cannot connect", "server", cn.where, "err", err)
		return
	}
	cn.c = c
	cn.server.Name, cn.server.Version = c.ServerInfo()
	cn.setTools(defs)
	go m.watch(cn)
}

// watch lists the tools again each time the server says they changed.
func (m *Manager) watch(cn *conn) {
	for {
		select {
		case <-cn.c.ToolsChanged():
			ctx, cancel := context.WithTimeout(context.Background(), m.connectTimeout)
			defs, err := cn.c.Tools(ctx)
			cancel()
			if err != nil {
				cn.mu.Lock()
				cn.listErr = err
				cn.mu.Unlock()
				m.log.Warn("mcp server: the tools changed and could not be listed", "server", cn.where, "err", err)
				continue
			}
			cn.setTools(defs)
			m.log.Info("mcp server: the tool list changed", "server", cn.where, "tools", len(defs))
		case <-cn.c.Done():
			return
		}
	}
}

// connectError says why a connection failed, as the API shows it.
func connectError(err error, sniff *authSniffer) error {
	if challenge, ok := sniff.unauthorized(); ok {
		if strings.Contains(strings.ToLower(challenge), "resource_metadata") {
			return fmt.Errorf("%w: the server asks for MCP OAuth sign-in, which servers added at run time do not support yet (T095); a server that takes a key can be added with auth \"headers\"", agentapi.ErrAuthRequired)
		}
		return fmt.Errorf("%w: the server answered 401 Unauthorized; add it with auth \"headers\" and the header it expects", agentapi.ErrAuthRequired)
	}
	if errors.Is(err, agentapi.ErrURLNotAllowed) { // a redirect off the allowlist
		return err
	}
	if errors.Is(err, netguard.ErrBlocked) {
		return fmt.Errorf("%w: %w", agentapi.ErrToolsetInvalid, err)
	}
	return fmt.Errorf("%w: %w", agentapi.ErrServerUnreachable, err)
}

// authSniffer is the transport under the client's: it notes a 401 and its
// challenge, so a failed connection can say a credential is missing.
type authSniffer struct {
	base http.RoundTripper

	mu        sync.Mutex
	got401    bool
	challenge string
}

func (a *authSniffer) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := a.base.RoundTrip(req)
	if err == nil && resp.StatusCode == http.StatusUnauthorized {
		a.mu.Lock()
		a.got401, a.challenge = true, resp.Header.Get("WWW-Authenticate")
		a.mu.Unlock()
	}
	return resp, err
}

func (a *authSniffer) unauthorized() (string, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.challenge, a.got401
}
