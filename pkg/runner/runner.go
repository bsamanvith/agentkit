package runner

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"iter"
	mrand "math/rand/v2"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bsamanvith/agentkit/pkg/clock"
	"github.com/bsamanvith/agentkit/pkg/flow"
	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

// Errors returned by the runner, checked with errors.Is. Errors of the store
// ([store.ErrLeased], [store.ErrFinal], [store.ErrConflict],
// [store.ErrLeaseLost], [store.ErrNotFound]), of flow ([flow.ErrShapeChanged])
// and of the harness and model are passed through wrapped.
var (
	// ErrInvalidConfig means [New] or [NewRecovery] was given a missing
	// argument or an unusable option.
	ErrInvalidConfig = errors.New("invalid runner configuration")
	// ErrInvalidInput means a message cannot start a run: it is not a single
	// well-formed user message. It also matches [harness.ErrInvalidInput].
	ErrInvalidInput = fmt.Errorf("invalid run input: %w", harness.ErrInvalidInput)
	// ErrRunUnfinished means the conversation already has a run that is not
	// final. [UnfinishedError] carries its ID: resume it, decide it, or
	// cancel it first.
	ErrRunUnfinished = errors.New("conversation has an unfinished run")
	// ErrAwaitingDecision means [Runner.Resume] was asked to resume a
	// suspended run, which only [Runner.Decide] continues.
	ErrAwaitingDecision = errors.New("run waits for a decision")
	// ErrNotPending means [Runner.Decide] named a path the run is not
	// waiting for, or a run that is not suspended.
	ErrNotPending = errors.New("no approval pending at that path")
	// ErrNotStarted means a run was interrupted before it stored its
	// message, so there is nothing to resume. The runner cancels it.
	ErrNotStarted = errors.New("run stopped before it stored its message")
	// ErrNoFlows means a flow run was started or found, but the runner has
	// no [FlowFactory] ([WithFlows]).
	ErrNoFlows = errors.New("runner has no flows")
	// ErrCanceled ends the events of an execution stopped by
	// [Runner.Cancel].
	ErrCanceled = errors.New("run canceled")
	// ErrIterated means [Execution.Events] was iterated a second time.
	ErrIterated = errors.New("execution already iterated")
)

// UnfinishedError reports the unfinished run that keeps a conversation from
// starting another. It matches [ErrRunUnfinished].
type UnfinishedError struct {
	ConversationID string
	RunID          string
}

func (e *UnfinishedError) Error() string {
	return fmt.Sprintf("conversation %q has an unfinished run %q", e.ConversationID, e.RunID)
}

// Unwrap returns [ErrRunUnfinished].
func (e *UnfinishedError) Unwrap() error { return ErrRunUnfinished }

// RunError is how an execution ends when its run failed: the run as stored
// after the failure, and the cause. Retryable and NextAttemptAt say whether,
// and from when, the recovery loop tries again. It unwraps to the cause, so
// errors.Is matches the model's, the harness's or flow's sentinels.
type RunError struct {
	Run store.Run
	Err error
}

func (e *RunError) Error() string {
	retry := "not retried"
	if e.Run.Retryable {
		retry = "retried at " + e.Run.NextAttemptAt.Format(time.RFC3339Nano)
	}
	return fmt.Sprintf("run %q failed (attempt %d, %s): %v", e.Run.ID, e.Run.Attempts, retry, e.Err)
}

func (e *RunError) Unwrap() error { return e.Err }

// Agent runs one conversation turn loop. [*harness.Harness] is one; the
// interface lets a test, or a wrapper, stand in for it.
type Agent interface {
	Run(ctx context.Context, in harness.Input) iter.Seq2[harness.Event, error]
}

// AgentFactory returns the agent that runs a conversation. It is called once
// per execution of a chat run, before the run is created or claimed, so a
// failure leaves nothing behind.
type AgentFactory func(ctx context.Context, conversationID string) (Agent, error)

// FlowFactory returns the step tree of a conversation's flow runs. It is
// called once per execution of a flow run; on resume it must return the tree
// that started the run, or the run fails with [flow.ErrShapeChanged].
type FlowFactory func(ctx context.Context, conversationID string) (flow.Step, error)

// Store is what the runner keeps runs in: the conversation log, the run
// records and leases, and flow checkpoints. Every backend of package store
// implements all three on one value.
type Store interface {
	store.ConversationStore
	store.RunStore
	flow.Checkpointer
}

