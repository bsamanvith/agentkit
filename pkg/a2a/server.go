package a2a

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
	"github.com/bsamanvith/agentkit/pkg/agentclient"
	"github.com/bsamanvith/agentkit/pkg/clock"
)

// Name is the component's name, in /readyz and logs.
const Name = "a2a"

// PathPrefix is where the agents are served: /a2a/{agent}, the JSON-RPC
// endpoint and the root of the HTTP+JSON binding, with the agent's card at
// /a2a/{agent}/.well-known/agent-card.json.
const PathPrefix = "/a2a/"

// WellKnownPath is the card of the default agent, for discovery by the
// server's address alone.
const WellKnownPath = "/.well-known/agent-card.json"

// Defaults of the options.
const (
	DefaultMaxBodyBytes = 1 << 20
	DefaultKeepAlive    = 15 * time.Second
	DefaultMaxListScan  = 1000
	DefaultPageSize     = 50
	MaxPageSize         = 100
)

// writeTimeout bounds one write to a stream's client, so one that stopped
// reading is dropped instead of holding the stream.
const writeTimeout = 30 * time.Second

// ErrInvalidConfig reports an unusable argument to [New].
var ErrInvalidConfig = errors.New("invalid A2A server configuration")

// internalBase is the URL the in-process calls to the agent API are made
// on; they never leave the process.
const internalBase = "http://agent-api.internal"

// Server serves the agents of an agent API as A2A agents. It is the "a2a"
// component.Component of `agentkit serve`. Build it with [New].
type Server struct {
	api *agentclient.Client
	cfg config

	base   context.Context // the runs' followers; canceled by Shutdown
	cancel context.CancelFunc
	mu     sync.Mutex
	closed bool
	runs   sync.WaitGroup
}

type config struct {
	publicURL    string
	version      string
	provider     *AgentProvider
	schemes      map[string]SecurityScheme
	maxBodyBytes int64
	keepAlive    time.Duration
	maxListScan  int
	clock        clock.Clock
	log          *slog.Logger
}

// Option configures [New].
type Option func(*config)

// WithPublicURL sets the URL the server is reached at, such as
// "https://agents.example.com", which the cards name. Without it a card names
// the scheme and host of the request that fetched it.
func WithPublicURL(u string) Option {
	return func(c *config) { c.publicURL = strings.TrimRight(u, "/") }
}

// WithVersion sets the version the cards give the agents (default "dev"):
// the build's version.
func WithVersion(v string) Option { return func(c *config) { c.version = v } }

// WithProvider names the organization behind the agents in their cards.
func WithProvider(organization, url string) Option {
	return func(c *config) { c.provider = &AgentProvider{Organization: organization, URL: url} }
}

// WithSecuritySchemes sets the ways to authenticate the cards declare, by
// name; each one alone is enough. They must be what the agent API accepts
// (package auth): the server only describes them, the agent API checks the
// credentials.
func WithSecuritySchemes(s map[string]SecurityScheme) Option {
	return func(c *config) { c.schemes = s }
}

// WithMaxBodyBytes limits a request's body (default DefaultMaxBodyBytes).
func WithMaxBodyBytes(n int64) Option { return func(c *config) { c.maxBodyBytes = n } }

// WithKeepAlive sets how often a quiet stream gets an SSE comment (default
// DefaultKeepAlive); 0 turns it off.
func WithKeepAlive(d time.Duration) Option { return func(c *config) { c.keepAlive = d } }

// WithMaxListScan bounds the runs ListTasks reads to answer one request
// (default DefaultMaxListScan): it reads the agent's conversations newest
// first, and stops there.
func WithMaxListScan(n int) Option { return func(c *config) { c.maxListScan = n } }

// WithClock sets the clock of the keep-alive (default clock.Real).
func WithClock(clk clock.Clock) Option { return func(c *config) { c.clock = clk } }

// WithLogger sets the logger (default: discard). It logs internal errors.
func WithLogger(l *slog.Logger) Option { return func(c *config) { c.log = l } }

