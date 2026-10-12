package flow_test

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"maps"
	"math/rand/v2"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/flow"
)

// The simulation builds a random step tree from a seed (sequences, parallels
// up to 12 wide, loops, routes, approvals, Named, leaves that read and write
// shared keys, some all the same value, so that order matters and branches can
// conflict) and runs it twice:
//
//   - clean: one Run with every approval decided up front and nothing failing.
//     Its outcome must be what reference, an independent interpreter of the
//     documented semantics, predicts: the same final state, or a failure of a
//     class the reference says can happen.
//   - history: the same tree with faults, resumed until it ends. Leaves fail or
//     panic on their first attempt at a path (after writing to the state), or
//     cancel the run's context (before or after doing their work); the context
//     is cancelled at the Nth event or after the Nth save; the Nth save fails;
//     the first Run decides all, some or none of the approvals, and a suspended
//     run is sometimes resumed with no decision at all.
//
// Every call goes through run/resume, so the invariants in invariants_test.go
// are checked on every call. On top, the simulation checks what needs the tree:
//
//	S1 the clean run matches the reference
//	S2 the history ends like the clean run: the same final state, and every step
//	   the clean run finished finished exactly once across the history (none run
//	   twice, none skipped, nothing else)
//	S3 no step function is running when Run or Resume returns
//	S4 every event and record is on a path the tree can produce, and every leaf
//	   runs at a path the tree gives to that leaf
//	S5 a cancelled run stops: in a tree without Parallel no step starts after
//	   the cancellation; a call cancelled while its root runs does not
//	   succeed; a call that was not cancelled never returns context.Canceled
//	S6 changing the state given to WithState after Run started, or the state a
//	   completed run returned, changes nothing
//	S7 resuming a suspended run without decisions runs no step function and
//	   suspends on the same approvals
//	S8 a rejection stays final when the run is resumed with an approval (I13)
//
// Branches of a Parallel whose branches are all leaves may be gated to return
// in an order the seed picks, so finish order is driven from the test side.
// TestSimulationCoversEveryCase checks the generator still produces every case.
// A failure names its seed; TestSimulatedFlows/seed=N reruns it.

type simKind int

const (
	kLeaf simKind = iota
	kApprove
	kSeq
	kPar
	kLoop
	kRoute
)

type simOp int

const (
	opWrite  simOp = iota // write = mix(id, read)
	opDelete              // delete write
	opIncr                // write++ (a loop's counter)
	opConst               // write = 7, the same in every leaf
)

type simFault int

const (
	fNone        simFault = iota
	fFail                 // write poison, fail
	fPanic                // write poison, panic
	fCancel               // cancel the run's context and return its error
	fCancelAfter          // do the work, cancel the run's context, return nil
)

// simNode is one step of a generated tree, in a form both the reference and
// the real tree are built from.
type simNode struct {
	id    int
	kind  simKind
	name  string // the name in its parent's paths
	inner string // when set, the step is flow.Named(name, a step called inner)
	kids  []*simNode
	keys  []string // kRoute: the key of each kid, sorted

	op          simOp
	read, write string
	fault       simFault
	gated       bool // in a gated Parallel: wait for branch gateAfter
	gateAfter   int
	index       int    // the branch index in its Parallel
	prevName    string // the name of branch gateAfter
	prevID      int    // and its node id
	max, target int    // kLoop; write is its counter
	unknown     bool   // kRoute: some key values lead nowhere
}

type sim struct {
	seed    uint64
	root    *simNode
	paths   map[string]*simNode // every path the tree can produce, and the step there
	hasPar  bool
	initial map[string]int

	cancelAtEvent, failSaveAt, cancelAfterSave int // 0: never
	firstDecisions                             int // the first Run decides 0: every approval, 1: some, 2: none
	idleResume                                 bool
}

var simNames = []string{"a", "ab", "b", "c"}

type simGen struct {
	r       *rand.Rand
	next    int
	written []string
	s       *sim
}

func (g *simGen) node(kind simKind, name string) *simNode {
	g.next++
	return &simNode{id: g.next, kind: kind, name: name}
}

func (g *simGen) pick(pool []string) string { return pool[g.r.IntN(len(pool))] }

func (g *simGen) readKey() string {
	return g.pick(append([]string{"s0", "s1"}, g.written...))
}

