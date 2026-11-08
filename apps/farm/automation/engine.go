package automation

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/bsamanvith/agentkit/apps/farm/geo"
	"github.com/bsamanvith/agentkit/apps/farm/weather"
	"github.com/bsamanvith/agentkit/pkg/clock"
	"github.com/bsamanvith/agentkit/pkg/trigger"
)

// Target is a plot an automation can fire for.
type Target struct {
	ID      string    `json:"id"`
	Name    string    `json:"name"`
	Point   geo.Point `json:"point"`
	HasPlan bool      `json:"has_plan"`
}

// Targets lists the plots in a scope.
type Targets interface {
	Targets(ctx context.Context, s Scope) ([]Target, error)
}

// Job is one automation's work for one plot.
type Job struct {
	Automation Automation
	Target     Target
	Due        time.Time
	Reason     string
	// Event is the event's name, for an event trigger.
	Event string
	// Forecast is the plot's forecast when a weather trigger fired it.
	Forecast *weather.Forecast
}

// Outcome is what a job found: notices for the farmer (none when there
// is nothing to do) and a note for the run's record.
type Outcome struct {
	Notices []Notice
	Note    string
}

// Handler does a job: a fixed rule, or the skill runner.
type Handler interface {
	Handle(ctx context.Context, j Job) (Outcome, error)
}

// HandlerFunc is a function as a Handler.
type HandlerFunc func(ctx context.Context, j Job) (Outcome, error)

// Handle implements Handler.
func (f HandlerFunc) Handle(ctx context.Context, j Job) (Outcome, error) { return f(ctx, j) }

// Channel delivers notices beyond the in-app inbox (the store), such as
// SMS or WhatsApp. A failed delivery is logged; the notice stays in the
// inbox.
type Channel interface {
	Name() string
	Deliver(ctx context.Context, n Notice) error
}

// Engine runs automations. It is safe for concurrent use.
type Engine struct {
	store    Store
	targets  Targets
	clock    clock.Clock
	log      *slog.Logger
	forecast weather.Source
	rules    map[string]Handler
	ruleInfo map[string]string
	skills   Handler
	channels []Channel
	workers  int
	tick     time.Duration
	dedupe   time.Duration
	keep     time.Duration
	retries  int
	backoff  time.Duration
	timeout  time.Duration

	jobs   chan Job
	events chan event
	mu     sync.Mutex // serialises a scan of due automations
}

type event struct {
	name, farm string
}

// Option configures [New].
type Option func(*Engine)

// WithClock sets the clock (default the machine's).
func WithClock(c clock.Clock) Option { return func(e *Engine) { e.clock = c } }

// WithLogger sets the logger.
func WithLogger(l *slog.Logger) Option { return func(e *Engine) { e.log = l } }

// WithWeather sets the forecast source weather triggers read.
func WithWeather(s weather.Source) Option { return func(e *Engine) { e.forecast = s } }

// WithRule registers a fixed rule by name, with a line on what it does.
func WithRule(name, about string, h Handler) Option {
	return func(e *Engine) { e.rules[name], e.ruleInfo[name] = h, about }
}

// WithSkills sets the handler of skill actions (an agent).
func WithSkills(h Handler) Option { return func(e *Engine) { e.skills = h } }

// WithChannels adds delivery channels beyond the inbox.
func WithChannels(cs ...Channel) Option {
	return func(e *Engine) { e.channels = append(e.channels, cs...) }
}

// WithWorkers sets how many jobs run at once (default 4).
func WithWorkers(n int) Option { return func(e *Engine) { e.workers = n } }

// WithTick sets how often schedules are looked at (default 30s).
func WithTick(d time.Duration) Option { return func(e *Engine) { e.tick = d } }

// WithDedupe sets how long a notice's key keeps the same notice from
// being sent again (default six days: a weekly check repeats what still
// matters, a daily one does not).
func WithDedupe(d time.Duration) Option { return func(e *Engine) { e.dedupe = d } }