// New returns the A2A server of the agent API that api serves (the handler
// of package agentapi's Server, mounted at /v1/), called in process.
func New(api http.Handler, opts ...Option) (*Server, error) {
	cfg := config{
		version: "dev", maxBodyBytes: DefaultMaxBodyBytes, keepAlive: DefaultKeepAlive,
		maxListScan: DefaultMaxListScan, clock: clock.Real(), log: slog.New(slog.DiscardHandler),
	}
	for _, o := range opts {
		o(&cfg)
	}
	switch {
	case api == nil:
		return nil, fmt.Errorf("%w: no agent API", ErrInvalidConfig)
	case cfg.maxBodyBytes <= 0 || cfg.keepAlive < 0 || cfg.maxListScan <= 0:
		return nil, fmt.Errorf("%w: a limit is not positive", ErrInvalidConfig)
	case cfg.clock == nil || cfg.log == nil:
		return nil, fmt.Errorf("%w: nil clock or logger", ErrInvalidConfig)
	case cfg.version == "":
		return nil, fmt.Errorf("%w: empty version", ErrInvalidConfig)
	}
	if cfg.publicURL != "" {
		u, err := url.Parse(cfg.publicURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.RawQuery != "" || u.Fragment != "" {
			return nil, fmt.Errorf("%w: the public URL must be an absolute http(s) URL", ErrInvalidConfig)
		}
	}
	for name, sc := range cfg.schemes {
		n := 0
		for _, set := range []bool{sc.APIKey != nil, sc.HTTPAuth != nil, sc.OpenIDConnect != nil} {
			if set {
				n++
			}
		}
		if name == "" || n != 1 {
			return nil, fmt.Errorf("%w: security scheme %q must be named and be exactly one kind", ErrInvalidConfig, name)
		}
	}
	client, err := agentclient.New(internalBase,
		agentclient.WithHTTPClient(&http.Client{Transport: inProcess{api}}),
		agentclient.WithUserAgent("agentkit-a2a"))
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Server{api: client, cfg: cfg, base: ctx, cancel: cancel}, nil
}

// Name implements component.Component: "a2a".
func (s *Server) Name() string { return Name }

// Ready implements component.Component: the server is ready once built.
func (s *Server) Ready(ctx context.Context) error { return ctx.Err() }

// Shutdown waits for the runs started over A2A to end, which the agent
// API's own drain ends, until ctx is done; then it stops following them.
// Later messages are refused.
func (s *Server) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	done := make(chan struct{})
	go func() { s.runs.Wait(); close(done) }()
	select {
	case <-done:
		s.cancel()
		return nil
	case <-ctx.Done():
		s.cancel()
		<-done
		return ctx.Err()
	}
}

// Handler serves PathPrefix and WellKnownPath. Mount it at both.
func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(s.serve)
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == WellKnownPath {
		s.serveDirectory(w, r)
		return
	}
	rest, ok := strings.CutPrefix(r.URL.Path, PathPrefix)
	if !ok {
		http.NotFound(w, r)
		return
	}
	agent, sub, _ := strings.Cut(rest, "/")
	if sub != "" || strings.HasSuffix(rest, "/") {
		sub = "/" + sub
	}
	if agent == "" {
		http.NotFound(w, r)
		return
	}
	if sub == "/.well-known/agent-card.json" {
		s.serveCard(w, r, agent)
		return
	}
	jsonrpc := sub == "" || sub == "/"
	if err := checkVersion(r); err != nil {
		if jsonrpc {
			writeRPCError(w, nil, err)
		} else {
			writeRESTError(w, err)
		}
		return
	}
	if jsonrpc {
		s.serveJSONRPC(w, r, agent)
		return
	}
	s.serveREST(w, r, agent, sub)
}

// checkVersion refuses a request for another version of A2A than 1.0. A
// request that names none is taken as 1.0 (the spec reads it as 0.3, which
// this server does not speak: answering it in 1.0 is what serves the spec's
// own examples, which send no version).
func checkVersion(r *http.Request) error {
	v := r.Header.Get("A2A-Version")
	if v == "" {
		v = r.URL.Query().Get("A2A-Version")
	}
	if v == "" {
		return nil
	}
	parts := strings.Split(strings.TrimSpace(v), ".")
	if len(parts) >= 2 && parts[0] == "1" && parts[1] == "0" {
		return nil
	}
	return ErrVersionUnsupported.with("the A2A protocol version "+quote(v)+" is not supported; this agent speaks "+ProtocolVersion, "supportedVersions", ProtocolVersion)
}

func quote(s string) string {
	if len(s) > 64 {
		s = s[:64]
	}
	return fmt.Sprintf("%q", s)
}

// lookup returns the agent of that name, as the caller sees it: an agent
// the caller may not use is not found, as in the agent API. It reads that
// one agent (GET /v1/agents/{name}), not the whole catalog, whose every
// agent's toolsets the API would list.
func (s *Server) lookup(ctx context.Context, name string) (wire.Agent, error) {
	notFound := &httpError{status: http.StatusNotFound, msg: "no agent " + quote(name)}
	if !isID(name) {
		return wire.Agent{}, notFound
	}
	a, err := s.api.Agent(ctx, name)
	if err != nil {
		var ae *agentclient.APIError
		if ctx.Err() == nil && errors.As(err, &ae) && ae.Status == http.StatusNotFound {
			return wire.Agent{}, notFound
		}
		return wire.Agent{}, s.fromAPI(ctx, err, opAgent)
	}
	a.System = "" // never shown: the card describes the agent without it
	return a, nil
}