func (g *simGen) build(name string, depth int) *simNode {
	var n *simNode
	k := g.r.IntN(10)
	switch depth {
	case 0:
		k = g.r.IntN(3) // a leaf or an approval
	case 3:
		k = 3 + g.r.IntN(7) // the root is a combinator
	}
	switch {
	case k < 2:
		n = g.node(kLeaf, name)
		n.read = g.readKey()
		switch x := g.r.IntN(10); {
		case x < 6:
			n.write = fmt.Sprintf("k%d", n.id)
		case x < 8:
			n.write = g.pick([]string{"s0", "s1"})
		case x < 9:
			n.op, n.write = opConst, g.pick([]string{"s0", "s1"})
		default:
			n.op, n.write = opDelete, g.pick([]string{"s0", "s1"})
		}
		g.written = append(g.written, n.write)
		if g.r.IntN(5) == 0 {
			n.fault = simFault(1 + g.r.IntN(4))
		}
	case k == 2:
		n = g.node(kApprove, name)
	case k < 5:
		n = g.node(kSeq, name)
		for _, i := range g.r.Perm(len(simNames))[:1+g.r.IntN(3)] {
			n.kids = append(n.kids, g.build(simNames[i], depth-1))
		}
	case k < 7:
		g.s.hasPar = true
		n = g.node(kPar, name)
		fan := g.r.IntN(2) == 0 // a fan-out of leaves, which may be gated
		width := 1 + g.r.IntN(3)
		if fan {
			width = 2 + g.r.IntN(2)
			if g.r.IntN(6) == 0 {
				width = 11 + g.r.IntN(2) // "[10]" sorts before "[2]"
			}
		}
		for range width {
			if fan {
				n.kids = append(n.kids, g.build(g.pick(simNames), 0))
			} else {
				n.kids = append(n.kids, g.build(g.pick(simNames), depth-1))
			}
		}
		g.gate(n)
	case k < 8:
		n = g.node(kLoop, name)
		n.write = fmt.Sprintf("n%d", n.id)
		tick := g.node(kLeaf, "tick")
		tick.op, tick.read, tick.write = opIncr, n.write, n.write
		body := g.node(kSeq, "it")
		body.kids = []*simNode{tick, g.build(g.pick(simNames), depth-1)}
		n.kids = []*simNode{body}
		n.max = 1 + g.r.IntN(3)
		n.target = 1 + g.r.IntN(n.max)
		if g.r.IntN(8) == 0 {
			n.target = n.max + 1 // never reached: ErrMaxIterations
		}
	default:
		n = g.node(kRoute, name)
		n.read = g.readKey()
		n.unknown = g.r.IntN(8) == 0
		for _, key := range []string{"p", "q", "r"} {
			if len(n.keys) == 0 || g.r.IntN(2) == 0 {
				n.keys = append(n.keys, key)
				n.kids = append(n.kids, g.build(g.pick(simNames), depth-1))
			}
		}
	}
	if g.r.IntN(8) == 0 {
		n.inner = "inner"
	}
	return n
}

// gate makes the leaf branches of p return in an order the seed picks.
func (g *simGen) gate(p *simNode) {
	for i, b := range p.kids {
		b.index = i
		if b.kind != kLeaf {
			return
		}
	}
	if len(p.kids) < 2 || g.r.IntN(4) == 0 {
		return
	}
	order := g.r.Perm(len(p.kids))
	for pos := 1; pos < len(order); pos++ {
		b := p.kids[order[pos]]
		prev := p.kids[order[pos-1]]
		b.gated, b.gateAfter, b.prevName, b.prevID = true, order[pos-1], prev.name, prev.id
	}
}

func newSim(seed uint64) *sim {
	s := &sim{seed: seed, paths: map[string]*simNode{}, initial: map[string]int{}}
	g := &simGen{r: rand.New(rand.NewPCG(seed, 0x5eed)), s: s}
	s.root = g.build("main", 3)
	s.root.walk("main", s.paths)
	for _, k := range []string{"s0", "s1"} {
		if g.r.IntN(3) > 0 {
			s.initial[k] = g.r.IntN(1000)
		}
	}
	if g.r.IntN(3) == 0 {
		s.cancelAtEvent = 1 + g.r.IntN(30)
	}
	if g.r.IntN(4) == 0 {
		s.failSaveAt = 1 + g.r.IntN(15)
	}
	if g.r.IntN(4) == 0 {
		s.cancelAfterSave = 1 + g.r.IntN(15)
	}
	s.firstDecisions = g.r.IntN(3)
	s.idleResume = g.r.IntN(2) == 0
	return s
}

