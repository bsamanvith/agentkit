package flow_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/bsamanvith/agentkit/internal/testkit"
	"github.com/bsamanvith/agentkit/pkg/flow"
	"github.com/bsamanvith/agentkit/pkg/flow/flowtest"
)

func noop(name string) flow.Step {
	return flow.Func(name, func(context.Context, *flow.State) error { return nil })
}

func never(*flow.State) (bool, error) { return false, nil }

func key(string) func(*flow.State) (string, error) {
	return func(*flow.State) (string, error) { return "k00", nil }
}

// referenceTree uses every kind of step, including a route with many keys
// (so that a fingerprint that follows map order shows up). salt changes every
// closure but not the shape.
func referenceTree(salt string) flow.Step {
	routes := map[string]flow.Step{}
	for i := range 12 {
		routes[fmt.Sprintf("k%02d", i)] = noop(fmt.Sprintf("handle%02d", i))
	}
	return flow.Sequence("main",
		flow.Func("prepare", func(context.Context, *flow.State) error { _ = salt; return nil }),
		flow.Parallel("gather",
			noop("web"),
			flow.Sequence("db", noop("q1"), noop("q2"))),
		flow.Loop("refine", noop("draft"), func(*flow.State) (bool, error) { _ = salt; return true, nil }, 4),
		flow.Route("pick", key(salt), routes),
		flow.Named("sign", flow.Approve("ignored")),
		noop("note-\U0001FAE9"), // assigned in Unicode 16: must not depend on the toolchain's tables
		noop("publish"))
}

func fingerprint(t testing.TB, s flow.Step) string {
	t.Helper()
	fp, err := flow.Fingerprint(s)
	if err != nil {
		t.Fatalf("Fingerprint: %v", err)
	}
	return fp
}

func TestFingerprintIsStable(t *testing.T) {
	t.Parallel()
	want := fingerprint(t, referenceTree("0"))

	// The same tree built again, with other closures and other map layouts,
	// has the same fingerprint.
	for i := range 100 {
		if got := fingerprint(t, referenceTree(fmt.Sprint(i))); got != want {
			t.Fatalf("rebuild %d: fingerprint %s, want %s", i, got, want)
		}
	}
	// A pinned value is what makes it the same in every process and release.
	testkit.Golden(t, "testdata/shape_fingerprint.txt", []byte(want+"\n"))
}

func TestFingerprintChangesWithTheShape(t *testing.T) {
	t.Parallel()
	a, b, c := noop("a"), noop("b"), noop("c")
	routeOf := func(m map[string]flow.Step) flow.Step { return flow.Route("r", key(""), m) }

	trees := map[string]flow.Step{
		"base":                    flow.Sequence("s", a, b),
		"step added":              flow.Sequence("s", a, b, c),
		"step removed":            flow.Sequence("s", a),
		"step renamed":            flow.Sequence("s", a, noop("b2")),
		"root renamed":            flow.Sequence("s2", a, b),
		"steps reordered":         flow.Sequence("s", b, a),
		"sequence to parallel":    flow.Parallel("s", a, b),
		"branches reordered":      flow.Parallel("s", b, a),
		"approval for a func":     flow.Sequence("s", a, flow.Approve("b")),
		"nested together":         flow.Sequence("s", flow.Sequence("x", a, b)),
		"nested first":            flow.Sequence("s", flow.Sequence("x", a), b),
		"nested last":             flow.Sequence("s", a, flow.Sequence("x", b)),
		"forged by a quoted name": flow.Sequence("s", noop(`a"),step("b`)),
		"forged by a name":        flow.Sequence("s", noop("a),step(b")),
		"forged by a root name":   flow.Sequence("s,step(a),step(b", noop("c")),
		"forged by a route key":   routeOf(map[string]flow.Step{"x=step(1:a),y": b}),
		"forged by a loop name":   flow.Loop("l,3,step(a", b, never, 3),
		"route":                   routeOf(map[string]flow.Step{"x": a, "y": b}),
		"route key renamed":       routeOf(map[string]flow.Step{"x": a, "z": b}),
		"route children swap":     routeOf(map[string]flow.Step{"x": b, "y": a}),
		"route key added":         routeOf(map[string]flow.Step{"x": a, "y": b, "z": c}),
		"route child renamed":     routeOf(map[string]flow.Step{"x": a, "y": noop("b2")}),
		"loop":                    flow.Loop("l", a, never, 3),
		"loop maximum":            flow.Loop("l", a, never, 4),
		"loop body renamed":       flow.Loop("l", b, never, 3),
		"loop to sequence":        flow.Sequence("l", a),
		"loop of a loop":          flow.Loop("l", flow.Loop("a", b, never, 3), never, 3),
		"parallel of sequences":   flow.Parallel("s", flow.Sequence("a", b), c),
	}
	seen := map[string]string{}
	for name, tree := range trees {
		fp := fingerprint(t, tree)
		if other, dup := seen[fp]; dup {
			t.Errorf("%q and %q have the same fingerprint %s", name, other, fp)
		}
		seen[fp] = name
	}
}