// isID reports whether s can name an agent or a run in a path of the agent
// API: not "." or "..", which the API's router would clean into another
// path (GET /v1/agents/. is the catalog, GET /v1/runs/. the list of runs).
// agentclient escapes everything else ("/", "?", "#", "%").
func isID(s string) bool { return s != "." && s != ".." }

// The card of an agent, and the directory.

func (s *Server) serveCard(w http.ResponseWriter, r *http.Request, name string) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeRESTError(w, &httpError{status: http.StatusMethodNotAllowed, msg: "use GET"})
		return
	}
	ctx, _ := withCaller(r.Context(), r)
	a, err := s.lookup(ctx, name)
	if err != nil {
		writeRESTError(w, err)
		return
	}
	s.writeCard(w, r, a)
}

func (s *Server) serveDirectory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeRESTError(w, &httpError{status: http.StatusMethodNotAllowed, msg: "use GET"})
		return
	}
	ctx, _ := withCaller(r.Context(), r)
	agents, err := s.api.Agents(ctx)
	if err != nil {
		writeRESTError(w, s.fromAPI(ctx, err, opAgent))
		return
	}
	for _, a := range agents {
		if a.Default {
			s.writeCard(w, r, a)
			return
		}
	}
	writeRESTError(w, &httpError{status: http.StatusNotFound, msg: "no default agent: fetch " + PathPrefix + "{agent}" + WellKnownPath})
}

func (s *Server) writeCard(w http.ResponseWriter, r *http.Request, a wire.Agent) {
	body, err := json.Marshal(s.card(r, a))
	if err != nil {
		writeRESTError(w, err)
		return
	}
	sum := sha256.Sum256(body)
	w.Header().Set("Content-Type", "application/json")
	// The card depends on the credential (the agents a key may use).
	w.Header().Set("Cache-Control", "private, max-age=300")
	w.Header().Set("Vary", "Authorization")
	w.Header().Set("ETag", `"`+hex.EncodeToString(sum[:16])+`"`)
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(body))
}

// card is the Agent Card of a: its interfaces, its skills (the agent
// itself, then one per toolset and one per Agent Skill), its capabilities and
// how to authenticate.
func (s *Server) card(r *http.Request, a wire.Agent) AgentCard {
	u := s.baseURL(r) + PathPrefix + a.Name
	desc := a.Description
	if desc == "" {
		desc = "The agentkit agent " + a.Name + "."
	}
	skills := []AgentSkill{{ID: a.Name, Name: a.Name, Description: desc, Tags: []string{"agent"}}}
	for _, ts := range a.Toolsets {
		tags := append([]string{"toolset"}, ts.Tools...)
		d := "The tools of the toolset " + ts.Name
		if len(ts.Tools) > 0 {
			d += ": " + strings.Join(ts.Tools, ", ")
		}
		skills = append(skills, AgentSkill{ID: "toolset:" + ts.Name, Name: ts.Name, Description: d + ".", Tags: tags})
	}
	for _, sk := range a.Skills {
		skills = append(skills, AgentSkill{ID: "skill:" + sk, Name: sk, Description: "The Agent Skill " + sk + ".", Tags: []string{"skill"}})
	}
	c := AgentCard{
		Name: a.Name, Description: desc,
		SupportedInterfaces: []AgentInterface{
			{URL: u, ProtocolBinding: BindingJSONRPC, ProtocolVersion: ProtocolVersion},
			{URL: u, ProtocolBinding: BindingHTTPJSON, ProtocolVersion: ProtocolVersion},
		},
		Provider:           s.cfg.provider,
		Version:            s.cfg.version,
		Capabilities:       AgentCapabilities{Streaming: true},
		DefaultInputModes:  []string{"text/plain", "application/json"},
		DefaultOutputModes: []string{"text/plain"},
		Skills:             skills,
	}
	if len(s.cfg.schemes) > 0 {
		c.SecuritySchemes = s.cfg.schemes
		for _, name := range slices.Sorted(maps.Keys(s.cfg.schemes)) {
			c.SecurityRequirements = append(c.SecurityRequirements, SecurityRequirement{Schemes: map[string]StringList{name: {List: []string{}}}})
		}
	}
	return c
}

func (s *Server) baseURL(r *http.Request) string {
	if s.cfg.publicURL != "" {
		return s.cfg.publicURL
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// readBody reads a request's body, at most the limit.
func (s *Server) readBody(r *http.Request) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r.Body, s.cfg.maxBodyBytes+1))
	if err != nil {
		return nil, ErrInvalidRequest.with("reading the body failed")
	}
	if int64(len(b)) > s.cfg.maxBodyBytes {
		return nil, ErrInvalidRequest.with(fmt.Sprintf("the body is larger than %d bytes", s.cfg.maxBodyBytes))
	}
	return b, nil
}
