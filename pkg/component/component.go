package component

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// Component is one deployable piece (an MCP server, a tool registry, a
// harness) as the runtime sees it.
type Component interface {
	// Name identifies the component in logs and in /readyz. It must be
	// unique within a Runtime.
	Name() string
	// Handler serves the component's endpoints. It is called once, by New,
	// and mounted at the Mount's pattern without stripping it.
	Handler() http.Handler
	// Ready reports whether the component can serve: nil if it can, an error
	// saying why not otherwise. An error matching ErrDegraded means it serves
	// with reduced function: the runtime still counts it as ready and shows
	// the reason in /readyz. Ready is called for every /readyz request and
	// must honor ctx.
	Ready(ctx context.Context) error
	// Shutdown releases the component's resources. It is called once, after
	// the server has stopped serving requests, and must return by the time
	// ctx is done.
	Shutdown(ctx context.Context) error
}

// Reporter is a Component that shows what it loaded in its /readyz entry,
// under "details" (for example the skills of its agents). Report is called
// for every /readyz request, after Ready; its value is encoded as JSON.
type Reporter interface {
	Report() map[string]any
}

var (
	// ErrDegraded marks a component that serves with reduced function (an
	// optional part failed to start) when wrapped by an error from
	// Component.Ready. It still counts as ready.
	ErrDegraded = errors.New("degraded")
	// ErrRequestsCutOff reports, in the error from Runtime.Serve, requests
	// still in flight when the shutdown deadline passed: their connections
	// were closed.
	ErrRequestsCutOff = errors.New("requests still in flight at the shutdown deadline")
	// ErrInvalidConfig reports an unusable argument to New.
	ErrInvalidConfig = errors.New("invalid component runtime configuration")
)

// Mount places a component's handler on the server.
type Mount struct {
	// Pattern is the net/http ServeMux pattern the handler is mounted at,
	// such as "/mcp/". Empty means the component serves no HTTP (a background
	// worker): it still takes part in readiness and shutdown, and its Handler
	// is not called.
	Pattern   string
	Component Component
}

// BuildInfo identifies the running build; GET /version returns it. The
// binary sets the fields from -ldflags "-X ...".
type BuildInfo struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	Date    string `json:"date"`
}

// Runtime serves components on one HTTP server. Build it with New.
type Runtime struct {
	mounts  []Mount
	cfg     config
	handler http.Handler
	served  atomic.Bool
}

// New mounts each component's handler, next to /healthz, /readyz and
// /version. Components are shut down in the reverse of the order given. It
// fails with ErrInvalidConfig for no components, a nil component, an empty or
// duplicate name, or a pattern the ServeMux rejects (malformed, or clashing
// with another pattern or with the runtime's endpoints).
func New(mounts []Mount, opts ...Option) (*Runtime, error) {
	cfg := newConfig(opts)
	if cfg.shutdownTimeout <= 0 {
		return nil, fmt.Errorf("%w: shutdown timeout must be positive", ErrInvalidConfig)
	}
	if len(mounts) == 0 {
		return nil, fmt.Errorf("%w: no components", ErrInvalidConfig)
	}
	r := &Runtime{mounts: append([]Mount(nil), mounts...), cfg: cfg}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /readyz", r.readyz)
	mux.HandleFunc("GET /version", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, cfg.build)
	})
	names := map[string]bool{}
	for _, m := range r.mounts {
		if m.Component == nil {
			return nil, fmt.Errorf("%w: nil component", ErrInvalidConfig)
		}
		name := m.Component.Name()
		if name == "" || names[name] {
			return nil, fmt.Errorf("%w: component name %q is empty or used twice", ErrInvalidConfig, name)
		}
		names[name] = true
		if m.Pattern == "" {
			continue
		}
		if err := handle(mux, m.Pattern, m.Component.Handler()); err != nil {
			return nil, fmt.Errorf("%w: component %q: %w", ErrInvalidConfig, name, err)
		}
	}
	r.handler = mux
	return r, nil
}

// handle registers h at pattern, turning the ServeMux's panics (malformed or
// conflicting pattern) into an error.
func handle(mux *http.ServeMux, pattern string, h http.Handler) (err error) {
	if h == nil {
		return fmt.Errorf("nil handler for pattern %q", pattern)
	}
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("pattern %q: %v", pattern, p)
		}
	}()
	mux.Handle(pattern, h)
	return nil
}