// WithRetries sets how many times a failed job is tried again (default 2)
// and the wait before the first retry, doubled each time (default 5s).
func WithRetries(n int, backoff time.Duration) Option {
	return func(e *Engine) { e.retries, e.backoff = n, backoff }
}

// WithRunTimeout bounds one attempt of a job (default 3 minutes: an agent
// skill takes a while).
func WithRunTimeout(d time.Duration) Option { return func(e *Engine) { e.timeout = d } }

// New returns an engine over a store and the plots.
func New(store Store, targets Targets, opts ...Option) (*Engine, error) {
	if store == nil || targets == nil {
		return nil, fmt.Errorf("%w: New needs a store and targets", ErrInvalid)
	}
	e := &Engine{
		store: store, targets: targets, clock: clock.Real(), log: slog.New(slog.DiscardHandler),
		rules: map[string]Handler{}, ruleInfo: map[string]string{}, workers: 4, tick: 30 * time.Second,
		dedupe: 6 * 24 * time.Hour, keep: 60 * 24 * time.Hour, retries: 2, backoff: 5 * time.Second, timeout: 3 * time.Minute,
	}
	for _, o := range opts {
		o(e)
	}
	if e.workers < 1 {
		e.workers = 1
	}
	e.jobs = make(chan Job, e.workers*16)
	e.events = make(chan event, 256)
	return e, nil
}

// Rules returns the registered rules' names and what each does.
func (e *Engine) Rules() map[string]string {
	out := make(map[string]string, len(e.ruleInfo))
	for k, v := range e.ruleInfo {
		out[k] = v
	}
	return out
}

// Check checks an automation's definition, and that its rule is
// registered or there is a skill runner.
func (e *Engine) Check(a Automation) error {
	if _, err := a.check(); err != nil {
		return err
	}
	if a.Trigger.Kind == OnWeather && e.forecast == nil {
		return fmt.Errorf("%w: weather triggers need a forecast source, and the app has none", ErrInvalid)
	}
	switch a.Action.Kind {
	case DoRule:
		if _, ok := e.rules[a.Action.Rule]; !ok {
			names := make([]string, 0, len(e.rules))
			for n := range e.rules {
				names = append(names, n)
			}
			slices.Sort(names)
			return fmt.Errorf("%w: no rule %q: want one of %s", ErrInvalid, a.Action.Rule, strings.Join(names, ", "))
		}
	case DoSkill:
		if e.skills == nil {
			return fmt.Errorf("%w: skill actions need the assistant's model, and the app has none", ErrInvalid)
		}
	}
	return nil
}

func newID(prefix string) string {
	b := make([]byte, 10)
	_, _ = rand.Read(b)
	return prefix + hex.EncodeToString(b)
}

// Create saves a new automation.
func (e *Engine) Create(ctx context.Context, a Automation) (Automation, error) {
	if err := e.Check(a); err != nil {
		return Automation{}, err
	}
	now := e.clock.Now().UTC()
	a.ID, a.Builtin, a.LastDue, a.CreatedAt, a.UpdatedAt = newID("auto_"), false, time.Time{}, now, now
	return a, e.store.CreateAutomation(ctx, a)
}

// Update replaces an automation's definition; a built-in one only takes
// Enabled.
func (e *Engine) Update(ctx context.Context, a Automation) (Automation, error) {
	old, err := e.store.GetAutomation(ctx, a.ID)
	if err != nil {
		return Automation{}, err
	}
	if old.Builtin {
		old.Enabled = a.Enabled
		a = old
	} else {
		if err := e.Check(a); err != nil {
			return Automation{}, err
		}
		a.Builtin, a.CreatedAt = false, old.CreatedAt
		a.LastDue = old.LastDue
		if a.Trigger.Schedule != old.Trigger.Schedule {
			a.LastDue = time.Time{} // a new schedule starts from now
		}
	}
	a.UpdatedAt = e.clock.Now().UTC()
	if !old.Enabled && a.Enabled {
		a.LastDue = time.Time{} // turned on: from now, not every time missed while off
		a.CreatedAt = old.CreatedAt
	}
	return a, e.store.UpdateAutomation(ctx, a)
}

