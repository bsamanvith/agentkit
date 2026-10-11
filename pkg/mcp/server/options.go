package server

import (
	"io"
	"log/slog"
	"net/http"
)

// Option configures [New] and [ServeStdio].
type Option func(*config)

type config struct {
	basePath     string
	stateful     bool
	maxBodyBytes int64 // 0: the SDK default
	middleware   []func(http.Handler) http.Handler
	logger       *slog.Logger
}

func newConfig(opts []Option) *config {
	c := &config{basePath: "/mcp", logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	for _, o := range opts {
		o(c)
	}
	return c
}

// WithBasePath sets the path prefix under which toolsets are served
// (default "/mcp"): a toolset "gmail" is at {base}/gmail. It must start with
// "/"; a trailing "/" is ignored, and "/" serves toolsets at the root.
// Mount the Server at that prefix without stripping it.
func WithBasePath(p string) Option { return func(c *config) { c.basePath = p } }

// WithStateful makes each endpoint keep MCP sessions (Mcp-Session-Id) instead
// of the default stateless mode. Replicas then need session affinity.
func WithStateful() Option { return func(c *config) { c.stateful = true } }

// WithMaxBodyBytes limits the size of a request body (default: the SDK's
// limit, 4 MiB). A negative value disables the limit.
func WithMaxBodyBytes(n int64) Option { return func(c *config) { c.maxBodyBytes = n } }

// WithMiddleware wraps the whole handler: every toolset endpoint, the listing
// and unknown paths, so a middleware that rejects unauthenticated requests
// reveals nothing about which toolsets exist. This is where authentication,
// tenant resolution (into the request context, which reaches the tool),
// rate limits and audit plug in. Given several times, the first is outermost.
func WithMiddleware(mw func(http.Handler) http.Handler) Option {
	return func(c *config) { c.middleware = append(c.middleware, mw) }
}

// WithLogger sets the logger for unexpected errors, such as a registry
// failing in a way that is not a tool error (default: discard).
func WithLogger(l *slog.Logger) Option { return func(c *config) { c.logger = l } }
