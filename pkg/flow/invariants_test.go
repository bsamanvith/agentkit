package flow_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/flow"
)

// The invariants of a run. They hold for every Run and Resume, whatever the
// step tree and whatever fails or crashes, so every run in this package goes
// through call (run, resume, runCtx, resumeCtx in helpers_test.go), which
// checks them when the call returns and, across all calls of one run id, when
// the test ends. A new behavior then cannot break one of them unnoticed in a
// scenario no test happens to look at.
//
// Per call:
//
//	I1  outcome      the state is nil exactly when there is an error
//	I2  refusals     a refused call (no checkpointer, invalid tree, run exists, unknown run,
//	                 other checkpoint version, changed tree without the opt-in) emits no
//	                 event and saves nothing
//	I3  events       well-formed: an optional warning comes first (Resume only, exactly when
//	                 the stored fingerprint differs); every path is well-formed; each starts
//	                 at most once and ends exactly once (finished, failed or suspended); a
//	                 step starts only while its parent is running and ends before it;
//	                 nothing is running when the call returns and no event comes after;
//	                 the observer is never called concurrently;
//	                 Err is nil exactly on finished, and matches ErrSuspended exactly on
//	                 suspended
//	I4  nesting      a combinator finishes only if every child it started finished
//	I5  root         the returned error is the root step's error; no error means the root
//	                 finished (or had completed before); the root suspends exactly when the
//	                 error matches ErrSuspended
//	I6  skipping     a step that had a record when the call began emits nothing
//	I7  checkpoints  every checkpoint saved is well-formed: version, run id, the tree's
//	                 fingerprint, an initial state and records that decode as State, paths
//	                 that are well-formed, only the frontier (no record under another),
//	                 Pending sorted, and it survives a JSON round trip unchanged
//	I8  records      a record appears only for a step that finished in this call, is
//	                 saved before the step is reported finished, and a step that failed or
//	                 suspended never has one
//	I9  status       every save but the last says running; the last says how the call
//	                 ended (none after a cancellation); Pending is the error's paths and the
//	                 innermost suspended steps; a completed run keeps only the root record,
//	                 which is the returned state
//	I10 copies       nothing handed to Save is changed afterwards
//
// Across the calls of one run id (checked when the test ends):
//
//	I11 progress     every checkpoint's records are covered by the next one's, and the
//	                 initial state and rejections never change or disappear
//	I12 no rerun     a step that finished never starts again
//	I13 final        after a completed call every Resume returns the same state and runs
//	                 nothing; after a rejected call every Resume returns ErrRejected
//
// The simulation (sim_test.go) adds what needs the tree or a reference: the
// outcome matches an independent interpreter, a run crashed anywhere and resumed
// ends where an uninterrupted run ends, no step is still running after Run
// returns, and a cancelled run stops.

// observer is a test's own event observer. Pass it to run and resume instead of
// flow.WithObserver, which the checks would replace.
type observer func(flow.Event)

// runKey identifies a run: its checkpointer and id.
type runKey struct {
	cp flow.Checkpointer
	id string
}

// checker holds the calls of one test, by run.
type checker struct {
	mu    sync.Mutex
	runs  map[runKey][]*flowCall
	order []runKey
}

var (
	checkersMu sync.Mutex
	checkers   = map[testing.TB]*checker{}
)

func checkerFor(tb testing.TB) *checker {
	checkersMu.Lock()
	defer checkersMu.Unlock()
	if c, ok := checkers[tb]; ok {
		return c
	}
	c := &checker{runs: map[runKey][]*flowCall{}}
	checkers[tb] = c
	tb.Cleanup(func() {
		c.checkHistory(tb)
		checkersMu.Lock()
		delete(checkers, tb)
		checkersMu.Unlock()
	})
	return c
}

func (c *checker) add(k runKey, fc *flowCall) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.runs[k]; !ok {
		c.order = append(c.order, k)
	}
	c.runs[k] = append(c.runs[k], fc)
}

// flowCall is one Run or Resume as the checks saw it.
type flowCall struct {
	resume bool
	id     string
	root   string // the root step's name
	shape  string // the tree's fingerprint; empty if the tree is invalid
	ctx    context.Context
	user   observer
	before *flow.Checkpoint // Resume: the checkpoint when the call began

	mu       sync.Mutex
	events   []flow.Event
	late     []flow.Event // events after the call returned
	saves    []savedCheckpoint
	problems []string // found by the observer or the tap, reported by check
	inside   atomic.Int32
	serial   int // written by every observer call without a lock: the race detector
	//             reports observer calls that are not serialized
	returned bool

	st     *flow.State
	stJSON []byte
	err    error
	ctxErr error // ctx.Err() when the call returned
}