// SetEnabled turns an automation on or off.
func (e *Engine) SetEnabled(ctx context.Context, id string, on bool) (Automation, error) {
	a, err := e.store.GetAutomation(ctx, id)
	if err != nil {
		return Automation{}, err
	}
	a.Enabled = on
	return e.Update(ctx, a)
}

// Delete removes an automation that is not built in.
func (e *Engine) Delete(ctx context.Context, id string) error {
	a, err := e.store.GetAutomation(ctx, id)
	if err != nil {
		return err
	}
	if a.Builtin {
		return fmt.Errorf("%w: %s comes with the app: turn it off instead", ErrBuiltin, a.Name)
	}
	return e.store.DeleteAutomation(ctx, id)
}

// Store returns the store, for listing automations, runs and notices.
func (e *Engine) Store() Store { return e.store }

// Seed saves the built-in automations: new ones as given, existing ones
// with their definition replaced and their Enabled and LastDue kept.
func (e *Engine) Seed(ctx context.Context, builtins []Automation) error {
	now := e.clock.Now().UTC()
	for _, b := range builtins {
		if b.ID == "" {
			return fmt.Errorf("%w: a built-in automation needs an ID", ErrInvalid)
		}
		if err := e.Check(b); err != nil {
			return fmt.Errorf("built-in automation %s: %w", b.ID, err)
		}
		b.Builtin = true
		old, err := e.store.GetAutomation(ctx, b.ID)
		switch {
		case errors.Is(err, ErrNotFound):
			b.CreatedAt, b.UpdatedAt = now, now
			if err := e.store.CreateAutomation(ctx, b); err != nil {
				return err
			}
		case err != nil:
			return err
		default:
			b.Enabled, b.LastDue, b.CreatedAt, b.UpdatedAt = old.Enabled, old.LastDue, old.CreatedAt, old.UpdatedAt
			if b.Trigger.Schedule != old.Trigger.Schedule {
				b.LastDue = time.Time{}
			}
			if err := e.store.UpdateAutomation(ctx, b); err != nil {
				return err
			}
		}
	}
	return nil
}

// Emit reports an event about a plot. It does not wait: the event's
// automations run in the background. When the queue is full the event is
// dropped and logged.
func (e *Engine) Emit(name, farm string) {
	select {
	case e.events <- event{name, farm}:
	default:
		e.log.Warn("automation: event queue full, event dropped", "event", name, "farm", farm)
	}
}

// Run runs the engine until ctx is done: the workers, the schedule scan
// every tick, and events.
func (e *Engine) Run(ctx context.Context) error {
	var wg sync.WaitGroup
	for range e.workers {
		wg.Go(func() { e.work(ctx) })
	}
	tick := e.clock.NewTicker(e.tick)
	defer tick.Stop()
	prune := e.clock.NewTicker(24 * time.Hour)
	defer prune.Stop()
	e.Scan(ctx)
	for {
		select {
		case <-ctx.Done():
			wg.Wait()
			return nil
		case <-tick.C():
			e.Scan(ctx)
		case ev := <-e.events:
			e.onEvent(ctx, ev)
		case <-prune.C():
			if err := e.store.Prune(ctx, e.clock.Now().Add(-e.keep)); err != nil {
				e.log.Warn("automation: prune", "err", err)
			}
		}
	}
}

func (e *Engine) work(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case j := <-e.jobs:
			e.do(ctx, j)
		}
	}
}

