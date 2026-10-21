package pool

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/bsamanvith/agentkit/pkg/runner"
)

// Errors of the package, checked with errors.Is.
var (
	// ErrNoPool means no pool may run a request: none of the router's pools
	// that match it is isolated enough.
	ErrNoPool = errors.New("no pool may run this agent")
	// ErrInvalidConfig means [NewRouter] was given unusable pools or rules,
	// or [ParseIsolation] an unknown level.
	ErrInvalidConfig = errors.New("invalid pool configuration")
	// ErrNoBuiltAgent means a [NewProcess] pool was asked to run a request
	// with no [Request.Built] agent.
	ErrNoBuiltAgent = errors.New("request has no built agent")
	// ErrClosed means a pool or a router was asked for an agent after its
	// Close. It matches [runner.ErrShutdown]: the server is going away.
	ErrClosed = fmt.Errorf("pool closed: %w", runner.ErrShutdown)
)

// Isolation is how far a pool keeps an agent from the server and from other
// agents. Levels are ordered: a higher one may run whatever a lower one may.
type Isolation uint8

// The isolation levels, weakest first. The zero value means the agent needs
// no isolation, which [Process] gives.
const (
	// Process runs the agent in a process shared with other agents: the
	// server's, or a runner component's.
	Process Isolation = iota
	// Sandbox runs the agent behind a user-space kernel (gVisor or similar)
	// of its own.
	Sandbox
	// VM runs the agent in a virtual machine of its own.
	VM
)

func (i Isolation) String() string {
	switch i {
	case Process:
		return "process"
	case Sandbox:
		return "sandbox"
	case VM:
		return "vm"
	}
	return "Isolation(" + strconv.Itoa(int(i)) + ")"
}

// ParseIsolation parses "process", "sandbox" or "vm"; "" is Process.
func ParseIsolation(s string) (Isolation, error) {
	switch s {
	case "", "process":
		return Process, nil
	case "sandbox":
		return Sandbox, nil
	case "vm":
		return VM, nil
	}
	return 0, fmt.Errorf("%w: isolation %q (want process, sandbox or vm)", ErrInvalidConfig, s)
}

// Request is what a pool is asked to run: the run's placement and what the
// server knows of its agent.
type Request struct {
	runner.Placement
	// Agent names the agent.
	Agent string
	// Isolation is the least isolation the agent needs.
	Isolation Isolation
	// Built is the agent as the server built it: its harness in the
	// server's process, or its runner component (package runnerrpc). A
	// [NewProcess] pool runs it. Pools that run agents elsewhere build their
	// own from the agent's definition.
	Built runner.Agent
}

// Pool is a source of agents with one isolation level. Its methods are safe
// for concurrent use.
type Pool interface {
	// Name names the pool in rules, logs and runs.
	Name() string
	// Isolation is the isolation the pool's agents have.
	Isolation() Isolation
	// Agent returns the agent that runs the request. Like a
	// [runner.AgentFactory], it is called before the run is created or
	// claimed. After Close it fails with [ErrClosed].
	Agent(ctx context.Context, r Request) (runner.Agent, error)
	// Close closes the agents the pool made that are still open and frees
	// what it holds. Call it once the runs using its agents have stopped;
	// a second call does nothing.
	Close() error
}

type process struct {
	name   string
	closed atomic.Bool
}

// NewProcess returns a pool that runs each request's Built agent, as the
// runner has always run them, with [Process] isolation. It makes no agents,
// so its Close closes none: they are the server's.
func NewProcess(name string) Pool { return &process{name: name} }

func (p *process) Name() string         { return p.name }
func (p *process) Isolation() Isolation { return Process }
func (p *process) Close() error         { p.closed.Store(true); return nil }

func (p *process) Agent(_ context.Context, r Request) (runner.Agent, error) {
	if p.closed.Load() {
		return nil, fmt.Errorf("%w: %q", ErrClosed, p.name)
	}
	if r.Built == nil {
		return nil, fmt.Errorf("%w: agent %q", ErrNoBuiltAgent, r.Agent)
	}
	return r.Built, nil
}

// Owned keeps what a pool made and must close: the agents (or the
// environments behind them) still open. A pool adds each one it makes and
// closes them all in its own Close. The zero value is ready to use, and it is
// safe for concurrent use.
type Owned struct {
	mu     sync.Mutex
	closed bool
	next   uint64
	open   map[uint64]io.Closer
}

// Add keeps c until Close, and returns release, which forgets c without
// closing it (for an agent that closed itself, say at the end of its run;
// calling it again does nothing). After Close, Add closes c at once and
// returns an error matching [ErrClosed], so nothing made late stays open.
func (o *Owned) Add(c io.Closer) (release func(), err error) {
	o.mu.Lock()
	if o.closed {
		o.mu.Unlock()
		return func() {}, errors.Join(ErrClosed, c.Close())
	}
	if o.open == nil {
		o.open = map[uint64]io.Closer{}
	}
	id := o.next
	o.next++
	o.open[id] = c
	o.mu.Unlock()
	return func() {
		o.mu.Lock()
		delete(o.open, id)
		o.mu.Unlock()
	}, nil
}

// Close closes everything added and not released, in the order it was
// added, and joins their errors. Later calls do nothing.
func (o *Owned) Close() error {
	o.mu.Lock()
	if o.closed {
		o.mu.Unlock()
		return nil
	}
	o.closed = true
	open := o.open
	o.open = nil
	o.mu.Unlock()
	var errs []error
	for _, id := range slices.Sorted(maps.Keys(open)) {
		errs = append(errs, open[id].Close())
	}
	return errors.Join(errs...)
}

