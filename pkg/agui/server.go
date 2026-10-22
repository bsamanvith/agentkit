package agui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
	"github.com/bsamanvith/agentkit/pkg/agentclient"
	"github.com/bsamanvith/agentkit/pkg/clock"
)

// Name is the component's name, in /readyz and logs.
const Name = "agui"

// PathPrefix is where the agents are served: POST /agui/{agent}.
const PathPrefix = "/agui/"

// Defaults of the options.
const (
	// DefaultMaxBodyBytes: an AG-UI client sends the whole conversation
	// with every run.
	DefaultMaxBodyBytes = 4 << 20
	DefaultKeepAlive    = 15 * time.Second
)

// writeTimeout bounds one write to a stream's client, so one that stopped
// reading is dropped instead of holding the stream.
const writeTimeout = 30 * time.Second

// ErrInvalidConfig reports an unusable argument to [New].
var ErrInvalidConfig = errors.New("invalid AG-UI server configuration")

// internalBase is the URL the in-process calls to the agent API are made
// on; they never leave the process.
const internalBase = "http://agent-api.internal"

// Server serves the agents of an agent API to AG-UI clients. It is the
// "agui" component.Component of `agentkit serve`. Build it with [New].
type Server struct {
	api *agentclient.Client
	cfg config

	base   context.Context // canceled by Shutdown once its time is up
	cancel context.CancelFunc
	mu     sync.Mutex
	closed bool
	active sync.WaitGroup
}

type config struct {
	maxBodyBytes int64
	keepAlive    time.Duration
	clock        clock.Clock
	log          *slog.Logger
}

// Option configures [New].
type Option func(*config)

// WithMaxBodyBytes limits a request's body (default DefaultMaxBodyBytes).
func WithMaxBodyBytes(n int64) Option { return func(c *config) { c.maxBodyBytes = n } }

// WithKeepAlive sets how often a quiet stream gets an SSE comment (default
// DefaultKeepAlive); 0 turns it off.
func WithKeepAlive(d time.Duration) Option { return func(c *config) { c.keepAlive = d } }

// WithClock sets the clock of the keep-alive and of the timestamps of the
// events the server makes up (default clock.Real).
func WithClock(clk clock.Clock) Option { return func(c *config) { c.clock = clk } }

// WithLogger sets the logger (default: discard). It logs internal errors.
func WithLogger(l *slog.Logger) Option { return func(c *config) { c.log = l } }

// New returns the AG-UI server of the agent API that api serves (the
// handler of package agentapi's Server, mounted at /v1/), called in process.
func New(api http.Handler, opts ...Option) (*Server, error) {
	cfg := config{maxBodyBytes: DefaultMaxBodyBytes, keepAlive: DefaultKeepAlive, clock: clock.Real(), log: slog.New(slog.DiscardHandler)}
	for _, o := range opts {
		o(&cfg)
	}
	switch {
	case api == nil:
		return nil, fmt.Errorf("%w: no agent API", ErrInvalidConfig)
	case cfg.maxBodyBytes <= 0 || cfg.keepAlive < 0:
		return nil, fmt.Errorf("%w: a limit is not positive", ErrInvalidConfig)
	case cfg.clock == nil || cfg.log == nil:
		return nil, fmt.Errorf("%w: nil clock or logger", ErrInvalidConfig)
	}
	client, err := agentclient.New(internalBase,
		agentclient.WithHTTPClient(&http.Client{Transport: inProcess{api}}),
		agentclient.WithUserAgent("agentkit-agui"))
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Server{api: client, cfg: cfg, base: ctx, cancel: cancel}, nil
}

// Name implements component.Component: "agui".
func (s *Server) Name() string { return Name }

// Ready implements component.Component: the server is ready once built.
func (s *Server) Ready(ctx context.Context) error { return ctx.Err() }

// Shutdown refuses new requests and waits for the streams going on to end,
// which the agent API's own drain ends, until ctx is done; then it ends them.
func (s *Server) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	done := make(chan struct{})
	go func() { s.active.Wait(); close(done) }()
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

// Handler serves PathPrefix: POST /agui/{agent} with a RunAgentInput, which
// answers with the run's AG-UI events as Server-Sent Events.
func (s *Server) Handler() http.Handler { return http.HandlerFunc(s.serve) }

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	agent, ok := strings.CutPrefix(r.URL.Path, PathPrefix)
	if !ok || agent == "" || strings.Contains(agent, "/") {
		writeError(w, http.StatusNotFound, wire.CodeNotFound, "not found: POST "+PathPrefix+"{agent}")
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeError(w, http.StatusMethodNotAllowed, wire.CodeInvalidRequest, "use POST with a RunAgentInput")
		return
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		writeError(w, http.StatusServiceUnavailable, wire.CodeShuttingDown, "the server is shutting down")
		return
	}
	s.active.Add(1)
	s.mu.Unlock()
	defer s.active.Done()
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	stop := context.AfterFunc(s.base, cancel)
	defer stop()
	ctx, _ = withCaller(ctx, r)

	in, err := s.readInput(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, wire.CodeInvalidRequest, err.Error())
		return
	}
	// The agent first, as the caller sees it: a credential the API refuses,
	// or an agent it may not use, is an HTTP error before any event.
	ag, err := s.api.Agent(ctx, agent)
	if err != nil {
		s.writeAPIError(ctx, w, err)
		return
	}
	o := &sseOut{w: w, rc: http.NewResponseController(w)}
	o.open()
	stopKeepAlive := s.keepAlive(o)
	defer stopKeepAlive()
	em := &emitter{out: o, in: in, clk: s.cfg.clock}
	err = s.run(ctx, ag, in, em)
	switch {
	case err == nil, errors.Is(err, errClientGone):
	case ctx.Err() != nil && s.base.Err() == nil:
		// The client went away: nobody reads what would be said.
	default:
		var re *runError
		if !errors.As(err, &re) {
			s.cfg.log.Error("agui: a run", "agent", agent, "error", err)
			re = &runError{code: wire.CodeInternal, msg: "the run failed; see the server log"}
		}
		_ = em.fail(re)
	}
	o.mu.Lock()
	_ = o.rc.SetWriteDeadline(time.Time{})
	o.mu.Unlock()
}