type savedCheckpoint struct {
	handed flow.Checkpoint // as the runner handed it to Save
	snap   []byte          // its JSON at that moment
	err    error           // what Save returned
}

func (fc *flowCall) problem(format string, args ...any) {
	fc.problems = append(fc.problems, fmt.Sprintf(format, args...))
}

// tap is the checkpointer the runner is given: it records every save and
// passes it on.
type tap struct {
	flow.Checkpointer
	fc *flowCall
}

func (tp *tap) Save(ctx context.Context, id string, c flow.Checkpoint) error {
	snap, merr := json.Marshal(c)
	err := tp.Checkpointer.Save(ctx, id, c)
	tp.fc.mu.Lock()
	defer tp.fc.mu.Unlock()
	if merr != nil {
		tp.fc.problem("a checkpoint does not marshal: %v", merr)
	}
	tp.fc.saves = append(tp.fc.saves, savedCheckpoint{handed: c, snap: snap, err: err})
	return err
}

func (fc *flowCall) observe(e flow.Event) {
	// I3: calls are serialized (WithObserver), so fn needs no lock.
	fc.serial++
	if fc.inside.Add(1) > 1 {
		fc.mu.Lock()
		fc.problem("the observer was called while another call was running")
		fc.mu.Unlock()
	}
	defer fc.inside.Add(-1)
	fc.mu.Lock()
	if fc.returned {
		fc.late = append(fc.late, e)
		fc.mu.Unlock()
		return
	}
	fc.events = append(fc.events, e)
	if e.Kind == flow.EventFinished {
		// I8: by the time a step is reported finished, its record is saved.
		var last *flow.Checkpoint
		for i := len(fc.saves) - 1; i >= 0; i-- {
			if fc.saves[i].err == nil {
				last = &fc.saves[i].handed
				break
			}
		}
		if last == nil || last.Steps[e.Path] == nil {
			fc.problem("%s was reported finished before a checkpoint with its record was saved", e.Path)
		}
	}
	fc.mu.Unlock()
	if fc.user != nil {
		fc.user(e)
	}
}

// call runs or resumes step with the checks. opts are flow.Options, or an
// observer for the test's own use.
func call(ctx context.Context, tb testing.TB, resume bool, cp flow.Checkpointer, id string, step flow.Step, opts ...any) (*flow.State, error) {
	tb.Helper()
	fc := &flowCall{resume: resume, id: id, ctx: ctx}
	if n, ok := step.(flow.Namer); ok {
		fc.root = n.Name()
	}
	if fp, err := flow.Fingerprint(step); err == nil {
		fc.shape = fp
	}
	var fopts []flow.Option
	for _, o := range opts {
		switch o := o.(type) {
		case flow.Option:
			fopts = append(fopts, o)
		case observer:
			fc.user = o
		default:
			tb.Fatalf("call: option %T is neither a flow.Option nor an observer", o)
		}
	}
	if cp != nil {
		if resume {
			if c, err := cp.Load(context.Background(), id); err == nil {
				fc.before = &c
			}
		}
		fopts = append(fopts, flow.WithCheckpointer(&tap{Checkpointer: withShapeCheck(tb, cp, step), fc: fc}))
	}
	fopts = append(fopts, flow.WithObserver(fc.observe)) // last, so nothing replaces it
	if resume {
		fc.st, fc.err = flow.Resume(ctx, id, step, fopts...)
	} else {
		fc.st, fc.err = flow.Run(ctx, id, step, fopts...)
	}
	fc.ctxErr = ctx.Err()
	fc.mu.Lock()
	fc.returned = true
	fc.mu.Unlock()
	if fc.st != nil {
		fc.stJSON, _ = json.Marshal(fc.st)
	}
	if cp != nil {
		checkerFor(tb).add(runKey{cp, id}, fc)
	}
	fc.check(tb)
	return fc.st, fc.err
}

// ---- path helpers, restated from the package documentation ----