func (n *simNode) walk(path string, out map[string]*simNode) {
	out[path] = n
	for i, kid := range n.kids {
		switch n.kind {
		case kSeq:
			kid.walk(path+"/"+kid.name, out)
		case kPar:
			kid.walk(fmt.Sprintf("%s[%d]/%s", path, i, kid.name), out)
		case kLoop:
			for it := range n.max {
				kid.walk(fmt.Sprintf("%s[%d]/%s", path, it, kid.name), out)
			}
		case kRoute:
			kid.walk(fmt.Sprintf("%s[%s]/%s", path, n.keys[i], kid.name), out)
		}
	}
}

func (s *sim) approvals() []string {
	var out []string
	for p, n := range s.paths {
		if n.kind == kApprove {
			out = append(out, p)
		}
	}
	slices.Sort(out)
	return out
}

// decision is the human's answer at path: a few approvals are rejected.
func (s *sim) decision(path string) flow.Decision {
	h := fnv.New64a()
	_ = binary.Write(h, binary.LittleEndian, s.seed)
	h.Write([]byte(path))
	if h.Sum64()%100 < 10 {
		return flow.Decision{Reason: "no to " + path}
	}
	return flow.Decision{Approved: true}
}

func (s *sim) decisions(keep func(string) bool, approveAll bool) []any {
	var out []any
	for _, p := range s.approvals() {
		if keep(p) {
			d := s.decision(p)
			if approveAll {
				d = flow.Decision{Approved: true}
			}
			out = append(out, flow.WithDecision(p, d))
		}
	}
	return out
}

func mix(id, v int) int { return (v*31 + id*7 + 1) % 1000003 }

// ---- the reference: the documented semantics, restated on a plain map ----

type refFails map[string]bool

func union(a, b refFails) refFails {
	if a == nil {
		a = refFails{}
	}
	maps.Copy(a, b)
	return a
}

func (s *sim) ref(n *simNode, path string, st map[string]int) refFails {
	switch n.kind {
	case kLeaf:
		switch n.op {
		case opWrite:
			st[n.write] = mix(n.id, st[n.read])
		case opDelete:
			delete(st, n.write)
		case opIncr:
			st[n.write]++
		case opConst:
			st[n.write] = 7
		}
	case kApprove:
		if !s.decision(path).Approved {
			return refFails{"rejected": true}
		}
	case kSeq:
		for _, kid := range n.kids {
			if f := s.ref(kid, path+"/"+kid.name, st); f != nil {
				return f
			}
		}
	case kPar:
		// Each branch on a copy of the state as it was; any failing branch
		// may be the one reported; otherwise a key changed by two branches
		// is a conflict, and the changes are merged.
		base := maps.Clone(st)
		var fails refFails
		var branches []map[string]int
		for i, kid := range n.kids {
			b := maps.Clone(base)
			if f := s.ref(kid, fmt.Sprintf("%s[%d]/%s", path, i, kid.name), b); f != nil {
				fails = union(fails, f)
			}
			branches = append(branches, b)
		}
		if fails != nil {
			return fails
		}
		changedBy := map[string]int{}
		for _, b := range branches {
			for k := range union(refFails{}, keySet(b, base)) {
				v, inB := b[k]
				w, inBase := base[k]
				if inB != inBase || v != w {
					changedBy[k]++
				}
			}
		}
		for _, count := range changedBy {
			if count > 1 {
				return refFails{"conflict": true}
			}
		}
		for _, b := range branches {
			for k := range changedBy {
				v, inB := b[k]
				w, inBase := base[k]
				if inB != inBase || v != w {
					if inB {
						st[k] = v
					} else {
						delete(st, k)
					}
				}
			}
		}
	case kLoop:
		body := n.kids[0]
		for i := range n.max {
			if f := s.ref(body, fmt.Sprintf("%s[%d]/%s", path, i, body.name), st); f != nil {
				return f
			}
			if st[n.write] >= n.target {
				return nil
			}
		}
		return refFails{"maxiter": true}
	case kRoute:
		i := routeIndex(n, st[n.read])
		if i == len(n.keys) {
			return refFails{"unknownroute": true}
		}
		return s.ref(n.kids[i], fmt.Sprintf("%s[%s]/%s", path, n.keys[i], n.kids[i].name), st)
	}
	return nil
}

