package rpc

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Headers of the protocol.
const (
	// TimeoutHeader carries the caller's deadline, in milliseconds from now.
	TimeoutHeader = "Agentkit-Timeout"
	// ContentType is the type of every answer: newline-delimited frames.
	ContentType = "application/x-ndjson"
)

// DefaultMaxRequestBytes is the largest request a server reads, unless
// [WithMaxRequestBytes] says otherwise: a run's request holds its transcript.
const DefaultMaxRequestBytes = 32 << 20

// Server serves methods ([Unary.Handle], [Stream.Handle]) over HTTP, as an
// http.Handler, and in process through [Pipe]. Register every method before
// serving: a Server is not safe for registration and calls at once.
type Server struct {
	methods map[string]*handler
	cfg     serverConfig
	// closing is canceled by [Server.Shutdown]: it ends calls in flight.
	closing context.Context
	shut    context.CancelFunc
}

type handler struct {
	codes []Code
	serve func(ctx context.Context, req json.RawMessage, send func(any) error) error
}

// ServerOption configures [NewServer].
type ServerOption func(*serverConfig)

type serverConfig struct {
	token    string
	maxBytes int64
	log      *slog.Logger
}

// WithToken makes the server refuse calls without "Authorization: Bearer
// token" ([ErrUnauthenticated]). Empty means no token is needed.
func WithToken(token string) ServerOption { return func(c *serverConfig) { c.token = token } }

// WithMaxRequestBytes limits the size of a request (default
// [DefaultMaxRequestBytes]).
func WithMaxRequestBytes(n int64) ServerOption { return func(c *serverConfig) { c.maxBytes = n } }

// WithLogger logs handler panics and calls that fail in the transport
// (default: nothing is logged).
func WithLogger(l *slog.Logger) ServerOption { return func(c *serverConfig) { c.log = l } }

// NewServer returns a server with no methods.
func NewServer(opts ...ServerOption) *Server {
	cfg := serverConfig{maxBytes: DefaultMaxRequestBytes, log: slog.New(slog.DiscardHandler)}
	for _, o := range opts {
		o(&cfg)
	}
	if cfg.maxBytes <= 0 {
		cfg.maxBytes = DefaultMaxRequestBytes
	}
	if cfg.log == nil {
		cfg.log = slog.New(slog.DiscardHandler)
	}
	closing, shut := context.WithCancel(context.Background())
	return &Server{methods: map[string]*handler{}, cfg: cfg, closing: closing, shut: shut}
}

// errShuttingDown is what calls cut by [Server.Shutdown] fail with.
var errShuttingDown = fmt.Errorf("%w: the server is shutting down", ErrUnavailable)

// Shutdown cancels the calls in flight, which fail with [ErrUnavailable] as
// if the connection had dropped, and refuses new ones the same way: callers
// retry on another replica. It does not wait for the handlers to return; the
// HTTP server's own shutdown does.
func (s *Server) Shutdown() { s.shut() }

var validMethod = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

func (s *Server) handle(name string, codes []Code, serve func(context.Context, json.RawMessage, func(any) error) error) {
	if !validMethod.MatchString(name) {
		panic(fmt.Sprintf("rpc: method name %q must be service/Method", name))
	}
	if s.methods[name] != nil {
		panic(fmt.Sprintf("rpc: method %q registered twice", name))
	}
	s.methods[name] = &handler{codes: codes, serve: serve}
}

// Methods returns the names of the methods s serves, sorted.
func (s *Server) Methods() []string {
	return slices.Sorted(maps.Keys(s.methods))
}

// maxTimeoutMillis bounds the timeout header, so it cannot overflow a
// time.Duration: a year is as good as no deadline.
const maxTimeoutMillis = int64(365 * 24 * time.Hour / time.Millisecond)

// errStop means the caller stopped listening.
var errStop = errors.New("rpc: caller stopped listening")

