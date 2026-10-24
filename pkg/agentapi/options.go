package agentapi

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
	"github.com/bsamanvith/agentkit/pkg/auth"
	"github.com/bsamanvith/agentkit/pkg/auth/login"
	"github.com/bsamanvith/agentkit/pkg/bus"
	"github.com/bsamanvith/agentkit/pkg/clock"
	"github.com/bsamanvith/agentkit/pkg/runner"
	"github.com/bsamanvith/agentkit/pkg/runner/pool"
	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
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
	// DefaultMaxBackgroundRuns is how many background runs (tasks) a tenant
	// may have going on at once.
	DefaultMaxBackgroundRuns = 10
	// DefaultTaskTimeLimit is the wall-clock limit of a background run.
	DefaultTaskTimeLimit = 2 * time.Hour
	// MinTokenLength is the shortest bearer token WithToken accepts.
	MinTokenLength = 16
)

// writeTimeout bounds each write to a client: one that stops reading cannot
// hold a run (and its slot) for longer.
const writeTimeout = time.Minute

// retryAfter is the Retry-After of a 503, in seconds.
const retryAfter = "2"

// retryAfterTask is the Retry-After of a 429 too_many_background_runs, in
// seconds: a background run ends in minutes, not seconds.
const retryAfterTask = "30"

// Option configures [New].
type Option func(*config)

type config struct {
	models         []wire.ModelProvider
	admin          ToolsetAdmin
	mcpRegistry    MCPRegistry
	credentials    Credentials
	registry       AgentRegistry
	token          string
	authn          auth.Authenticator
	login          *login.Login
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
	watchBuffer    int // events a viewer of a shared watch may be behind (T103)
	runnerOpts     []runner.Option
	router         *pool.Router
	runs           RunsFactory
	grace          time.Duration
	graceArmed     func(runID string) // tests: called when a run's grace starts
	maxBackground  int
	taskLimit      time.Duration
	webhooks       *Webhooks
	runContext     func(context.Context, RunContext) context.Context
	triggers       *Triggers
}