func keySet(ms ...map[string]int) refFails {
	out := refFails{}
	for _, m := range ms {
		for k := range m {
			out[k] = true
		}
	}
	return out
}

func routeIndex(n *simNode, v int) int {
	buckets := len(n.keys)
	if n.unknown {
		buckets++
	}
	return v % buckets
}

func simClass(err error) string {
	switch {
	case err == nil:
		return "completed"
	case errors.Is(err, flow.ErrRejected):
		return "rejected"
	case errors.Is(err, flow.ErrConflict):
		return "conflict"
	case errors.Is(err, flow.ErrMaxIterations):
		return "maxiter"
	case errors.Is(err, flow.ErrUnknownRoute):
		return "unknownroute"
	}
	return "other: " + err.Error()
}

// ---- the real tree, its faults, and what it observes ----

var errTransient = errors.New("transient failure")

// gateGuard bounds a gated leaf's wait, only so that a broken runner cannot
// hang the simulation.
const gateGuard = 5 * time.Second

// world is one simulated deployment of a tree: its checkpointer, its faults
// and what its steps and observer saw, across all calls of the run.
type world struct {
	sim       *sim
	faults    bool
	step      flow.Step
	mem       *flow.Memory
	cp        flow.Checkpointer
	seedState *flow.State

	inflight atomic.Int32
	leafRuns atomic.Int64

	mu               sync.Mutex
	cancel           context.CancelFunc
	cancelled        bool
	rootOpen         bool
	rootOpenAtCancel bool
	idle             bool // no faults fire: a resume that must run nothing
	gates            map[string]chan struct{}
	before           map[string]json.RawMessage
	attempts         map[string]int
	events, saves    int
	scribbled        bool
	finished         map[string]int
	problems         []string
}

func (s *sim) world(faults bool) *world {
	w := &world{sim: s, faults: faults, mem: flow.NewMemory(), attempts: map[string]int{}, finished: map[string]int{}}
	w.cp = &faultyCP{Checkpointer: w.mem, w: w}
	w.seedState = flow.NewState()
	for k, v := range s.initial {
		if err := flow.Set(w.seedState, k, v); err != nil {
			panic(err)
		}
	}
	w.step = w.build(s.root)
	return w
}

func (w *world) problem(format string, args ...any) {
	w.problems = append(w.problems, fmt.Sprintf(format, args...))
}

func (w *world) build(n *simNode) flow.Step {
	name := n.name
	if n.inner != "" {
		name = n.inner
	}
	var kids []flow.Step
	for _, kid := range n.kids {
		kids = append(kids, w.build(kid))
	}
	var s flow.Step
	switch n.kind {
	case kLeaf:
		s = flow.Func(name, w.leaf(n))
	case kApprove:
		s = flow.Approve(name)
	case kSeq:
		s = flow.Sequence(name, kids...)
	case kPar:
		s = flow.Parallel(name, kids...)
	case kLoop:
		s = flow.Loop(name, kids[0], func(st *flow.State) (bool, error) {
			v, err := getInt(st, n.write)
			return v >= n.target, err
		}, n.max)
	case kRoute:
		routes := map[string]flow.Step{}
		for i, k := range n.keys {
			routes[k] = kids[i]
		}
		s = flow.Route(name, func(st *flow.State) (string, error) {
			v, err := getInt(st, n.read)
			if i := routeIndex(n, v); i < len(n.keys) {
				return n.keys[i], err
			}
			return "zz", err
		}, routes)
	}
	if n.inner != "" {
		s = flow.Named(n.name, s)
	}
	return s
}

func getInt(st *flow.State, key string) (int, error) {
	v, err := flow.Get[int](st, key)
	if errors.Is(err, flow.ErrNotFound) {
		return 0, nil
	}
	return v, err
}

// gate returns the channel closed when the leaf at path returns in this call.
func (w *world) gate(path string) chan struct{} {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.gateLocked(path)
}

func (w *world) gateLocked(path string) chan struct{} {
	g, ok := w.gates[path]
	if !ok {
		g = make(chan struct{})
		w.gates[path] = g
	}
	return g
}

func (w *world) cancelLocked() {
	if w.cancel != nil && !w.cancelled {
		w.cancelled = true
		w.rootOpenAtCancel = w.rootOpen
		w.cancel()
	}
}