// EventKind identifies an [Event].
type EventKind uint8

// The event kinds. Consumers must ignore kinds they do not know: later
// versions add more.
const (
	// EventHarness carries an event of a chat run's harness in
	// Event.Harness. For [harness.EventRunStart] with a prompt and for
	// [harness.EventTurnDone], Event.Seq is the position the entry was
	// stored at, and the entry is stored before the event is delivered.
	EventHarness EventKind = iota + 1
	// EventFlow carries an event of a flow run in Event.Flow, including
	// [flow.EventWarning].
	EventFlow
	// EventDone ends an execution whose run completed, suspended or was
	// rejected: Event.Run is the run as stored. Event.Result is set for a
	// chat run that completed by running the agent, Event.State for a
	// completed flow run.
	EventDone
)

func (k EventKind) String() string {
	switch k {
	case EventHarness:
		return "harness"
	case EventFlow:
		return "flow"
	case EventDone:
		return "done"
	}
	return "EventKind(" + strconv.Itoa(int(k)) + ")"
}

// Event is one step of an execution.
type Event struct {
	Kind  EventKind
	RunID string

	Harness harness.Event
	Seq     int64
	Flow    flow.Event

	Run    store.Run
	Result *harness.Result
	State  *flow.State
}

// Execution is a run that has been created or claimed and is ready to run.
// Iterate Events to run it; until then it holds the run's lease without
// renewing it, so an execution that is never iterated is resumed by the
// recovery loop once its lease expires.
type Execution struct {
	// Run is the run as it stood when the execution was made.
	Run    store.Run
	events iter.Seq2[Event, error]
	used   atomic.Bool
}

// Events runs the execution and streams its events. It ends with an
// [EventDone] when the run completed, suspended or was rejected, and with an
// error otherwise: a [*RunError] when the run failed (and was stored so), the
// context's error when ctx was cancelled or the consumer stopped (the run stays
// running, with its lease released, for the recovery loop), [ErrCanceled] when
// [Runner.Cancel] stopped it, or a store error when the run could not be
// written (another writer, a lost lease). It can be iterated once.
func (x *Execution) Events() iter.Seq2[Event, error] {
	return func(yield func(Event, error) bool) {
		if x.used.Swap(true) {
			yield(Event{}, ErrIterated)
			return
		}
		x.events(yield)
	}
}

// Wait iterates the execution's events to the end and returns the last one.
func (x *Execution) Wait() (Event, error) {
	var last Event
	for ev, err := range x.Events() {
		if err != nil {
			return last, err
		}
		last = ev
	}
	return last, nil
}

// Runner runs agents with persistence. It is safe for concurrent use.
type Runner struct {
	store  Store
	agents AgentFactory
	flows  FlowFactory
	clk    clock.Clock
	owner  string
	ttl    time.Duration
	retry  RetryPolicy
	newID  func() string
	jitter func(n int64) int64
	cache  *cache

	execs atomic.Uint64
	mu    sync.Mutex
	live  map[liveKey]*liveRun
}

type liveKey struct{ tenant, run string }

type liveRun struct {
	cancel context.CancelCauseFunc
	done   chan struct{}
}

// Option configures [New].
type Option func(*config) error

type config struct {
	flows     FlowFactory
	clk       clock.Clock
	owner     string
	ttl       time.Duration
	retry     RetryPolicy
	newID     func() string
	rnd       *mrand.Rand
	cacheSize int
}

// WithFlows sets the factory of flow runs. Without it, [Runner.RunFlow] and
// resuming a flow run fail with [ErrNoFlows].
func WithFlows(f FlowFactory) Option {
	return func(c *config) error {
		if f == nil {
			return fmt.Errorf("%w: nil flow factory", ErrInvalidConfig)
		}
		c.flows = f
		return nil
	}
}

// WithClock sets the clock for lease renewal and retry times (default
// [clock.Real]). It should be the store's clock.
func WithClock(clk clock.Clock) Option {
	return func(c *config) error {
		if clk == nil {
			return fmt.Errorf("%w: nil clock", ErrInvalidConfig)
		}
		c.clk = clk
		return nil
	}
}

// WithOwner sets the label of this runner's leases (default a random one).
// Each execution holds the lease as the owner plus a counter, so two
// executions of one runner never share a lease.
func WithOwner(owner string) Option {
	return func(c *config) error {
		if owner == "" {
			return fmt.Errorf("%w: empty owner", ErrInvalidConfig)
		}
		c.owner = owner
		return nil
	}
}