// covers reports whether a record at k stands for the step at p: p is k or a
// step inside it.
func covers(k, p string) bool {
	return p == k || strings.HasPrefix(p, k+"/") || strings.HasPrefix(p, k+"[")
}

func coveredBy(steps map[string]json.RawMessage, p string) bool {
	for k := range steps {
		if covers(k, p) {
			return true
		}
	}
	return false
}

func validName(s string) bool { return s != "" && !strings.ContainsAny(s, "/[]") }

// validPath reports whether p is a path under root: root, then segments that
// are names, each but the last optionally followed by "[index or key]".
func validPath(root, p string) bool {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		name := s
		if open := strings.IndexByte(s, '['); open >= 0 {
			if i == len(segs)-1 || !strings.HasSuffix(s, "]") || !validName(s[open+1:len(s)-1]) {
				return false
			}
			name = s[:open]
		}
		if !validName(name) || (i == 0 && name != root) {
			return false
		}
	}
	return true
}

// parentOf returns the path of the combinator that runs the step at p, or ""
// for the root.
func parentOf(p string) string {
	i := strings.LastIndexByte(p, '/')
	if i < 0 {
		return ""
	}
	parent := p[:i]
	if strings.HasSuffix(parent, "]") {
		parent = parent[:strings.LastIndexByte(parent, '[')]
	}
	return parent
}

func isState(raw []byte) bool { return json.Unmarshal(raw, flow.NewState()) == nil }

// refused reports whether the call was turned away before running anything.
func (fc *flowCall) refused() bool {
	err := fc.err
	var sce *flow.ShapeChangedError
	switch {
	case err == nil:
		return false
	case errors.Is(err, flow.ErrNoCheckpointer), errors.Is(err, flow.ErrRunExists):
		return true
	case errors.Is(err, flow.ErrInvalidFlow) && fc.shape == "":
		return true
	case errors.As(err, &sce):
		return true
	case fc.resume && fc.before == nil:
		return true // unknown run, or the checkpoint could not be loaded
	case fc.resume && fc.before.Version != flow.CheckpointVersion:
		return true
	}
	return false
}

func (fc *flowCall) cancelled() bool {
	return fc.err != nil && fc.ctxErr != nil && errors.Is(fc.err, fc.ctxErr)
}

func (fc *flowCall) check(tb testing.TB) {
	tb.Helper()
	fc.mu.Lock()
	defer fc.mu.Unlock()
	what := "Run"
	if fc.resume {
		what = "Resume"
	}
	errorf := func(format string, args ...any) {
		tb.Helper()
		tb.Errorf("invariant, %s %q: %s", what, fc.id, fmt.Sprintf(format, args...))
	}
	for _, p := range fc.problems {
		errorf("%s", p)
	}
	// I1
	if (fc.st == nil) == (fc.err == nil) {
		errorf("returned state %v with error %v: want exactly one of them", fc.st != nil, fc.err)
	}
	// I2
	if fc.refused() {
		if len(fc.events) > 0 || len(fc.saves) > 0 {
			errorf("refused with %v but emitted %d events and saved %d checkpoints", fc.err, len(fc.events), len(fc.saves))
		}
		return
	}
	var before map[string]json.RawMessage
	if fc.before != nil {
		before = fc.before.Steps
	}
	ended := fc.checkEvents(errorf, before)
	fc.checkSaves(errorf, before, ended)
}

// comparableShape reports whether the stored fingerprint can be compared with
// the tree's: it is set and made by the same version of the encoding (the
// prefix up to ":"). One that cannot be compared only warns (T032).
func (fc *flowCall) comparableShape() bool {
	v, _, ok := strings.Cut(fc.shape, ":")
	return fc.before.Shape != "" && ok && strings.HasPrefix(fc.before.Shape, v+":")
}