func (w *world) cancelNow() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.cancelLocked()
}

func (w *world) leaf(n *simNode) func(context.Context, *flow.State) error {
	return func(ctx context.Context, st *flow.State) error {
		w.inflight.Add(1)
		defer w.inflight.Add(-1)
		w.leafRuns.Add(1)
		_, path, ok := flow.StepPath(ctx)
		if !ok {
			return errors.New("StepPath not ok inside Run")
		}
		// S4: the step at this path is this leaf, not just any step.
		if w.sim.paths[path] != n {
			w.mu.Lock()
			w.problem("S4: leaf %d (%q) runs at %q, which the tree gives to another step", n.id, n.name, path)
			w.mu.Unlock()
		}
		// Gates are keyed by the Parallel's path and the leaf, so a wrong
		// branch index cannot leave a gate that never opens.
		done := w.gate(parentOf(path) + "#" + strconv.Itoa(n.id))
		defer close(done)

		w.mu.Lock()
		w.attempts[path]++
		first := w.attempts[path] == 1
		fault := w.faults && !w.idle && first
		if w.faults && !w.scribbled {
			// S6: the caller changes the state it gave WithState.
			w.scribbled = true
			_ = flow.Set(w.seedState, "s0", -7)
			w.seedState.Delete("s1")
			_ = flow.Set(w.seedState, "junk", 1)
		}
		var prevDone chan struct{}
		if n.gated {
			par := parentOf(path)
			if !coveredBy(w.before, fmt.Sprintf("%s[%d]/%s", par, n.gateAfter, n.prevName)) { // a skipped branch never returns
				prevDone = w.gateLocked(par + "#" + strconv.Itoa(n.prevID))
			}
		}
		w.mu.Unlock()
		if prevDone != nil {
			select {
			case <-prevDone:
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(gateGuard):
				// Only a broken runner gets here (the branch waited for
				// never ran in this call). Giving up loses the forced order
				// and nothing else, so this can never fail a correct run.
			}
		}

		if fault {
			switch n.fault {
			case fFail:
				_ = flow.Set(st, "poison", 1)
				return errTransient
			case fPanic:
				_ = flow.Set(st, "poison", 2)
				panic("simulated crash in " + path)
			case fCancel:
				w.cancelNow()
				return ctx.Err()
			case fCancelAfter:
				defer w.cancelNow()
			}
		}
		switch n.op {
		case opDelete:
			st.Delete(n.write)
			return nil
		case opConst:
			return flow.Set(st, n.write, 7)
		case opIncr:
			v, err := getInt(st, n.write)
			if err != nil {
				return err
			}
			return flow.Set(st, n.write, v+1)
		default:
			v, err := getInt(st, n.read)
			if err != nil {
				return err
			}
			return flow.Set(st, n.write, mix(n.id, v))
		}
	}
}

func (w *world) observe(e flow.Event) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, ok := w.sim.paths[e.Path]; !ok {
		w.problem("S4: event %s on %q, a path the tree cannot produce", e.Kind, e.Path)
	}
	if e.Path == "main" {
		w.rootOpen = e.Kind == flow.EventStarted
	}
	if e.Kind == flow.EventStarted && w.cancelled && !w.sim.hasPar {
		w.problem("S5: %s started after the run was cancelled", e.Path)
	}
	if e.Kind == flow.EventFinished {
		w.finished[e.Path]++
	}
	if w.faults && !w.idle {
		w.events++
		if w.events == w.sim.cancelAtEvent {
			w.cancelLocked()
		}
	}
}

// faultyCP fails or cancels at the save the seed picks, and checks every
// record's path.
type faultyCP struct {
	flow.Checkpointer
	w *world
}

func (f *faultyCP) Save(ctx context.Context, id string, c flow.Checkpoint) error {
	w := f.w
	w.mu.Lock()
	for k := range c.Steps {
		if _, ok := w.sim.paths[k]; !ok {
			w.problem("S4: record at %q, a path the tree cannot produce", k)
		}
	}
	n := 0
	if w.faults && !w.idle {
		w.saves++
		n = w.saves
	}
	w.mu.Unlock()
	if n != 0 && n == w.sim.failSaveAt {
		return errDisk
	}
	err := f.Checkpointer.Save(ctx, id, c)
	if n != 0 && n == w.sim.cancelAfterSave {
		w.cancelNow()
	}
	return err
}

