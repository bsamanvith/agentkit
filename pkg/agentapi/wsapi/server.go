package wsapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
	"github.com/bsamanvith/agentkit/pkg/clock"
)

// Name is the component's name, in /readyz and logs.
const Name = "agent-ws"

// Defaults of the options.
const (
	DefaultHeartbeat    = 30 * time.Second
	DefaultPongTimeout  = 10 * time.Second
	DefaultHelloTimeout = 10 * time.Second
	DefaultWriteTimeout = 10 * time.Second
	DefaultQueue        = 256
	DefaultMaxStreams   = 64
	DefaultMaxCommands  = 16
	// DefaultMaxMessageBytes is the largest message a client may send:
	// the API's default body limit (1 MiB) and room for the envelope.
	DefaultMaxMessageBytes = 1<<20 + 4<<10
)

// ErrInvalidConfig reports an unusable argument to [New].
var ErrInvalidConfig = errors.New("invalid WebSocket binding configuration")

type config struct {
	log          *slog.Logger
	clock        clock.Clock
	heartbeat    time.Duration
	pongTimeout  time.Duration
	helloTimeout time.Duration
	writeTimeout time.Duration
	queue        int
	maxStreams   int
	maxCommands  int
	maxMessage   int64
	origins      []string
}

// Option configures [New].
type Option func(*config)

// WithLogger sets the logger (default: discard).
func WithLogger(l *slog.Logger) Option { return func(c *config) { c.log = l } }

// WithClock sets the clock of the heartbeats and the hello timeout (default
// the real one).
func WithClock(clk clock.Clock) Option { return func(c *config) { c.clock = clk } }

// WithHeartbeat pings each connection every d (0: never) and closes one
// whose pong does not come within timeout (defaults [DefaultHeartbeat],
// [DefaultPongTimeout]).
func WithHeartbeat(d, timeout time.Duration) Option {
	return func(c *config) { c.heartbeat, c.pongTimeout = d, timeout }
}

// WithHelloTimeout closes a connection whose hello has not come d after the
// upgrade (default [DefaultHelloTimeout]).
func WithHelloTimeout(d time.Duration) Option { return func(c *config) { c.helloTimeout = d } }

// WithWriteTimeout closes a connection one of whose messages could not be
// written in d (default [DefaultWriteTimeout]).
func WithWriteTimeout(d time.Duration) Option { return func(c *config) { c.writeTimeout = d } }

// WithQueue sets how many messages a connection holds for its client before
// the client counts as slow (default [DefaultQueue]).
func WithQueue(n int) Option { return func(c *config) { c.queue = n } }

// WithMaxStreams sets how many streams one connection may have open at once
// (default [DefaultMaxStreams]).
func WithMaxStreams(n int) Option { return func(c *config) { c.maxStreams = n } }

// WithMaxCommands sets how many commands that open no stream one connection
// may have waiting for their result (default [DefaultMaxCommands]).
func WithMaxCommands(n int) Option { return func(c *config) { c.maxCommands = n } }

// WithMaxMessageBytes sets the largest message a client may send (default
// [DefaultMaxMessageBytes]); a larger one closes the connection (1009).
func WithMaxMessageBytes(n int64) Option { return func(c *config) { c.maxMessage = n } }

// WithOriginPatterns allows pages of these other origins to connect (host
// patterns, as path.Match takes them). Pages of the server's own origin,
// and clients that send no Origin, may always connect.
func WithOriginPatterns(p ...string) Option {
	return func(c *config) { c.origins = append(c.origins, p...) }
}

// Server is the WebSocket binding of an agent API: a component that serves
// [wire.WSPath]. Build it with [New]; it is safe for concurrent use.
type Server struct {
	api http.Handler
	cfg config

	mu     sync.Mutex
	conns  map[*conn]struct{}
	closed bool
	wg     sync.WaitGroup // connections being served

	slow func() // tests: called when a client is found slow
}

