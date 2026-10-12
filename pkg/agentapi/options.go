package agentapi

import (
	"crypto/rand"
	"encoding/base32"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/bsamanvith/agentkit/pkg/clock"
)

// Defaults of the options.
const (
	DefaultMaxConcurrentRuns = 16
	DefaultRunTimeout        = 10 * time.Minute
	DefaultMaxRuns           = 1000
	DefaultMaxConversations  = 1000
	DefaultRetention         = 24 * time.Hour
	DefaultMaxBodyBytes      = 1 << 20
	DefaultKeepAlive         = 15 * time.Second
	// MinTokenLength is the shortest bearer token WithToken accepts.
	MinTokenLength = 16
)

// writeTimeout bounds each write to a client: one that stops reading cannot
// hold a run (and its slot) for longer.
const writeTimeout = time.Minute

// retryAfter is the Retry-After of a 503, in seconds.
const retryAfter = "2"

// Option configures [New].
type Option func(*config)

type config struct {
	token          string
	maxConcurrent  int
	runTimeout     time.Duration
	maxRuns        int
	maxConvs       int
	retention      time.Duration
	maxBodyBytes   int64
	keepAlive      time.Duration
	clock          clock.Clock
	logger         *slog.Logger
	newID          func(prefix string) string
	drainStop      <-chan struct{}
	drainAfter     time.Duration
	drainRequested bool
	toolsets       []Toolset
	tenant         func(*http.Request) string
}

func newConfig(opts []Option) config {
	c := config{
		maxConcurrent: DefaultMaxConcurrentRuns,
		runTimeout:    DefaultRunTimeout,
		maxRuns:       DefaultMaxRuns,
		maxConvs:      DefaultMaxConversations,
		retention:     DefaultRetention,
		maxBodyBytes:  DefaultMaxBodyBytes,
		keepAlive:     DefaultKeepAlive,
		clock:         clock.Real(),
		logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		newID:         randomID,
	}
	for _, o := range opts {
		o(&c)
	}
	return c
}

// WithToken requires every request to carry "Authorization: Bearer <token>".
// The token must be at least [MinTokenLength] bytes. Without this option the
// API is open: only serve it on a loopback address.
func WithToken(token string) Option { return func(c *config) { c.token = token } }

// WithMaxConcurrentRuns caps the runs going on at once (default
// [DefaultMaxConcurrentRuns]); past it a new run gets 503 with Retry-After.
// n must be at least 1.
func WithMaxConcurrentRuns(n int) Option { return func(c *config) { c.maxConcurrent = n } }

// WithRunTimeout ends a run that takes longer than d with [CodeTimeout]
// (default [DefaultRunTimeout]); 0 means no limit.
func WithRunTimeout(d time.Duration) Option { return func(c *config) { c.runTimeout = d } }

// WithRetention bounds what is kept in memory: at most maxRuns finished runs
// and maxConversations conversations, none idle for longer than maxAge. The
// oldest go first; a conversation with a run going on is never evicted.
// Defaults: [DefaultMaxRuns], [DefaultMaxConversations], [DefaultRetention].
func WithRetention(maxRuns, maxConversations int, maxAge time.Duration) Option {
	return func(c *config) { c.maxRuns, c.maxConvs, c.retention = maxRuns, maxConversations, maxAge }
}

// WithMaxBodyBytes limits the body of POST /v1/runs (default
// [DefaultMaxBodyBytes]); larger bodies get 413.
func WithMaxBodyBytes(n int64) Option { return func(c *config) { c.maxBodyBytes = n } }

// WithKeepAlive sets how often an event stream that has nothing to send
// writes an SSE comment, so proxies do not close it (default
// [DefaultKeepAlive]); 0 turns it off.
func WithKeepAlive(d time.Duration) Option { return func(c *config) { c.keepAlive = d } }

// WithClock sets the clock for retention, the run timeout, the keep-alive and
// the drain (default [clock.Real]). Give the harness the same clock's Now
// (harness.WithNow) so that the times in events and resources agree.
func WithClock(clk clock.Clock) Option { return func(c *config) { c.clock = clk } }

// WithLogger sets the logger (default: discard). It logs each run's start and
// end, and the drain.
func WithLogger(l *slog.Logger) Option {
	return func(c *config) {
		if l != nil {
			c.logger = l
		}
	}
}

// WithNewID replaces how run and conversation IDs are made (default: the
// prefix, "run_" or "conv_", and 26 random base32 characters). For tests
// that need stable IDs; IDs must be unique.
func WithNewID(f func(prefix string) string) Option { return func(c *config) { c.newID = f } }

// WithDrain ties runs to the server's shutdown. When stop is closed (the
// process got SIGTERM), the server takes no new runs, and runs still going
// after the given time are ended with [CodeShutdown]: their clients get a
// run_error event instead of a cut connection. Pass a little less than the
// runtime's shutdown timeout. Without this option, runs are ended only by
// [Server.Shutdown].
func WithDrain(stop <-chan struct{}, after time.Duration) Option {
	return func(c *config) { c.drainStop, c.drainAfter, c.drainRequested = stop, after, true }
}

var idEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

func randomID(prefix string) string {
	b := make([]byte, 16)
	_, _ = rand.Read(b) // never fails (crypto/rand panics instead)
	return prefix + strings.ToLower(idEncoding.EncodeToString(b))
}