// Handler returns the server's handler: the components and the runtime's own
// endpoints. Serve uses it; tests can mount it on httptest.
func (r *Runtime) Handler() http.Handler { return r.handler }

// Serve serves on ln until ctx is done or the listener fails, then shuts
// down: it stops accepting connections, waits for in-flight requests, and
// calls each component's Shutdown in reverse order, all within the shutdown
// timeout. Past the timeout, remaining connections are closed and Serve
// returns without waiting further. Serve closes ln. It returns nil after a
// clean shutdown triggered by ctx; otherwise the errors that occurred (the
// listener's, ErrRequestsCutOff, the components'), joined. It may be called
// once.
func (r *Runtime) Serve(ctx context.Context, ln net.Listener) error {
	if !r.served.CompareAndSwap(false, true) {
		return errors.New("component: Serve called twice")
	}
	log := r.cfg.logger
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	protocols.SetUnencryptedHTTP2(true)
	srv := &http.Server{
		Handler:   r.handler,
		Protocols: protocols,
		// No ReadTimeout/WriteTimeout: responses can stream (MCP, ConnectRPC).
		// The header and idle timeouts still stop slow-loris clients.
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    64 << 10,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
		// Requests must outlive the signal that starts the shutdown, so their
		// context is not derived from ctx.
		BaseContext: func(net.Listener) context.Context { return context.WithoutCancel(ctx) },
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	log.Info("listening", "addr", ln.Addr().String(), "components", r.names())

	var errs []error
	select {
	case err := <-errc:
		errs = append(errs, fmt.Errorf("serve: %w", err))
		log.Error("server failed", "err", err)
	case <-ctx.Done():
	}
	log.Info("shutting down", "timeout", r.cfg.shutdownTimeout.String())
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), r.cfg.shutdownTimeout)
	defer cancel()
	if len(errs) == 0 {
		if err := srv.Shutdown(sctx); err != nil {
			errs = append(errs, fmt.Errorf("%w: %w", ErrRequestsCutOff, err))
			log.Error("requests still in flight at the deadline", "err", err)
			_ = srv.Close()
		}
		<-errc // http.ErrServerClosed
	}
	for i := len(r.mounts) - 1; i >= 0; i-- {
		c := r.mounts[i].Component
		if err := shutdown(sctx, c); err != nil {
			errs = append(errs, fmt.Errorf("component %q: shutdown: %w", c.Name(), err))
			log.Error("component shutdown failed", "component", c.Name(), "err", err)
		}
	}
	err := errors.Join(errs...)
	if err == nil {
		log.Info("stopped")
	}
	return err
}

// shutdown calls c.Shutdown and returns by the time ctx is done, even if the
// component does not (its goroutine is then abandoned: the process is about
// to exit).
func shutdown(ctx context.Context, c Component) error {
	done := make(chan error, 1)
	go func() { done <- c.Shutdown(ctx) }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *Runtime) names() []string {
	out := make([]string, len(r.mounts))
	for i, m := range r.mounts {
		out[i] = m.Component.Name()
	}
	return out
}

type componentStatus struct {
	Name   string `json:"name"`
	Status string `json:"status"` // ready, degraded, not ready
	Reason string `json:"reason,omitempty"`
	// Details is what a Reporter reports.
	Details map[string]any `json:"details,omitempty"`
}

// readyz asks every component at once and answers 200 only if all are ready
// (or degraded).
func (r *Runtime) readyz(w http.ResponseWriter, req *http.Request) {
	statuses := make([]componentStatus, len(r.mounts))
	var wg sync.WaitGroup
	for i, m := range r.mounts {
		wg.Go(func() {
			s := componentStatus{Name: m.Component.Name(), Status: "ready"}
			if err := m.Component.Ready(req.Context()); err != nil {
				s.Status, s.Reason = "not ready", err.Error()
				if errors.Is(err, ErrDegraded) {
					s.Status = "degraded"
				}
			}
			if r, ok := m.Component.(Reporter); ok {
				s.Details = r.Report()
			}
			statuses[i] = s
		})
	}
	wg.Wait()
	code, status := http.StatusOK, "ready"
	for _, s := range statuses {
		if s.Status == "not ready" {
			code, status = http.StatusServiceUnavailable, "not ready"
		}
	}
	writeJSON(w, code, map[string]any{"status": status, "components": statuses})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