// New returns the WebSocket binding of the API api serves (agentapi's
// Server.Handler, which serves /v1/). It fails with [ErrInvalidConfig] for a
// nil handler or an option with an unusable value.
func New(api http.Handler, opts ...Option) (*Server, error) {
	cfg := config{
		log: slog.New(slog.DiscardHandler), clock: clock.Real(),
		heartbeat: DefaultHeartbeat, pongTimeout: DefaultPongTimeout,
		helloTimeout: DefaultHelloTimeout, writeTimeout: DefaultWriteTimeout,
		queue: DefaultQueue, maxStreams: DefaultMaxStreams, maxCommands: DefaultMaxCommands,
		maxMessage: DefaultMaxMessageBytes,
	}
	for _, o := range opts {
		o(&cfg)
	}
	switch {
	case api == nil:
		return nil, fmt.Errorf("%w: nil API handler", ErrInvalidConfig)
	case cfg.log == nil, cfg.clock == nil:
		return nil, fmt.Errorf("%w: nil logger or clock", ErrInvalidConfig)
	case cfg.heartbeat < 0, cfg.heartbeat > 0 && cfg.pongTimeout <= 0:
		return nil, fmt.Errorf("%w: a negative heartbeat, or one without a pong timeout", ErrInvalidConfig)
	case cfg.helloTimeout <= 0, cfg.writeTimeout <= 0:
		return nil, fmt.Errorf("%w: the hello and write timeouts must be positive", ErrInvalidConfig)
	case cfg.queue < 1, cfg.maxStreams < 1, cfg.maxCommands < 1, cfg.maxMessage < 1:
		return nil, fmt.Errorf("%w: the queue, the limits and the message size must be at least 1", ErrInvalidConfig)
	}
	return &Server{api: api, cfg: cfg, conns: map[*conn]struct{}{}}, nil
}

// Name implements component.Component: [Name].
func (s *Server) Name() string { return Name }

// Handler serves the upgrade. Mount it at [wire.WSPath].
func (s *Server) Handler() http.Handler { return http.HandlerFunc(s.serve) }

// Ready implements component.Component: ready until [Server.Shutdown].
func (s *Server) Ready(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("shutting down")
	}
	return ctx.Err()
}

// Shutdown closes every connection (1001, going away) once what it holds
// for its client is written, refuses new ones, and waits for them to end,
// until ctx is done.
func (s *Server) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	s.closed = true
	for c := range s.conns {
		c.goAway()
	}
	s.mu.Unlock()
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		s.mu.Lock()
		for c := range s.conns {
			_ = c.ws.CloseNow()
		}
		s.mu.Unlock()
		return fmt.Errorf("WebSocket connections still open: %w", ctx.Err())
	}
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeError(w, http.StatusMethodNotAllowed, wire.CodeInvalidRequest, "the WebSocket endpoint takes GET with Upgrade: websocket")
		return
	}
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		w.Header().Set("Upgrade", "websocket")
		writeError(w, http.StatusUpgradeRequired, wire.CodeInvalidRequest, "the WebSocket endpoint takes GET with Upgrade: websocket")
		return
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		w.Header().Set("Retry-After", "5")
		writeError(w, http.StatusServiceUnavailable, wire.CodeShuttingDown, "the server is shutting down")
		return
	}
	s.wg.Add(1)
	s.mu.Unlock()
	defer s.wg.Done()

	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: s.cfg.origins})
	if err != nil { // Accept answered the request
		s.cfg.log.Info("websocket: upgrade refused", "err", err)
		return
	}
	ws.SetReadLimit(s.cfg.maxMessage)
	c := newConn(s, ws, r)
	s.mu.Lock()
	if s.closed { // Shutdown went by since the check
		s.mu.Unlock()
		_ = ws.Close(websocket.StatusGoingAway, "server shutting down")
		return
	}
	s.conns[c] = struct{}{}
	s.mu.Unlock()
	c.run()
	s.mu.Lock()
	delete(s.conns, c)
	s.mu.Unlock()
}