// readInput reads and checks a request's RunAgentInput: the fields the
// spec requires, and a protocol version this server speaks.
func (s *Server) readInput(r *http.Request) (*RunAgentInput, error) {
	b, err := io.ReadAll(io.LimitReader(r.Body, s.cfg.maxBodyBytes+1))
	if err != nil {
		return nil, errors.New("reading the body failed")
	}
	if int64(len(b)) > s.cfg.maxBodyBytes {
		return nil, fmt.Errorf("the body is larger than %d bytes", s.cfg.maxBodyBytes)
	}
	var in RunAgentInput
	if err := json.Unmarshal(b, &in); err != nil {
		return nil, fmt.Errorf("the body is not a RunAgentInput: %w", err)
	}
	switch {
	case in.ThreadID == "" || in.RunID == "":
		return nil, errors.New("the RunAgentInput needs a threadId and a runId")
	case in.Messages == nil:
		return nil, errors.New("the RunAgentInput needs messages (an empty list is fine)")
	case in.ProtocolVersion != "" && !strings.HasPrefix(in.ProtocolVersion+".", "1."):
		return nil, fmt.Errorf("AG-UI protocol version %q is not supported: this server speaks %s", in.ProtocolVersion, ProtocolVersion)
	}
	return &in, nil
}

// writeAPIError answers a request the agent API refused before any event:
// with the API's status, code and message, and its WWW-Authenticate.
func (s *Server) writeAPIError(ctx context.Context, w http.ResponseWriter, err error) {
	var ae *agentclient.APIError
	if !errors.As(err, &ae) || ae.Status < 400 {
		if ctx.Err() == nil {
			s.cfg.log.Error("agui: calling the agent API", "error", err)
		}
		writeError(w, http.StatusInternalServerError, wire.CodeInternal, "calling the agent failed; see the server log")
		return
	}
	if ae.Status == http.StatusUnauthorized {
		if c := callerFrom(ctx); c != nil && *c.challenge != "" {
			w.Header().Set("WWW-Authenticate", *c.challenge)
		}
	}
	if ae.RetryAfter > 0 {
		w.Header().Set("Retry-After", fmt.Sprint(int(ae.RetryAfter.Seconds()+0.5)))
	}
	code := ae.Code
	if code == "" {
		code = wire.CodeInternal
	}
	writeError(w, ae.Status, code, ae.Message)
}

// writeError writes the agent API's error body.
func writeError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(wire.ErrorResponse{V: wire.Version, Error: &wire.Error{Code: code, Message: msg}})
}

// The stream.

// sseOut writes one response's Server-Sent Events.
type sseOut struct {
	w   http.ResponseWriter
	rc  *http.ResponseController
	mu  sync.Mutex
	err error
}

// errClientGone: a write to the stream's client failed.
var errClientGone = errors.New("the stream's client went away")

func (o *sseOut) open() {
	h := o.w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	o.w.WriteHeader(http.StatusOK)
	_ = o.rc.Flush()
}

func (o *sseOut) send(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return o.write("data: " + string(b) + "\n\n")
}

func (o *sseOut) write(msg string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.err != nil {
		return o.err
	}
	if err := o.rc.SetWriteDeadline(time.Now().Add(writeTimeout)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		o.err = errClientGone
		return o.err
	}
	if _, err := o.w.Write([]byte(msg)); err != nil {
		o.err = errClientGone
		return o.err
	}
	if err := o.rc.Flush(); err != nil {
		o.err = errClientGone
	}
	return o.err
}

// keepAlive writes an SSE comment to o on every tick until the returned
// function is called, which returns once nothing more will be written. AG-UI
// clients read only data lines, so a comment is never an event.
func (s *Server) keepAlive(o *sseOut) func() {
	if s.cfg.keepAlive == 0 {
		return func() {}
	}
	t := s.cfg.clock.NewTicker(s.cfg.keepAlive)
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-t.C():
				_ = o.write(": keep-alive\n\n")
			case <-stop:
				return
			}
		}
	}()
	return func() {
		t.Stop()
		close(stop)
		<-done
	}
}