func TestFingerprintIgnoresWhatDoesNotChangeTheShape(t *testing.T) {
	t.Parallel()
	base := fingerprint(t, flow.Sequence("s", noop("a"), flow.Approve("b"), flow.Route("r", key(""),
		map[string]flow.Step{"x": noop("p"), "y": noop("q")})))

	same := map[string]flow.Step{
		"other closures, route map in another order": flow.Sequence("s",
			flow.Func("a", func(context.Context, *flow.State) error { return errBoom }),
			flow.Approve("b"),
			flow.Route("r", func(*flow.State) (string, error) { return "y", nil },
				map[string]flow.Step{"y": noop("q"), "x": noop("p")})),
		"Named with the name of the step it wraps": flow.Sequence("s",
			flow.Named("a", noop("whatever")),
			flow.Named("b", flow.Approve("other")),
			flow.Route("r", key(""), map[string]flow.Step{"x": noop("p"), "y": noop("q")})),
	}
	for name, tree := range same {
		if got := fingerprint(t, tree); got != base {
			t.Errorf("%s: fingerprint %s, want %s", name, got, base)
		}
	}
}

func TestFingerprintOfAnInvalidTreeIsAnError(t *testing.T) {
	t.Parallel()
	for name, tree := range map[string]flow.Step{
		"nil":             nil,
		"duplicate names": flow.Sequence("s", noop("a"), noop("a")),
		"bad name":        flow.Sequence("s", noop("a/b")),
	} {
		if fp, err := flow.Fingerprint(tree); !errors.Is(err, flow.ErrInvalidFlow) || fp != "" {
			t.Errorf("%s: Fingerprint = (%q, %v), want ErrInvalidFlow", name, fp, err)
		}
	}
}

// approvalFlow is a run that stops at an approval, so it can be resumed with a
// tree of another shape. renamed changes the name of the step after it.
func approvalFlow(c *calls, last string) flow.Step {
	return flow.Sequence("main", trail(c, "a"), flow.Approve("ok"), trail(c, last))
}

var approved = flow.WithDecision("main/ok", flow.Decision{Approved: true})

func TestResumeOfAChangedTreeFailsWithErrShapeChanged(t *testing.T) {
	t.Parallel()
	var c calls
	cp := flow.NewMemory()
	old, changed := approvalFlow(&c, "b"), approvalFlow(&c, "b2")
	if _, err := run(t, cp, "r", old); !errors.Is(err, flow.ErrSuspended) {
		t.Fatalf("Run error = %v, want ErrSuspended", err)
	}
	before := load(t, cp, "r")
	var rec flowtest.Recorder

	_, err := resume(t, cp, "r", changed, approved, flow.WithObserver(rec.Observe))
	if !errors.Is(err, flow.ErrShapeChanged) {
		t.Fatalf("Resume error = %v, want ErrShapeChanged", err)
	}
	var sce *flow.ShapeChangedError
	if !errors.As(err, &sce) {
		t.Fatalf("Resume error %v is not a ShapeChangedError", err)
	}
	if sce.RunID != "r" || sce.Stored != fingerprint(t, old) || sce.Current != fingerprint(t, changed) {
		t.Errorf("error = %+v, want run r, stored %s, current %s", sce, fingerprint(t, old), fingerprint(t, changed))
	}
	// Nothing happened: no step ran, no event, the checkpoint is as it was.
	equalStrings(t, "steps run", c.all(), []string{"a"})
	if got := rec.Events(); len(got) != 0 {
		t.Errorf("events = %v, want none", got)
	}
	if after := load(t, cp, "r"); !reflect.DeepEqual(after, before) {
		t.Errorf("the checkpoint changed: %+v, was %+v", after, before)
	}

	// Resuming with the tree that wrote it still works.
	st, err := resume(t, cp, "r", old, approved)
	if err != nil {
		t.Fatalf("Resume with the original tree: %v", err)
	}
	equalStrings(t, "trail", mustGet[[]string](t, st, "trail"), []string{"a", "b"})
}