// Scan fires every enabled scheduled automation whose time has come:
// once for its latest due time, however many were missed while the app
// was down. Run calls it every tick; tests call it directly.
func (e *Engine) Scan(ctx context.Context) {
	e.mu.Lock()
	defer e.mu.Unlock()
	all, err := e.store.ListAutomations(ctx)
	if err != nil {
		e.log.Error("automation: list", "err", err)
		return
	}
	now := e.clock.Now()
	for _, a := range all {
		if !a.Enabled || a.Trigger.Kind == OnEvent {
			continue
		}
		sched, err := a.check()
		if err != nil {
			e.log.Error("automation: bad definition", "automation", a.ID, "err", err)
			continue
		}
		due, ok := latestDue(sched, a, now)
		if !ok {
			continue
		}
		claimed, err := e.store.ClaimFiring(ctx, a.ID, due)
		if err != nil {
			e.log.Error("automation: claim", "automation", a.ID, "err", err)
			continue
		}
		a.LastDue = due
		if err := e.store.SetLastDue(ctx, a.ID, due); err != nil {
			e.log.Error("automation: save last due", "automation", a.ID, "err", err)
		}
		if !claimed {
			continue // another process fired it
		}
		e.fire(ctx, a, due, "schedule "+due.In(sched.Location()).Format(time.RFC3339), "")
	}
}

// latestDue is the latest schedule time at or before now after the last
// one fired (or, the first time, after the automation was made).
func latestDue(s *trigger.Schedule, a Automation, now time.Time) (time.Time, bool) {
	from := a.LastDue
	if from.IsZero() {
		from = a.UpdatedAt
		if a.CreatedAt.After(from) {
			from = a.CreatedAt
		}
	}
	next := s.Next(from)
	if next.IsZero() || next.After(now) {
		return time.Time{}, false
	}
	for range 100_000 {
		n := s.Next(next)
		if n.IsZero() || n.After(now) {
			break
		}
		next = n
	}
	return next, true
}

func (e *Engine) onEvent(ctx context.Context, ev event) {
	all, err := e.store.ListAutomations(ctx)
	if err != nil {
		e.log.Error("automation: list", "err", err)
		return
	}
	for _, a := range all {
		if a.Enabled && a.Trigger.Kind == OnEvent && a.Trigger.Event == ev.name {
			e.fire(ctx, a, e.clock.Now().UTC(), "event "+ev.name, ev.farm)
		}
	}
}

// fire queues a job per plot in a's scope (only farm, when given), and
// returns how many.
func (e *Engine) fire(ctx context.Context, a Automation, due time.Time, reason, farm string) int {
	scope := a.Scope
	if farm != "" {
		if len(scope.Farms) > 0 && !slices.Contains(scope.Farms, farm) {
			return 0
		}
		scope.Farms = []string{farm}
	}
	targets, err := e.targets.Targets(ctx, scope)
	if err != nil {
		e.log.Error("automation: targets", "automation", a.ID, "err", err)
		return 0
	}
	ev := ""
	if a.Trigger.Kind == OnEvent {
		ev = a.Trigger.Event
	}
	e.log.Info("automation: firing", "automation", a.ID, "plots", len(targets), "reason", reason)
	for _, t := range targets {
		select {
		case e.jobs <- Job{Automation: a, Target: t, Due: due, Reason: reason, Event: ev}:
		case <-ctx.Done():
			return 0
		}
	}
	return len(targets)
}

// RunNow runs an automation at once, for one plot or its whole scope, and
// waits for the runs. It runs even when the automation is off, and does
// not claim a schedule time. With the engine not running (no workers), it
// does the jobs itself.
func (e *Engine) RunNow(ctx context.Context, id, farm string) ([]Run, error) {
	a, err := e.store.GetAutomation(ctx, id)
	if err != nil {
		return nil, err
	}
	scope := a.Scope
	if farm != "" {
		scope.Farms = []string{farm}
	}
	targets, err := e.targets.Targets(ctx, scope)
	if err != nil {
		return nil, err
	}
	now := e.clock.Now().UTC()
	runs := make([]Run, len(targets))
	var wg sync.WaitGroup
	sem := make(chan struct{}, e.workers)
	for i, t := range targets {
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			runs[i] = e.do(ctx, Job{Automation: a, Target: t, Due: now, Reason: "run by hand"})
		})
	}
	wg.Wait()
	return runs, ctx.Err()
}