// quiet turns the faults off (or on again) for the calls that follow.
func (w *world) quiet(on bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.idle = on
}

// call runs (or resumes, if the run has a checkpoint) the tree once.
func (w *world) call(t *testing.T, opts ...any) (*flow.State, error) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	stored, err := w.mem.Load(ctx, "sim")
	exists := err == nil
	w.mu.Lock()
	w.cancel, w.cancelled, w.rootOpen, w.rootOpenAtCancel = cancel, false, false, false
	w.gates, w.before = map[string]chan struct{}{}, stored.Steps
	w.mu.Unlock()

	opts = append(opts, observer(w.observe))
	var st *flow.State
	if exists {
		st, err = resumeCtx(ctx, t, w.cp, "sim", w.step, opts...)
	} else {
		st, err = runCtx(ctx, t, w.cp, "sim", w.step, append(opts, flow.WithState(w.seedState))...)
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	if n := w.inflight.Load(); n != 0 {
		w.problem("S3: %d step functions still running after the call returned", n)
	}
	switch {
	case w.cancelled && w.rootOpenAtCancel && err == nil:
		// Not "returns the context's error": a step that failed on its own
		// at the same time is reported instead (FuzzSimulatedFlow found one).
		w.problem("S5: cancelled while the root ran, but the call succeeded")
	case !w.cancelled && errors.Is(err, context.Canceled):
		w.problem("S5: not cancelled, but returned %v", err)
	}
	for _, p := range w.problems {
		t.Errorf("seed %d: %s", w.sim.seed, p)
	}
	w.problems = nil
	return st, err
}

func transient(err error) bool {
	var pe *flow.PanicError
	return errors.Is(err, errTransient) || errors.As(err, &pe) || errors.Is(err, context.Canceled) ||
		errors.Is(err, flow.ErrCheckpoint) || errors.Is(err, flow.ErrSuspended)
}

func stateMap(t *testing.T, st *flow.State) map[string]int {
	t.Helper()
	raw, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]int{}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("state %s: %v", raw, err)
	}
	return m
}

func simulateSeed(t *testing.T, seed uint64) {
	t.Helper()
	s := newSim(seed)
	all := func(string) bool { return true }

	// S1: the clean run against the reference.
	want := maps.Clone(s.initial)
	wantFails := s.ref(s.root, "main", want)
	clean := s.world(false)
	cleanSt, cleanErr := clean.call(t, s.decisions(all, false)...)
	if wantFails == nil {
		if cleanErr != nil {
			t.Fatalf("seed %d: the clean run failed with %v; the reference completes with %v", seed, cleanErr, want)
		}
		if got := stateMap(t, cleanSt); !maps.Equal(got, want) {
			t.Fatalf("seed %d: the clean run ended with %v, the reference with %v", seed, got, want)
		}
	} else if class := simClass(cleanErr); !wantFails[class] {
		t.Fatalf("seed %d: the clean run ended %s (%v), the reference says one of %v", seed, class, cleanErr, wantFails)
	}

	// S2: the same tree, crashed and resumed until it ends.
	h := s.world(true)
	r := rand.New(rand.NewPCG(seed, 2))
	var opts []any
	switch s.firstDecisions {
	case 0:
		opts = s.decisions(all, false)
	case 1:
		opts = s.decisions(func(string) bool { return r.IntN(2) == 0 }, false)
	}
	var st *flow.State
	var err error
	for n := 0; ; n++ {
		if n == 100 {
			t.Fatalf("seed %d: the run has not ended after %d calls: %v", seed, n, err)
		}
		st, err = h.call(t, opts...)
		var susp *flow.SuspendedError
		if errors.As(err, &susp) && s.idleResume {
			// S7: no decision, nothing runs, the same approvals wait.
			h.quiet(true)
			runs := h.leafRuns.Load()
			_, again := h.call(t)
			var susp2 *flow.SuspendedError
			if !errors.As(again, &susp2) || !slices.Equal(susp.Paths, susp2.Paths) {
				t.Errorf("seed %d: resuming without decisions returned %v, want suspended on %v", seed, again, susp.Paths)
			}
			if got := h.leafRuns.Load(); got != runs {
				t.Errorf("seed %d: resuming without decisions ran %d step functions", seed, got-runs)
			}
			h.quiet(false)
		}
		opts = s.decisions(all, false)
		if !transient(err) {
			break
		}
	}
	if simClass(err) != simClass(cleanErr) && (err == nil || cleanErr == nil || !wantFails[simClass(err)]) {
		t.Fatalf("seed %d: the resumed run ended %s (%v), the clean run %s (%v)", seed, simClass(err), err, simClass(cleanErr), cleanErr)
	}
	h.quiet(true) // what follows checks the outcome; no more faults
	if err == nil {
		if got, want := stateMap(t, st), stateMap(t, cleanSt); !maps.Equal(got, want) {
			t.Errorf("seed %d: the resumed run ended with %v, the clean run with %v", seed, got, want)
		}
		if !maps.Equal(h.finished, clean.finished) {
			t.Errorf("seed %d: steps finished across the history %v, in the clean run %v", seed, h.finished, clean.finished)
		}
		// S6: changing the returned state changes nothing (I13 compares
		// with the state as returned).
		_ = flow.Set(st, "junk", 2)
		st.Delete("s0")
		if _, err := h.call(t, opts...); err != nil {
			t.Errorf("seed %d: resuming the completed run: %v", seed, err)
		}
	}
	if errors.Is(err, flow.ErrRejected) {
		// S8: I13 checks that this returns ErrRejected again.
		_, _ = h.call(t, s.decisions(all, true)...)
	}
}

