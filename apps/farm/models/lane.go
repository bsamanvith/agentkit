package models

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/bsamanvith/agentkit/pkg/llm"
)

// How long a model that failed rests before the lane tries it first again:
// a short while after a transient failure, longer after a bad key.
const (
	RestTransient = time.Minute
	RestAuth      = 30 * time.Minute
)

// State is where a model of the lane stands.
type State string

// The states.
const (
	// StateReady: in the lane, and not resting.
	StateReady State = "ready"
	// StateResting: in the lane, but it failed lately; tried after the
	// ready ones until its rest ends.
	StateResting State = "resting"
	// StateNoKey: left out, its provider's key is not set.
	StateNoKey State = "no_key"
	// StateNotEvaluated: left out, it has not passed make farm-eval.
	StateNotEvaluated State = "not_evaluated"
	// StateBroken: left out, it could not be built.
	StateBroken State = "broken"
)

// Status is one model of the list a lane was built from.
type Status struct {
	Model string `json:"model"`
	State State  `json:"state"`
	// Cost is the provider's, from [Provider.Cost].
	Cost string `json:"cost,omitempty"`
	// Reason says why the model is resting or left out.
	Reason string `json:"reason,omitempty"`
}

// Option configures [New].
type Option func(*config)

type config struct {
	unevaluated bool
	getenv      func(string) string
	now         func() time.Time
	log         *slog.Logger
	built       map[Name]llm.Model
}

// AllowUnevaluated lets models in that have not passed make farm-eval: for
// running the eval itself, and for trying a model by hand.
func AllowUnevaluated() Option { return func(c *config) { c.unevaluated = true } }

// WithGetenv reads keys with f instead of os.Getenv.
func WithGetenv(f func(string) string) Option { return func(c *config) { c.getenv = f } }

// WithClock sets the clock the rests are timed by.
func WithClock(now func() time.Time) Option { return func(c *config) { c.now = now } }

// WithLogger logs hand-overs to log.
func WithLogger(log *slog.Logger) Option { return func(c *config) { c.log = log } }

// WithModel uses m for name n instead of building it (it still needs a key
// unless a test's getenv gives one).
func WithModel(n Name, m llm.Model) Option {
	return func(c *config) {
		if c.built == nil {
			c.built = map[Name]llm.Model{}
		}
		c.built[n] = m
	}
}

// Lane runs each request on the first model that answers. It is an
// llm.Model, safe for concurrent use.
type Lane struct {
	members []*member
	listed  []Status // the models left out, by list position
	order   []Name   // the list as given
	now     func() time.Time
	log     *slog.Logger

	mu sync.Mutex // guards the members' rest fields
}

type member struct {
	name   Name
	cost   string
	model  llm.Model
	until  time.Time // resting until then
	reason string
}

// ErrNoModel means the lane has no model to run a request on.
var ErrNoModel = errors.New("no model in the lane")

// New builds a lane from names, leaving out the models without a key, those
// not evaluated (without [AllowUnevaluated]) and those that cannot be built.
// A lane may end up empty: [Lane.Len] says so, and its requests fail.
func New(names []Name, opts ...Option) *Lane {
	c := config{getenv: os.Getenv, now: time.Now, log: slog.New(slog.DiscardHandler)}
	for _, o := range opts {
		o(&c)
	}
	l := &Lane{now: c.now, log: c.log, order: names}
	for _, n := range names {
		p, ok := n.provider()
		if !ok {
			l.listed = append(l.listed, Status{Model: n.String(), State: StateBroken, Reason: ErrUnknownProvider.Error()})
			continue
		}
		left := func(s State, reason string) {
			l.listed = append(l.listed, Status{Model: n.String(), State: s, Cost: p.Cost, Reason: reason})
		}
		if firstSet(c.getenv, p.Keys) == "" {
			left(StateNoKey, "set "+strings.Join(p.Keys, " or "))
			continue
		}
		if !c.unevaluated && !IsEvaluated(n) {
			left(StateNotEvaluated, "run make farm-eval FARM_EVAL_MODEL="+n.String())
			continue
		}
		m, ok := c.built[n]
		if !ok {
			var err error
			if m, err = Build(n, c.getenv); err != nil {
				left(StateBroken, err.Error())
				continue
			}
		}
		l.members = append(l.members, &member{name: n, cost: p.Cost, model: m})
	}
	return l
}

// Len is the number of models in the lane.
func (l *Lane) Len() int { return len(l.members) }

// Active is the model the next request goes to first, or "off".
func (l *Lane) Active() string {
	if ms := l.queue(); len(ms) > 0 {
		return ms[0].name.String()
	}
	return "off"
}

// Statuses lists every model the lane was built from, in the list's order.
func (l *Lane) Statuses() []Status {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	byName := map[string]Status{}
	for _, s := range l.listed {
		byName[s.Model] = s
	}
	for _, m := range l.members {
		s := Status{Model: m.name.String(), State: StateReady, Cost: m.cost}
		if now.Before(m.until) {
			s.State, s.Reason = StateResting, m.reason
		}
		byName[s.Model] = s
	}
	out := make([]Status, 0, len(l.order))
	for _, n := range l.order {
		out = append(out, byName[n.String()])
	}
	return out
}

// queue is the order to try the members in: the ready ones, then the
// resting ones (sooner back first), so a request still has a chance when
// every model failed lately.
func (l *Lane) queue() []*member {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	var ready, resting []*member
	for _, m := range l.members {
		if now.Before(m.until) {
			resting = append(resting, m)
		} else {
			ready = append(ready, m)
		}
	}
	for i := 1; i < len(resting); i++ { // a few members: insertion sort
		for j := i; j > 0 && resting[j].until.Before(resting[j-1].until); j-- {
			resting[j], resting[j-1] = resting[j-1], resting[j]
		}
	}
	return append(ready, resting...)
}

func (l *Lane) rest(m *member, err error) {
	d := RestTransient
	if errors.Is(err, llm.ErrAuth) {
		d = RestAuth
	}
	l.mu.Lock()
	m.until, m.reason = l.now().Add(d), err.Error()
	l.mu.Unlock()
}

func (l *Lane) recovered(m *member) {
	l.mu.Lock()
	m.until, m.reason = time.Time{}, ""
	l.mu.Unlock()
}

// handOn reports whether another model could answer a request that failed
// with err: the provider's trouble, not the request's.
func handOn(err error) bool {
	return llm.IsRetryable(err) || errors.Is(err, llm.ErrAuth)
}

// Generate runs req on the first model of the queue, handing it on to the
// next one when a model fails before its reply starts with an error another
// could avoid.
func (l *Lane) Generate(ctx context.Context, req llm.Request) iter.Seq2[llm.Event, error] {
	return func(yield func(llm.Event, error) bool) {
		queue := l.queue()
		if len(queue) == 0 {
			yield(llm.Event{}, fmt.Errorf("%w: %w", llm.ErrAuth, ErrNoModel))
			return
		}
		for i, m := range queue {
			started := false
			var failed error
			for ev, err := range m.model.Generate(ctx, req) {
				if err != nil {
					if !started && handOn(err) && ctx.Err() == nil && i < len(queue)-1 {
						failed = err
						break
					}
					if !started && handOn(err) {
						l.rest(m, err)
					}
					yield(ev, err)
					return
				}
				started = true
				if !yield(ev, nil) {
					return
				}
			}
			if failed == nil {
				l.recovered(m)
				return
			}
			l.rest(m, failed)
			l.log.Warn("farm: model handed on", "from", m.name.String(), "to", queue[i+1].name.String(), "err", failed)
		}
	}
}