// do runs one job: the weather condition, then the action with retries,
// then the notices; and records the run.
func (e *Engine) do(ctx context.Context, j Job) (r Run) {
	r = Run{ID: newID("run_"), Automation: j.Automation.ID, Farm: j.Target.ID, Due: j.Due.UTC(), Reason: j.Reason, Started: e.clock.Now().UTC()}
	defer func() {
		r.Finished = e.clock.Now().UTC()
		if err := e.store.AddRun(context.WithoutCancel(ctx), r); err != nil {
			e.log.Error("automation: save run", "run", r.ID, "err", err)
		}
	}()
	fail := func(err error) Run {
		r.Status, r.Error = RunFailed, err.Error()
		e.log.Warn("automation: run failed", "automation", j.Automation.ID, "farm", j.Target.ID, "err", err)
		return r
	}
	if c := j.Automation.Trigger.Weather; j.Automation.Trigger.Kind == OnWeather && c != nil && j.Reason != "run by hand" {
		if e.forecast == nil {
			return fail(errors.New("no forecast source"))
		}
		fc, err := e.forecast.Forecast(ctx, j.Target.Point, max(c.days(), 7))
		if err != nil {
			return fail(err)
		}
		met, why := c.Met(fc.Days)
		if !met {
			r.Status, r.Note = RunQuiet, why
			return r
		}
		j.Forecast, j.Reason = &fc, j.Reason+"; "+why
		r.Reason = j.Reason
	}
	var h Handler
	switch j.Automation.Action.Kind {
	case DoRule:
		h = e.rules[j.Automation.Action.Rule]
	case DoSkill:
		h = e.skills
	}
	if h == nil {
		return fail(fmt.Errorf("no handler for %s %q%s", j.Automation.Action.Kind, j.Automation.Action.Rule, j.Automation.Action.Skill))
	}
	var out Outcome
	var err error
	wait := e.backoff
	for attempt := 0; attempt <= e.retries; attempt++ {
		if attempt > 0 {
			select {
			case <-e.clock.After(wait):
			case <-ctx.Done():
				return fail(ctx.Err())
			}
			wait *= 2
		}
		r.Attempts++
		actx, cancel := context.WithTimeout(ctx, e.timeout)
		out, err = h.Handle(actx, j)
		cancel()
		if err == nil || ctx.Err() != nil {
			break
		}
	}
	if err != nil {
		return fail(err)
	}
	r.Note = out.Note
	since := e.clock.Now().Add(-e.dedupe)
	for _, n := range out.Notices {
		n.ID, n.Farm, n.Automation, n.Run = newID("note_"), j.Target.ID, j.Automation.ID, r.ID
		n.CreatedAt = e.clock.Now().UTC()
		if n.Key == "" {
			n.Key = j.Automation.ID + ":" + n.Title
		}
		if n.Severity == "" {
			n.Severity = Advice
		}
		added, err := e.store.AddNotice(ctx, n, since)
		if err != nil {
			return fail(err)
		}
		if !added {
			continue
		}
		r.Notices++
		for _, ch := range e.channels {
			if err := ch.Deliver(ctx, n); err != nil {
				e.log.Warn("automation: deliver", "channel", ch.Name(), "notice", n.ID, "err", err)
			}
		}
	}
	r.Status = RunQuiet
	if r.Notices > 0 {
		r.Status = RunNotified
	} else if len(out.Notices) > 0 {
		r.Note = strings.TrimPrefix(r.Note+"; already told the farmer", "; ")
	}
	return r
}