func TestResumeWithAllowShapeChangeProceedsAndWarns(t *testing.T) {
	t.Parallel()
	var c calls
	cp := flow.NewMemory()
	old, changed := approvalFlow(&c, "b"), approvalFlow(&c, "b2")
	if _, err := run(t, cp, "r", old); !errors.Is(err, flow.ErrSuspended) {
		t.Fatalf("Run error = %v, want ErrSuspended", err)
	}
	var rec flowtest.Recorder

	st, err := resume(t, cp, "r", changed, approved, flow.WithAllowShapeChange(), flow.WithObserver(rec.Observe))
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	equalStrings(t, "trail", mustGet[[]string](t, st, "trail"), []string{"a", "b2"})

	events := rec.Events()
	var sce *flow.ShapeChangedError
	if len(events) == 0 || events[0].Kind != flow.EventWarning || events[0].Path != "main" || !errors.As(events[0].Err, &sce) {
		t.Fatalf("first event = %+v, want a warning on main carrying a ShapeChangedError", events)
	}
	if sce.Stored != fingerprint(t, old) || sce.Current != fingerprint(t, changed) {
		t.Errorf("warning = %+v, want stored %s, current %s", sce, fingerprint(t, old), fingerprint(t, changed))
	}
	// The run now belongs to the new tree: it resumes without the opt-in.
	if got := load(t, cp, "r").Shape; got != fingerprint(t, changed) {
		t.Errorf("checkpoint shape = %s, want the new tree's %s", got, fingerprint(t, changed))
	}
	if _, err := resume(t, cp, "r", changed); err != nil {
		t.Errorf("Resume of the adopted run: %v", err)
	}
}

func TestResumeWithTheSameTreeWarnsAboutNothing(t *testing.T) {
	t.Parallel()
	var c calls
	cp := flow.NewMemory()
	if _, err := run(t, cp, "r", approvalFlow(&c, "b")); !errors.Is(err, flow.ErrSuspended) {
		t.Fatalf("Run error = %v, want ErrSuspended", err)
	}
	var rec flowtest.Recorder
	// A tree built again (new closures) is the same tree.
	if _, err := resume(t, cp, "r", approvalFlow(&c, "b"), approved, flow.WithObserver(rec.Observe)); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	for _, e := range rec.Events() {
		if e.Kind == flow.EventWarning {
			t.Errorf("unexpected warning %+v", e)
		}
	}
}

// reviewFlow is the tree that wrote testdata/checkpoint_suspended*.json.
func reviewFlow(c *calls) flow.Step {
	return flow.Sequence("review",
		trail(c, "draft"),
		flow.Parallel("check", trail(c, "lint"), flow.Approve("legal")),
		trail(c, "publish"))
}

// readCheckpoint loads a checkpoint fixture.
func readCheckpoint(t *testing.T, file string) flow.Checkpoint {
	t.Helper()
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var ckpt flow.Checkpoint
	if err := json.Unmarshal(raw, &ckpt); err != nil {
		t.Fatalf("%s: %v", file, err)
	}
	return ckpt
}

