package component

import (
	"io"
	"log/slog"
	"net/http"
	"time"
)

// Option configures New.
type Option func(*config)

type config struct {
	logger          *slog.Logger
	build           BuildInfo
	shutdownTimeout time.Duration
	middleware      []func(http.Handler) http.Handler
}

// DefaultShutdownTimeout is how long a shutdown may take unless
// WithShutdownTimeout says otherwise.
const DefaultShutdownTimeout = 15 * time.Second

func newConfig(opts []Option) config {
	c := config{
		logger:          slog.New(slog.NewTextHandler(io.Discard, nil)),
		shutdownTimeout: DefaultShutdownTimeout,
	}
	for _, o := range opts {
		o(&c)
	}
	return c
}

// WithLogger sets the logger for the server's lifecycle (listening, shutting
// down, failures) and for net/http's own errors (default: discard). Pass a
// JSON handler in production.
func WithLogger(l *slog.Logger) Option {
	return func(c *config) {
		if l != nil {
			c.logger = l
		}
	}
}

// WithBuildInfo sets what GET /version reports.
func WithBuildInfo(b BuildInfo) Option { return func(c *config) { c.build = b } }

// WithShutdownTimeout bounds the whole shutdown: draining in-flight requests
// and every component's Shutdown (default DefaultShutdownTimeout). It must be
// positive.
func WithShutdownTimeout(d time.Duration) Option {
	return func(c *config) { c.shutdownTimeout = d }
}

// WithMiddleware wraps the whole server's handler, the runtime's own
// endpoints included, in mw: the first given is the outermost. It is for
// checks every request passes, such as a proxy's assertion; mw decides which
// paths (the health probes) it lets through.
func WithMiddleware(mw ...func(http.Handler) http.Handler) Option {
	return func(c *config) { c.middleware = append(c.middleware, mw...) }
}