// WithLease sets the lease time to live (default 30s). An execution renews it
// every third of that.
func WithLease(ttl time.Duration) Option {
	return func(c *config) error {
		if ttl < 3*time.Millisecond {
			return fmt.Errorf("%w: lease ttl %v below 3ms", ErrInvalidConfig, ttl)
		}
		c.ttl = ttl
		return nil
	}
}

// WithRetry sets the retry policy of failures that [llm.IsRetryable] accepts.
func WithRetry(p RetryPolicy) Option {
	return func(c *config) error {
		if err := p.check(); err != nil {
			return err
		}
		c.retry = p
		return nil
	}
}

// WithIDs sets how run IDs are made (default 16 random bytes in hex). Every
// ID must be new.
func WithIDs(newID func() string) Option {
	return func(c *config) error {
		if newID == nil {
			return fmt.Errorf("%w: nil id function", ErrInvalidConfig)
		}
		c.newID = newID
		return nil
	}
}

// WithRand sets the source of the retry jitter (default a random one), so a
// test sees the same delays every time.
func WithRand(src mrand.Source) Option {
	return func(c *config) error {
		if src == nil {
			return fmt.Errorf("%w: nil random source", ErrInvalidConfig)
		}
		c.rnd = mrand.New(src)
		return nil
	}
}

// WithCacheSize sets how many conversations' contexts the in-process cache
// keeps (default 1024).
func WithCacheSize(n int) Option {
	return func(c *config) error {
		if n < 1 {
			return fmt.Errorf("%w: cache size %d", ErrInvalidConfig, n)
		}
		c.cacheSize = n
		return nil
	}
}

// New returns a runner over s that runs conversations with the agents from
// agents.
func New(s Store, agents AgentFactory, opts ...Option) (*Runner, error) {
	if s == nil || agents == nil {
		return nil, fmt.Errorf("%w: nil store or agent factory", ErrInvalidConfig)
	}
	c := config{clk: clock.Real(), ttl: 30 * time.Second, retry: DefaultRetryPolicy(), cacheSize: 1024}
	for _, opt := range opts {
		if err := opt(&c); err != nil {
			return nil, err
		}
	}
	if c.owner == "" {
		c.owner = "runner-" + randomHex(8)
	}
	if c.newID == nil {
		c.newID = func() string { return randomHex(16) }
	}
	jitter := mrand.Int64N
	if c.rnd != nil {
		var mu sync.Mutex
		rnd := c.rnd
		jitter = func(n int64) int64 {
			mu.Lock()
			defer mu.Unlock()
			return rnd.Int64N(n)
		}
	}
	return &Runner{
		store:  s,
		agents: agents,
		flows:  c.flows,
		clk:    c.clk,
		owner:  c.owner,
		ttl:    c.ttl,
		retry:  c.retry,
		newID:  c.newID,
		jitter: jitter,
		cache:  newCache(c.cacheSize),
		live:   map[liveKey]*liveRun{},
	}, nil
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b) // never fails (crypto/rand)
	return hex.EncodeToString(b)
}

// nextOwner is the lease owner of one execution.
func (r *Runner) nextOwner() string {
	return r.owner + "/" + strconv.FormatUint(r.execs.Add(1), 10)
}

// Context returns the conversation's context as the next request would send
// it, and the position of the log it was read at, from the cache when it is
// warm. The transcript is the caller's.
func (r *Runner) Context(ctx context.Context, conversationID string) (harness.Transcript, int64, error) {
	v, err := r.cache.load(ctx, r.store, conversationID)
	if err != nil {
		return nil, 0, err
	}
	return v.tr, v.seq, nil
}

func (r *Runner) register(tenant tenancy.Tenant, runID string, cancel context.CancelCauseFunc) (*liveRun, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	k := liveKey{tenant.ID(), runID}
	if _, ok := r.live[k]; ok {
		return nil, false
	}
	l := &liveRun{cancel: cancel, done: make(chan struct{})}
	r.live[k] = l
	return l, true
}

func (r *Runner) unregister(tenant tenancy.Tenant, runID string, l *liveRun) {
	r.mu.Lock()
	delete(r.live, liveKey{tenant.ID(), runID})
	r.mu.Unlock()
	close(l.done)
}

func (r *Runner) liveRun(tenant tenancy.Tenant, runID string) *liveRun {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.live[liveKey{tenant.ID(), runID}]
}
