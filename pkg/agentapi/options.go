package agentapi

import (
	"crypto/rand"
	"encoding/base32"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/bsamanvith/agentkit/pkg/bus"
	"github.com/bsamanvith/agentkit/pkg/clock"
	"github.com/bsamanvith/agentkit/pkg/runner"
	"github.com/bsamanvith/agentkit/pkg/store"
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
	DefaultReconnectGrace    = 10 * time.Second
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
	store          Store
	bus            bus.Bus
	streamPoll     time.Duration
	runnerOpts     []runner.Option
	grace          time.Duration
	graceArmed     func(runID string) // tests: called when a run's grace starts
}

// Store is what the server keeps conversations and runs in: the runner's
// store, the conversation records, and runs' events and commands (T090).
// Every backend of package store (memory, sqlite, postgres) is one.
type Store interface {
	runner.Store
	store.ConversationIndex
	store.EventLog
	store.CommandLog
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
		grace:         DefaultReconnectGrace,
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

// WithRetention bounds the finished runs whose events the server keeps in
// memory: at most maxRuns, none finished longer than maxAge ago, the oldest
// going first. Runs and conversations stay in the store ([WithStore]); a run
// whose events were let go is replayed from it, with a failure's code
// reduced to [CodeRunFailed]. maxConversations is no longer used: the store
// keeps conversations. Defaults: [DefaultMaxRuns], [DefaultMaxConversations],
// [DefaultRetention].
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

// WithStore sets where conversations and runs are kept; it is required. A
// program picks the backend (cmd/agentkit by AGENTKIT_STORE); this package
// never imports one, so as never to reach the store's cross-tenant Admin.
// [github.com/bsamanvith/agentkit/pkg/store/scoped.Memory] is a store kept
// in memory. The server does not close the store: close it after
// [Server.Shutdown].
func WithStore(s Store) Option { return func(c *config) { c.store = s } }

// WithBus sets the bus runs' events and commands go between replicas on
// (E18). Every server sharing a store must share the bus too; the server does
// not close it. Without this option, the server makes an in-memory bus of its
// own, which is right for one replica.
func WithBus(b bus.Bus) Option { return func(c *config) { c.bus = b } }

// WithStreamPoll sets how often a stream of a run another replica runs reads
// the store while the bus is quiet, for events whose publish was lost
// (default [stream.DefaultPoll]), and how often such a stream checks that
// the run is still held.
func WithStreamPoll(d time.Duration) Option { return func(c *config) { c.streamPoll = d } }

// WithRunnerOptions adds options to the runner the server builds over its
// store, after its own: the clock ([WithClock]) and run IDs ([WithNewID]).
// Pass [runner.WithFlows] to serve flow runs' decisions.
func WithRunnerOptions(opts ...runner.Option) Option {
	return func(c *config) { c.runnerOpts = append(c.runnerOpts, opts...) }
}

// WithReconnectGrace sets how long a chat run goes on with nobody reading
// its events (default [DefaultReconnectGrace]): the last client to go away
// starts it, and a client that comes back (GET /v1/runs/{id}/events) before
// it ends follows the run on. When it ends, the run is interrupted, as by
// POST /v1/runs/{id}/stop. 0 interrupts it at once.
func WithReconnectGrace(d time.Duration) Option { return func(c *config) { c.grace = d } }

var idEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

func randomID(prefix string) string {
	b := make([]byte, 16)
	_, _ = rand.Read(b) // never fails (crypto/rand panics instead)
	return prefix + strings.ToLower(idEncoding.EncodeToString(b))
}