// checkEvents checks I3 to I6 and returns how each path ended.
func (fc *flowCall) checkEvents(errorf func(string, ...any), before map[string]json.RawMessage) map[string]flow.Event {
	events := fc.events
	// The warning (T032): first, once, on Resume only, exactly when the
	// stored fingerprint is not the tree's.
	wantWarning := fc.resume && fc.before != nil && fc.before.Shape != fc.shape
	if len(events) > 0 && events[0].Kind == flow.EventWarning {
		w := events[0]
		events = events[1:]
		switch {
		case !wantWarning:
			errorf("unexpected warning %v", w.Err)
		case w.Path != fc.root:
			errorf("warning on %q, want the root %q", w.Path, fc.root)
		case !fc.comparableShape() && !errors.Is(w.Err, flow.ErrNoShape):
			errorf("warning %v for a checkpoint without a comparable fingerprint, want ErrNoShape", w.Err)
		case fc.comparableShape() && !errors.Is(w.Err, flow.ErrShapeChanged):
			errorf("warning %v for a changed tree, want ErrShapeChanged", w.Err)
		}
	} else if wantWarning {
		errorf("no warning, but the checkpoint has fingerprint %q and the tree %q", fc.before.Shape, fc.shape)
	}

	open := map[string]bool{}
	ended := map[string]flow.Event{}
	childOf := func(parent string) []string { // paths that started under parent
		var out []string
		for _, e := range fc.events {
			if e.Kind == flow.EventStarted && parentOf(e.Path) == parent {
				out = append(out, e.Path)
			}
		}
		return out
	}
	for _, e := range events {
		p := e.Path
		if !validPath(fc.root, p) {
			errorf("event %s on malformed path %q", e.Kind, p)
		}
		if coveredBy(before, p) {
			errorf("%s %s: the step had completed before this call and must be skipped silently", e.Kind, p)
		}
		switch e.Kind {
		case flow.EventStarted:
			if _, seen := ended[p]; seen || open[p] {
				errorf("%s started twice in one call", p)
			}
			if parent := parentOf(p); parent == "" && p != fc.root {
				errorf("%s started but the root is %q", p, fc.root)
			} else if parent != "" && !open[parent] {
				errorf("%s started while its parent %s was not running", p, parent)
			}
			open[p] = true
		case flow.EventFinished, flow.EventFailed, flow.EventSuspended:
			if !open[p] {
				errorf("%s %s without having started", e.Kind, p)
			}
			for q := range open {
				if q != p && covers(p, q) {
					errorf("%s %s while %s inside it was still running", e.Kind, p, q)
				}
			}
			switch {
			case e.Kind == flow.EventFinished && e.Err != nil:
				errorf("finished %s carries error %v", p, e.Err)
			case e.Kind == flow.EventFailed && (e.Err == nil || errors.Is(e.Err, flow.ErrSuspended)):
				errorf("failed %s carries error %v", p, e.Err)
			case e.Kind == flow.EventSuspended && !errors.Is(e.Err, flow.ErrSuspended):
				errorf("suspended %s carries error %v, want ErrSuspended", p, e.Err)
			}
			// I4: a combinator finishes only if all its children did.
			if e.Kind == flow.EventFinished {
				for _, child := range childOf(p) {
					if ce, ok := ended[child]; ok && ce.Kind != flow.EventFinished {
						errorf("%s finished although %s %s", p, child, ce.Kind)
					}
				}
			}
			delete(open, p)
			ended[p] = e
		default:
			errorf("event of unknown kind %q on %s", e.Kind, p)
		}
	}
	if len(open) > 0 {
		errorf("steps still running when the call returned: %v", slices.Sorted(maps.Keys(open)))
	}

	// I5
	root, rootRan := ended[fc.root]
	switch {
	case fc.err == nil && !rootRan && !coveredBy(before, fc.root):
		errorf("returned no error but the root never finished")
	case !rootRan:
	case root.Kind == flow.EventFinished && fc.err != nil && !errors.Is(fc.err, flow.ErrCheckpoint):
		errorf("the root finished but the call returned %v", fc.err)
	case root.Kind != flow.EventFinished && !errors.Is(fc.err, root.Err):
		errorf("returned %v, not the root's error %v", fc.err, root.Err)
	case (root.Kind == flow.EventSuspended) != errors.Is(fc.err, flow.ErrSuspended):
		errorf("the root ended %s but the call returned %v", root.Kind, fc.err)
	}
	return ended
}