func TestResumeOfACheckpointWithoutAShapeStillRuns(t *testing.T) {
	t.Parallel()
	// This file is a checkpoint as written before fingerprints existed.
	old := readCheckpoint(t, "testdata/checkpoint_suspended_no_shape.json")
	if old.Shape != "" {
		t.Fatalf("fixture has shape %q, it must not have one", old.Shape)
	}
	var c calls
	cp := flow.NewMemory()
	if err := cp.Save(t.Context(), "r", old); err != nil {
		t.Fatal(err)
	}
	var rec flowtest.Recorder

	st, err := resume(t, cp, "r", reviewFlow(&c),
		flow.WithDecision("review/check[1]/legal", flow.Decision{Approved: true}), flow.WithObserver(rec.Observe))
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	equalStrings(t, "steps run", c.all(), []string{"publish"})
	equalStrings(t, "trail", mustGet[[]string](t, st, "trail"), []string{"draft", "lint", "publish"})

	events := rec.Events()
	if len(events) == 0 || events[0].Kind != flow.EventWarning || !errors.Is(events[0].Err, flow.ErrNoShape) {
		t.Errorf("first event = %+v, want a warning matching ErrNoShape", events)
	}
	// From now on the run has a fingerprint.
	if got, want := load(t, cp, "r").Shape, fingerprint(t, reviewFlow(&c)); got != want {
		t.Errorf("checkpoint shape = %q, want %q", got, want)
	}
}

// A fingerprint made by another version of the encoding cannot be compared with
// the current one, so it is no evidence that the tree changed. The first file
// is a checkpoint as it was written when the encoding was still v1: the same
// tree as reviewFlow, with a fingerprint that differs only because of the
// encoding.
func TestResumeOfACheckpointWithAFingerprintOfAnotherVersionStillRuns(t *testing.T) {
	t.Parallel()
	v1 := readCheckpoint(t, "testdata/checkpoint_suspended_v1_shape.json")
	if !strings.HasPrefix(v1.Shape, "v1:") {
		t.Fatalf("fixture has shape %q, want a v1 fingerprint", v1.Shape)
	}
	for name, shape := range map[string]string{
		"the v1 encoding":   v1.Shape,
		"a later version":   "v99:abc",
		"no version":        "abc",
		"only a version":    "v2",
		"a version in vain": "x" + v1.Shape,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var c calls
			cp := flow.NewMemory()
			stored := v1.Clone()
			stored.Shape = shape
			if err := cp.Save(t.Context(), "r", stored); err != nil {
				t.Fatal(err)
			}
			var rec flowtest.Recorder

			st, err := resume(t, cp, "r", reviewFlow(&c),
				flow.WithDecision("review/check[1]/legal", flow.Decision{Approved: true}), flow.WithObserver(rec.Observe))
			if err != nil {
				t.Fatalf("Resume: %v", err)
			}
			equalStrings(t, "steps run", c.all(), []string{"publish"})
			equalStrings(t, "trail", mustGet[[]string](t, st, "trail"), []string{"draft", "lint", "publish"})
			events := rec.Events()
			if len(events) == 0 || events[0].Kind != flow.EventWarning || !errors.Is(events[0].Err, flow.ErrNoShape) || errors.Is(events[0].Err, flow.ErrShapeChanged) {
				t.Errorf("first event = %+v, want a warning matching ErrNoShape and not ErrShapeChanged", events)
			}
			if got, want := load(t, cp, "r").Shape, fingerprint(t, reviewFlow(&c)); got != want {
				t.Errorf("checkpoint shape = %q, want the current %q", got, want)
			}
		})
	}
}

// recording is a Checkpointer that keeps every checkpoint it is asked to save.
type recording struct {
	flow.Checkpointer
	mu    sync.Mutex
	saved []flow.Checkpoint
}

func (r *recording) Save(ctx context.Context, id string, c flow.Checkpoint) error {
	r.mu.Lock()
	r.saved = append(r.saved, c.Clone())
	r.mu.Unlock()
	return r.Checkpointer.Save(ctx, id, c)
}

func (r *recording) shapes() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, c := range r.saved {
		out = append(out, c.Shape)
	}
	return out
}