// Match reports whether a rule applies to a request.
type Match func(Request) bool

// Chats matches interactive runs: a user waits on them.
func Chats() Match { return func(r Request) bool { return r.Interactive } }

// Background matches runs nobody waits on.
func Background() Match { return func(r Request) bool { return !r.Interactive } }

// Agents matches the runs of the named agents.
func Agents(names ...string) Match {
	names = slices.Clone(names)
	return func(r Request) bool { return slices.Contains(names, r.Agent) }
}

// Needs matches the runs of agents that need at least isolation i.
func Needs(i Isolation) Match { return func(r Request) bool { return r.Isolation >= i } }

// All matches the requests every one of ms matches.
func All(ms ...Match) Match {
	ms = slices.Clone(ms)
	return func(r Request) bool {
		for _, m := range ms {
			if !m(r) {
				return false
			}
		}
		return true
	}
}

// Rule sends the requests it matches to a pool.
type Rule struct {
	// Pool names one of the router's pools.
	Pool string
	// When says which requests the rule takes; nil takes every request.
	When Match
}

// Router places requests in pools. It is safe for concurrent use.
type Router struct {
	pools []Pool // in the order given, for Close
	named map[string]Pool
	rules []Rule
	def   Pool

	closeOnce sync.Once
	closed    atomic.Bool
	closeErr  error
}

// NewRouter returns a router over pools, which it owns from then on
// ([Router.Close] closes them): it tries rules in order, then the pool named
// def. Pool names must be unique and non-empty, and every rule and def must
// name one of them.
func NewRouter(pools []Pool, rules []Rule, def string) (*Router, error) {
	rt := &Router{pools: slices.Clone(pools), named: map[string]Pool{}, rules: slices.Clone(rules)}
	for _, p := range pools {
		if p == nil || p.Name() == "" {
			return nil, fmt.Errorf("%w: a pool with no name", ErrInvalidConfig)
		}
		if _, ok := rt.named[p.Name()]; ok {
			return nil, fmt.Errorf("%w: two pools named %q", ErrInvalidConfig, p.Name())
		}
		rt.named[p.Name()] = p
	}
	for i, r := range rules {
		if _, ok := rt.named[r.Pool]; !ok {
			return nil, fmt.Errorf("%w: rule %d names pool %q, which is not one of the pools", ErrInvalidConfig, i, r.Pool)
		}
	}
	rt.def = rt.named[def]
	if rt.def == nil {
		return nil, fmt.Errorf("%w: default pool %q is not one of the pools", ErrInvalidConfig, def)
	}
	return rt, nil
}

// DefaultName names the pool of [Default].
const DefaultName = "default"

// Default returns a router with one [NewProcess] pool, named [DefaultName],
// that takes every request needing no more than process isolation: the
// runner as it ran before pools.
func Default() *Router {
	rt, _ := NewRouter([]Pool{NewProcess(DefaultName)}, nil, DefaultName) // valid by construction
	return rt
}

// Route returns the pool that runs r: of the pools of the rules that match r,
// in order, then the default pool, the first whose isolation is at least
// r.Isolation. It returns an error matching [ErrNoPool] when there is none.
func (rt *Router) Route(r Request) (Pool, error) {
	for _, rule := range rt.rules {
		if rule.When != nil && !rule.When(r) {
			continue
		}
		if p := rt.named[rule.Pool]; p.Isolation() >= r.Isolation {
			return p, nil
		}
	}
	if rt.def.Isolation() >= r.Isolation {
		return rt.def, nil
	}
	return nil, fmt.Errorf("%w: agent %q needs %s isolation", ErrNoPool, r.Agent, r.Isolation)
}

// Agent routes r and returns the agent its pool gives. After Close it fails
// with [ErrClosed].
func (rt *Router) Agent(ctx context.Context, r Request) (runner.Agent, error) {
	if rt.closed.Load() {
		return nil, ErrClosed
	}
	p, err := rt.Route(r)
	if err != nil {
		return nil, err
	}
	a, err := p.Agent(ctx, r)
	if err != nil {
		return nil, fmt.Errorf("pool %q: %w", p.Name(), err)
	}
	return a, nil
}

// Factory returns a [runner.AgentFactory] that routes every run: describe
// says what the server knows of the placement's agent (its name, the
// isolation it needs and the agent it built), and the router does the rest.
func (rt *Router) Factory(describe func(ctx context.Context, p runner.Placement) (Request, error)) runner.AgentFactory {
	return func(ctx context.Context, p runner.Placement) (runner.Agent, error) {
		r, err := describe(ctx, p)
		if err != nil {
			return nil, err
		}
		r.Placement = p
		return rt.Agent(ctx, r)
	}
}

// Close closes every pool, in the order given to [NewRouter], and with them
// the agents they made; it joins their errors. Later calls return the same
// error. Close it once the runs using its agents have stopped.
func (rt *Router) Close() error {
	rt.closeOnce.Do(func() {
		rt.closed.Store(true)
		var errs []error
		for _, p := range rt.pools {
			if err := p.Close(); err != nil {
				errs = append(errs, fmt.Errorf("pool %q: %w", p.Name(), err))
			}
		}
		rt.closeErr = errors.Join(errs...)
	})
	return rt.closeErr
}