// checkSaves checks I7 to I10.
func (fc *flowCall) checkSaves(errorf func(string, ...any), before map[string]json.RawMessage, ended map[string]flow.Event) {
	var initial []byte
	if fc.before != nil {
		initial = fc.before.Initial
	}
	prev := before
	for i, s := range fc.saves {
		c := s.handed
		// I10
		if now, err := json.Marshal(c); err != nil || !bytes.Equal(now, s.snap) {
			errorf("save %d was changed after it was handed to Save:\nthen %s\nnow  %s", i, s.snap, now)
		}
		// I7
		if c.Version != flow.CheckpointVersion || c.RunID != fc.id {
			errorf("save %d has version %d and run id %q", i, c.Version, c.RunID)
		}
		if fc.shape != "" && c.Shape != fc.shape {
			errorf("save %d has fingerprint %q, the tree's is %q", i, c.Shape, fc.shape)
		}
		if !isState(c.Initial) {
			errorf("save %d: initial state %s is not a state", i, c.Initial)
		}
		if initial == nil {
			initial = c.Initial
		} else if !bytes.Equal(initial, c.Initial) {
			errorf("save %d changed the initial state from %s to %s", i, initial, c.Initial)
		}
		for k, raw := range c.Steps {
			if !validPath(fc.root, k) {
				errorf("save %d has a record at malformed path %q", i, k)
			}
			if !isState(raw) {
				errorf("save %d: record %s = %s is not a state", i, k, raw)
			}
			for k2 := range c.Steps {
				if k2 != k && covers(k, k2) {
					errorf("save %d keeps %s although %s, which contains it, has completed", i, k2, k)
				}
			}
			// I8 (a refused save stored nothing)
			if s.err != nil {
				continue
			}
			if e, ok := ended[k]; ok && e.Kind != flow.EventFinished {
				errorf("save %d has a record for %s, which %s", i, k, e.Kind)
			}
			if _, had := before[k]; !had {
				if e, ok := ended[k]; !ok || e.Kind != flow.EventFinished {
					errorf("save %d has a record for %s, which did not finish in this call", i, k)
				}
			}
		}
		if !slices.IsSorted(c.Pending) || len(slices.Compact(slices.Clone(c.Pending))) != len(c.Pending) {
			errorf("save %d: pending %v is not sorted and unique", i, c.Pending)
		}
		var back flow.Checkpoint
		if err := json.Unmarshal(s.snap, &back); err != nil {
			errorf("save %d does not decode: %v", i, err)
		} else if again, _ := json.Marshal(back); !bytes.Equal(again, s.snap) {
			errorf("save %d does not survive a JSON round trip:\n%s\n%s", i, s.snap, again)
		}
		if s.err == nil {
			// I11 within the call: progress is never lost.
			if lost := uncovered(prev, c.Steps); len(lost) > 0 {
				errorf("save %d lost the records %v", i, lost)
			}
			prev = c.Steps
		}
		// I9: only the last save says how the run ended.
		if s.err == nil && (i < len(fc.saves)-1 || fc.cancelled()) {
			if c.Status != flow.StatusRunning {
				errorf("save %d of %d has status %s, want running", i+1, len(fc.saves), c.Status)
			}
		}
	}
	if fc.cancelled() || len(fc.saves) == 0 {
		return
	}
	last := fc.saves[len(fc.saves)-1]
	if last.err != nil {
		return
	}
	c := last.handed
	var susp *flow.SuspendedError
	want := flow.StatusFailed
	switch {
	case fc.err == nil:
		want = flow.StatusCompleted
	case errors.Is(fc.err, flow.ErrRejected):
		want = flow.StatusRejected
	case errors.As(fc.err, &susp):
		want = flow.StatusSuspended
	}
	if c.Status != want {
		errorf("the final checkpoint has status %s, the call returned %v", c.Status, fc.err)
	}
	if want == flow.StatusSuspended {
		var innermost []string
		for p, e := range ended {
			if e.Kind != flow.EventSuspended {
				continue
			}
			inner := false
			for q, f := range ended {
				inner = inner || (q != p && f.Kind == flow.EventSuspended && covers(p, q))
			}
			if !inner {
				innermost = append(innermost, p)
			}
		}
		slices.Sort(innermost)
		if !slices.Equal(c.Pending, susp.Paths) || !slices.Equal(c.Pending, innermost) {
			errorf("pending %v, error paths %v, innermost suspended steps %v: want all equal", c.Pending, susp.Paths, innermost)
		}
	} else if len(c.Pending) > 0 {
		errorf("status %s with pending approvals %v", c.Status, c.Pending)
	}
	if want == flow.StatusCompleted {
		if keys := slices.Sorted(maps.Keys(c.Steps)); !slices.Equal(keys, []string{fc.root}) {
			errorf("a completed run keeps records %v, want only the root", keys)
		} else if !bytes.Equal(c.Steps[fc.root], fc.stJSON) {
			errorf("the root record %s is not the returned state %s", c.Steps[fc.root], fc.stJSON)
		}
	}
}