// Store is what the server keeps conversations and runs in: the runner's
// store, the conversation records, runs' events and commands (T090), the
// index of a tenant's runs and runs' webhooks (T071), and the messages
// between agents (T073). Every backend of
// package store (memory, sqlite, postgres) is one.
type Store interface {
	runner.Store
	store.ConversationIndex
	store.EventLog
	store.CommandLog
	store.RunIndex
	store.NotificationLog
	store.MessageLog
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
		watchBuffer:   defaultWatchBuffer,
		grace:         DefaultReconnectGrace,
		maxBackground: DefaultMaxBackgroundRuns,
		taskLimit:     DefaultTaskTimeLimit,
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
// The token must be at least [MinTokenLength] bytes. It is one API key of
// tenant [DefaultTenant] with every scope and toolset. Without it or
// [WithAuthenticator] the API is open: only serve it on a loopback address.
func WithToken(token string) Option { return func(c *config) { c.token = token } }

// WithAuthenticator requires every request to authenticate with a, in place
// of [WithToken] (give one or the other). The request's principal (package
// auth) then decides:
//   - its tenant: the conversations and runs it sees (unless [WithTenant]);
//   - its scopes: [ScopeRead] for every GET, [ScopeRun] for every other
//     method, else 403 forbidden;
//   - its toolsets: the toolsets it is shown, and the agents it may use, those
//     whose toolsets it may all use. A toolset or agent it may not use is 404,
//     the same answer as one that does not exist.
func WithAuthenticator(a auth.Authenticator) Option { return func(c *config) { c.authn = a } }

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
// the drain (default [clock.Real]). Give the harness the same clock
// (harness.WithClock) so that the times in events and resources agree.
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
// Pass [runner.WithFlows] to serve flow runs' decisions. They do not apply to
// the [Runs] of [WithRuns].
func WithRunnerOptions(opts ...runner.Option) Option {
	return func(c *config) { c.runnerOpts = append(c.runnerOpts, opts...) }
}

// WithRouter sets the pools the server runs its agents in ([E20]). The router
// places every chat run, started or resumed, by the default runner or the
// [Runs] of [WithRuns], from the run's placement, its agent's name and its
// agent's [Agent.Isolation]; a run no pool may run does not start. Without
// it the server runs every agent as built, in one process pool
// ([pool.Default]): the runner as it ran before pools.
//
// A pool with a limit ([pool.WithLimit]) holds a slot for each of its runs
// going on. A chat (POST /v1/runs, or a resume) takes one when the request
// comes, and is refused with 429 over_capacity when none is free; a
// background run waits in the store until the pool's dispatcher
// ([Server.Dispatcher]) has one. So a chat pool's slots are never taken by
// background work routed to another pool. Each run stores its pool
// ([store.Run.Pool]), and GET /v1/runs/{id} shows it.
//
// The server owns the router: [Server.Shutdown] closes it, and so the agents
// its pools made, once the runs have stopped; [NewAgents] closes it when it
// fails to make the server.
//
// [E20]: https://github.com/bsamanvith/agentkit/blob/main/tasks/E20-runner-pools.md
func WithRouter(rt *pool.Router) Option { return func(c *config) { c.router = rt } }

// WithReconnectGrace sets how long a chat run goes on with nobody reading
// its events (default [DefaultReconnectGrace]): the last client to go away
// starts it, and a client that comes back (GET /v1/runs/{id}/events) before
// it ends follows the run on. When it ends, the run is interrupted, as by
// POST /v1/runs/{id}/stop. 0 interrupts it at once.
func WithReconnectGrace(d time.Duration) Option { return func(c *config) { c.grace = d } }

// WithMaxBackgroundRuns caps the background runs (tasks, POST /v1/tasks) a
// tenant has going on at once, queued or running, on this server's store
// (default [DefaultMaxBackgroundRuns]); past it a new task gets 429
// too_many_background_runs with Retry-After. n must be at least 1. Replicas
// count the same runs, but two that start a task at the same instant may
// both pass the last place.
func WithMaxBackgroundRuns(n int) Option { return func(c *config) { c.maxBackground = n } }

// WithTaskTimeLimit sets the wall-clock limit of background runs (default
// [DefaultTaskTimeLimit]): a task still going on d after it started fails
// with [CodeWallClockLimit], however often it was resumed in between
// ([runner.WithTimeLimit]). 0 means none. The run timeout ([WithRunTimeout])
// is for chat runs only. It does not apply to the [Runs] of [WithRuns],
// whose runner sets its own.
func WithTaskTimeLimit(d time.Duration) Option { return func(c *config) { c.taskLimit = d } }

// Webhooks are what the server needs to take completion webhooks
// (POST /v1/tasks with "notify", [Agent.Notify]). Delivering them is not the
// server's: a [github.com/bsamanvith/agentkit/pkg/webhook.Notifier] over the
// same store does it, with the same secrets, posting
// [Server.WebhookPayload].
type Webhooks struct {
	// Secret returns the text form of the tenant's signing secret
	// ("whsec_..."), which GET /v1/webhooks/secret shows the tenant's
	// admins.
	Secret func(t tenancy.Tenant) (string, error)
	// CheckURL refuses a webhook URL the server must not post to, such as
	// one on a private network (package netguard); nil allows any http or
	// https URL.
	CheckURL func(raw string) error
}

// WithWebhooks lets runs have completion webhooks. Without it, a task that
// asks for one is refused, and GET /v1/webhooks/secret is 404.
func WithWebhooks(w Webhooks) Option { return func(c *config) { c.webhooks = &w } }

var idEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

func randomID(prefix string) string {
	b := make([]byte, 16)
	_, _ = rand.Read(b) // never fails (crypto/rand panics instead)
	return prefix + strings.ToLower(idEncoding.EncodeToString(b))
}