const simSeeds = 300

func TestSimulatedFlows(t *testing.T) {
	t.Parallel()
	for seed := uint64(1); seed <= simSeeds; seed++ {
		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
			t.Parallel()
			simulateSeed(t, seed)
		})
	}
}

func FuzzSimulatedFlow(f *testing.F) {
	for _, seed := range []uint64{0, 1, 2, 3, 42, 1 << 40} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, seed uint64) { simulateSeed(t, seed) })
}

// The simulation is only as good as what it generates. This counts, from the
// generator and the reference alone (nothing runs), how many of the seeds
// TestSimulatedFlows uses exercise each case, so a change to the generator
// cannot quietly stop producing one.
func TestSimulationCoversEveryCase(t *testing.T) {
	t.Parallel()
	got := map[string]int{}
	for seed := uint64(1); seed <= simSeeds; seed++ {
		s := newSim(seed)
		for class := range s.ref(s.root, "main", maps.Clone(s.initial)) {
			got["fails: "+class]++
		}
		if len(s.ref(s.root, "main", maps.Clone(s.initial))) == 0 {
			got["completes"]++
		}
		seen := map[string]bool{}
		for _, n := range s.paths {
			switch {
			case n.kind == kPar && len(n.kids) > 10:
				seen["a Parallel of more than 10 branches"] = true
			case n.kind == kPar:
				seen["a Parallel"] = true
			case n.kind == kLoop:
				seen["a Loop"] = true
			case n.kind == kRoute:
				seen["a Route"] = true
			case n.kind == kApprove:
				seen["an approval"] = true
			}
			if n.gated {
				seen["gated branches"] = true
			}
			if n.inner != "" {
				seen["Named"] = true
			}
			if n.fault != fNone {
				seen[fmt.Sprintf("fault %d", n.fault)] = true
			}
			if n.op == opConst {
				seen["equal writes"] = true
			}
		}
		if s.cancelAtEvent > 0 {
			seen["cancel at an event"] = true
		}
		if s.failSaveAt > 0 {
			seen["a failed save"] = true
		}
		if s.cancelAfterSave > 0 {
			seen["cancel after a save"] = true
		}
		if s.firstDecisions == 2 {
			seen["a first Run with no decision"] = true
		}
		for k := range seen {
			got[k]++
		}
	}
	for _, want := range []string{
		"completes", "fails: rejected", "fails: conflict", "fails: maxiter", "fails: unknownroute",
		"a Parallel", "a Parallel of more than 10 branches", "a Loop", "a Route", "an approval",
		"gated branches", "Named", "fault 1", "fault 2", "fault 3", "fault 4", "equal writes",
		"cancel at an event", "a failed save", "cancel after a save", "a first Run with no decision",
	} {
		if got[want] < 5 {
			t.Errorf("%d of %d seeds have %s, want at least 5", got[want], simSeeds, want)
		}
	}
	if got["completes"] < simSeeds/2 {
		t.Errorf("%d of %d seeds complete; the resume comparison needs most of them to", got["completes"], simSeeds)
	}
}