// uncovered returns the records of from that no record of to stands for.
func uncovered(from, to map[string]json.RawMessage) []string {
	var lost []string
	for k := range from {
		if !coveredBy(to, k) {
			lost = append(lost, k)
		}
	}
	slices.Sort(lost)
	return lost
}

// checkHistory checks I11 to I13 over the calls of each run, and that no event
// arrived after a call returned.
func (c *checker) checkHistory(tb testing.TB) {
	tb.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, k := range c.order {
		calls := c.runs[k]
		errorf := func(format string, args ...any) {
			tb.Helper()
			tb.Errorf("invariant, run %q: %s", k.id, fmt.Sprintf(format, args...))
		}
		var (
			prev     map[string]json.RawMessage
			initial  []byte
			rejected map[string]string
			finished = map[string]int{} // path -> call that finished it
			final    *flowCall          // the call that completed or was rejected
		)
		for n, fc := range calls {
			fc.mu.Lock()
			late := slices.Clone(fc.late)
			fc.mu.Unlock()
			if len(late) > 0 {
				errorf("call %d: %d events after it returned, first %+v", n, len(late), late[0])
			}
			if fc.refused() {
				continue
			}
			if !fc.resume { // a new run: nothing before it counts
				prev, initial, rejected, finished, final = nil, nil, nil, map[string]int{}, nil
			}
			// I13 (unless the checkpointer refused a save or the call was cancelled)
			if final != nil && fc.resume && fc.shape == final.shape && !fc.cancelled() && !errors.Is(fc.err, flow.ErrCheckpoint) {
				switch {
				case final.err == nil && (fc.err != nil || !bytes.Equal(fc.stJSON, final.stJSON)):
					errorf("call %d: resuming a completed run returned %v, %s; it had returned %s", n, fc.err, fc.stJSON, final.stJSON)
				case final.err == nil && slices.ContainsFunc(fc.events, func(e flow.Event) bool { return e.Kind != flow.EventWarning }):
					errorf("call %d: resuming a completed run emitted %v", n, fc.events)
				case final.err != nil && !errors.Is(fc.err, flow.ErrRejected):
					errorf("call %d: resuming a rejected run returned %v", n, fc.err)
				case final.err != nil && slices.ContainsFunc(fc.events, func(e flow.Event) bool { return e.Kind != flow.EventWarning }):
					errorf("call %d: resuming a rejected run emitted %v", n, fc.events)
				}
			}
			checkpoints := []map[string]json.RawMessage{}
			var states [][]byte
			var rejections []map[string]string
			if fc.before != nil {
				checkpoints = append(checkpoints, fc.before.Steps)
				states = append(states, fc.before.Initial)
				rejections = append(rejections, fc.before.Rejected)
			}
			for _, s := range fc.saves {
				if s.err == nil {
					checkpoints = append(checkpoints, s.handed.Steps)
					states = append(states, s.handed.Initial)
					rejections = append(rejections, s.handed.Rejected)
				}
			}
			// I11
			for i, steps := range checkpoints {
				if prev != nil {
					if lost := uncovered(prev, steps); len(lost) > 0 {
						errorf("call %d lost the records %v", n, lost)
					}
				}
				prev = steps
				if initial != nil && !bytes.Equal(initial, states[i]) {
					errorf("call %d: the initial state changed from %s to %s", n, initial, states[i])
				}
				initial = states[i]
				for p, reason := range rejected {
					if got, ok := rejections[i][p]; !ok || got != reason {
						errorf("call %d forgot the rejection of %s", n, p)
					}
				}
				rejected = rejections[i]
			}
			// I12
			for _, e := range fc.events {
				if e.Kind == flow.EventStarted {
					if m, done := finished[e.Path]; done {
						errorf("call %d started %s again; it finished in call %d", n, e.Path, m)
					}
				}
			}
			for _, e := range fc.events {
				if e.Kind == flow.EventFinished {
					finished[e.Path] = n
				}
			}
			if final == nil && (fc.err == nil || (errors.Is(fc.err, flow.ErrRejected) && !fc.cancelled() && !errors.Is(fc.err, flow.ErrCheckpoint))) {
				final = fc // its last save, with the outcome, went through
			}
		}
	}
}