// dispatch runs the named method on req, calling send with each message's
// JSON. It returns the error to send in place of the end frame, or nil.
func (s *Server) dispatch(ctx context.Context, name string, req json.RawMessage, send func(json.RawMessage) error) (werr *Error) {
	h := s.methods[name]
	if h == nil {
		return encodeError(fmt.Errorf("%w %q", ErrUnknownMethod, name), nil)
	}
	if s.closing.Err() != nil {
		return encodeError(errShuttingDown, nil)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer context.AfterFunc(s.closing, cancel)()
	defer func() {
		// Whatever a cut handler returned (its context's error, say), the
		// caller is told the server went away.
		if werr != nil && s.closing.Err() != nil {
			werr = encodeError(errShuttingDown, nil)
		}
	}()
	defer func() {
		if v := recover(); v != nil {
			s.cfg.log.Error("rpc handler panicked", "method", name, "panic", v)
			werr = encodeError(fmt.Errorf("%w: method %s panicked", ErrInternal, name), nil)
		}
	}()
	err := h.serve(ctx, req, func(msg any) error {
		raw, err := json.Marshal(msg)
		if err != nil {
			return fmt.Errorf("rpc %s: message: %w", name, err)
		}
		return send(raw)
	})
	if err == nil {
		return nil
	}
	return encodeError(err, h.codes)
}

// frame is one line of an answer: a message, the end, or an error.
type frame struct {
	Data  json.RawMessage `json:"data,omitempty"`
	End   bool            `json:"end,omitempty"`
	Error *Error          `json:"error,omitempty"`
}

// ServeHTTP serves POST <anything>/<service>/<Method>.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", ContentType)
	w.Header().Set("Cache-Control", "no-store")
	fail := func(status int, err error) {
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(frame{Error: encodeError(err, nil)})
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		fail(http.StatusMethodNotAllowed, fmt.Errorf("%w: use POST", ErrBadRequest))
		return
	}
	if s.cfg.token != "" {
		got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || subtle.ConstantTimeCompare([]byte(got), []byte(s.cfg.token)) != 1 {
			fail(http.StatusUnauthorized, ErrUnauthenticated)
			return
		}
	}
	name := methodOf(r.URL.Path)
	if s.methods[name] == nil {
		fail(http.StatusNotFound, fmt.Errorf("%w %q", ErrUnknownMethod, name))
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, s.cfg.maxBytes))
	if err != nil {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			fail(http.StatusRequestEntityTooLarge, fmt.Errorf("%w: request over %d bytes", ErrBadRequest, s.cfg.maxBytes))
		} else {
			fail(http.StatusBadRequest, fmt.Errorf("%w: %w", ErrBadRequest, err))
		}
		return
	}
	ctx := r.Context()
	if v := r.Header.Get(TimeoutHeader); v != "" {
		ms, err := strconv.ParseInt(v, 10, 64)
		if err != nil || ms < 0 {
			fail(http.StatusBadRequest, fmt.Errorf("%w: %s %q", ErrBadRequest, TimeoutHeader, v))
			return
		}
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(min(ms, maxTimeoutMillis))*time.Millisecond)
		defer cancel()
	}

	rc := http.NewResponseController(w)
	enc := json.NewEncoder(w)
	w.WriteHeader(http.StatusOK)
	write := func(f frame) error {
		if err := enc.Encode(f); err != nil {
			return errors.Join(errStop, err)
		}
		if err := rc.Flush(); err != nil && !errors.Is(err, http.ErrNotSupported) {
			return errors.Join(errStop, err)
		}
		return nil
	}
	werr := s.dispatch(ctx, name, body, func(raw json.RawMessage) error { return write(frame{Data: raw}) })
	if werr != nil {
		_ = write(frame{Error: werr})
		return
	}
	_ = write(frame{End: true})
}

// methodOf is the method a request path names: its last two segments.
func methodOf(path string) string {
	path = strings.TrimSuffix(path, "/")
	i := strings.LastIndexByte(path, '/')
	if i < 0 {
		return ""
	}
	j := strings.LastIndexByte(path[:i], '/')
	return path[j+1:]
}