// Whatever a run does, every checkpoint it saves says which tree wrote it.
func TestEveryCheckpointCarriesTheFingerprint(t *testing.T) {
	t.Parallel()
	check := func(t *testing.T, what string, r *recording, from int, want string) {
		t.Helper()
		shapes := r.shapes()
		if len(shapes) <= from {
			t.Fatalf("%s: saved %d checkpoints, want more than %d", what, len(shapes), from)
		}
		for i, got := range shapes[from:] {
			if got != want {
				t.Errorf("%s: save %d has shape %q, want %q", what, from+i, got, want)
			}
		}
	}

	t.Run("a run that fails, then resumes", func(t *testing.T) {
		t.Parallel()
		var c calls
		r := &recording{Checkpointer: flow.NewMemory()}
		tree := flow.Sequence("seq", trail(&c, "a"), failOnce(&c, "b"), trail(&c, "c"))
		if _, err := run(t, r, "r", tree); !errors.Is(err, errBoom) {
			t.Fatalf("Run error = %v, want errBoom", err)
		}
		check(t, "run", r, 0, fingerprint(t, tree))
		n := len(r.shapes())
		if _, err := resume(t, r, "r", tree); err != nil {
			t.Fatalf("Resume: %v", err)
		}
		check(t, "resume", r, n, fingerprint(t, tree))
	})

	t.Run("a checkpoint without a shape", func(t *testing.T) {
		t.Parallel()
		var c calls
		r := &recording{Checkpointer: flow.NewMemory()}
		if err := r.Checkpointer.Save(t.Context(), "r", readCheckpoint(t, "testdata/checkpoint_suspended_no_shape.json")); err != nil {
			t.Fatal(err)
		}
		approve := flow.WithDecision("review/check[1]/legal", flow.Decision{Approved: true})
		if _, err := resume(t, r, "r", reviewFlow(&c), approve); err != nil {
			t.Fatalf("Resume: %v", err)
		}
		check(t, "resume", r, 0, fingerprint(t, reviewFlow(&c)))
	})

	t.Run("a changed tree with the opt-in", func(t *testing.T) {
		t.Parallel()
		var c calls
		r := &recording{Checkpointer: flow.NewMemory()}
		if _, err := run(t, r, "r", approvalFlow(&c, "b")); !errors.Is(err, flow.ErrSuspended) {
			t.Fatalf("Run error = %v, want ErrSuspended", err)
		}
		n := len(r.shapes())
		changed := approvalFlow(&c, "b2")
		if _, err := resume(t, r, "r", changed, approved, flow.WithAllowShapeChange()); err != nil {
			t.Fatalf("Resume: %v", err)
		}
		check(t, "resume", r, n, fingerprint(t, changed))
	})
}

// spec describes a tree in a way that does not go through the fingerprint's
// encoding: two trees have the same spec exactly when they have the same
// names, kinds, nesting, route keys and loop maxima.
type spec struct {
	Kind string
	Name string
	Max  int      `json:",omitempty"`
	Keys []string `json:",omitempty"`
	Kids []spec   `json:",omitempty"`
}

// treeGen builds small random trees and their spec from a seed. The names
// include ones that try to forge another tree's text, and the pools are small
// so that different seeds often build the same tree.
type treeGen struct{ r *rand.Rand }

var namePool = []string{"a", "b", "c", `a"`, `b"),step("c`, "a),step(b", "é"}

var routeKeys = []string{"x", "y", "z", "x=step(1:a),y"}

func newTreeGen(seed uint64) *treeGen { return &treeGen{rand.New(rand.NewPCG(seed, 1))} }

func (g *treeGen) pick(pool []string) string { return pool[g.r.IntN(len(pool))] }

// build returns a step named name and its spec, nested at most depth deep.
func (g *treeGen) build(name string, depth int) (flow.Step, spec) {
	kinds := 2
	if depth > 0 {
		kinds = 7
	}
	switch g.r.IntN(kinds) {
	case 0:
		return noop(name), spec{Kind: "step", Name: name}
	case 1:
		return flow.Approve(name), spec{Kind: "approve", Name: name}
	case 2: // Sequence: distinct child names
		var steps []flow.Step
		sp := spec{Kind: "seq", Name: name}
		for _, i := range g.r.Perm(len(namePool))[:1+g.r.IntN(3)] {
			s, k := g.build(namePool[i], depth-1)
			steps, sp.Kids = append(steps, s), append(sp.Kids, k)
		}
		return flow.Sequence(name, steps...), sp
	case 3:
		var steps []flow.Step
		sp := spec{Kind: "par", Name: name}
		for range 1 + g.r.IntN(3) {
			s, k := g.build(g.pick(namePool), depth-1)
			steps, sp.Kids = append(steps, s), append(sp.Kids, k)
		}
		return flow.Parallel(name, steps...), sp
	case 4:
		routes := map[string]flow.Step{}
		sp := spec{Kind: "route", Name: name}
		keys := slices.Clone(routeKeys)
		g.r.Shuffle(len(keys), func(i, j int) { keys[i], keys[j] = keys[j], keys[i] })
		kids := map[string]spec{}
		for _, k := range keys[:1+g.r.IntN(len(keys))] {
			routes[k], kids[k] = g.build(g.pick(namePool), depth-1)
		}
		for _, k := range routeKeys {
			if kid, ok := kids[k]; ok {
				sp.Keys, sp.Kids = append(sp.Keys, k), append(sp.Kids, kid)
			}
		}
		return flow.Route(name, key(""), routes), sp
	case 5:
		body, k := g.build(g.pick(namePool), depth-1)
		max := 1 + g.r.IntN(3)
		return flow.Loop(name, body, never, max), spec{Kind: "loop", Name: name, Max: max, Kids: []spec{k}}
	default: // Named renames whatever it wraps
		inner, sp := g.build(g.pick(namePool), depth-1)
		sp.Name = name
		return flow.Named(name, inner), sp
	}
}

func (g *treeGen) tree() (flow.Step, string) {
	s, sp := g.build(g.pick(namePool), 2)
	out, err := json.Marshal(sp)
	if err != nil {
		panic(err)
	}
	return s, string(out)
}

// checkFingerprintMatchesStructure builds two trees from the seeds and requires
// their fingerprints to be equal exactly when their specs are.
func checkFingerprintMatchesStructure(t *testing.T, seedA, seedB uint64) {
	t.Helper()
	a, specA := newTreeGen(seedA).tree()
	b, specB := newTreeGen(seedB).tree()
	fa, fb := fingerprint(t, a), fingerprint(t, b)
	if (fa == fb) != (specA == specB) {
		t.Fatalf("seeds %d and %d: fingerprints equal = %v, structures equal = %v\n%s\n%s",
			seedA, seedB, fa == fb, specA == specB, specA, specB)
	}
	// The same seed builds the tree again, with new closures and new maps.
	again, _ := newTreeGen(seedA).tree()
	if got := fingerprint(t, again); got != fa {
		t.Fatalf("seed %d: rebuilt tree has fingerprint %s, want %s", seedA, got, fa)
	}
}

// Across thousands of generated trees (about 1,800 distinct ones, many built
// from more than one seed), the fingerprint and the spec identify each other:
// no two different structures share a fingerprint, and one structure never
// has two.
func TestFingerprintMatchesStructureSimulation(t *testing.T) {
	t.Parallel()
	byFingerprint := map[string]string{}
	bySpec := map[string]string{}
	for seed := range uint64(3000) {
		tree, sp := newTreeGen(seed).tree()
		fp := fingerprint(t, tree)
		if other, ok := byFingerprint[fp]; ok && other != sp {
			t.Fatalf("seed %d: different structures share fingerprint %s:\n%s\n%s", seed, fp, sp, other)
		}
		if other, ok := bySpec[sp]; ok && other != fp {
			t.Fatalf("seed %d: structure %s has fingerprints %s and %s", seed, sp, fp, other)
		}
		byFingerprint[fp], bySpec[sp] = sp, fp
	}
	if len(bySpec) < 1000 || len(bySpec) == 3000 {
		t.Fatalf("the generator made %d distinct trees from 3000 seeds, want many but with repeats", len(bySpec))
	}
}

func FuzzFingerprintMatchesStructure(f *testing.F) {
	f.Add(uint64(0), uint64(0))
	f.Add(uint64(1), uint64(2))
	f.Add(uint64(1<<63), uint64(7))
	f.Fuzz(checkFingerprintMatchesStructure)
}
