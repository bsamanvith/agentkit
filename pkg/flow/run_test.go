package flow_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/bsamanvith/agentkit/internal/testkit"
	"github.com/bsamanvith/agentkit/pkg/flow"
	"github.com/bsamanvith/agentkit/pkg/flow/flowtest"
)

func TestMemoryCheckpointer(t *testing.T) {
	flowtest.RunCheckpointer(t, func(*testing.T) flow.Checkpointer { return flow.NewMemory() })
}

// unnamed is a Step with no Name method.
type unnamed struct{}

func (unnamed) Run(context.Context, *flow.State) error { return nil }

func TestValidateRejectsMalformedFlows(t *testing.T) {
	t.Parallel()
	noop := func(name string) flow.Step {
		return flow.Func(name, func(context.Context, *flow.State) error { return nil })
	}
	always := func(*flow.State) (bool, error) { return true, nil }
	key := func(*flow.State) (string, error) { return "k", nil }

	bad := map[string]flow.Step{
		"step without a name":     flow.Sequence("s", unnamed{}),
		"empty name":              flow.Sequence("s", noop("")),
		"slash in name":           flow.Sequence("s", noop("a/b")),
		"bracket in name":         flow.Sequence("s", noop("a[0]")),
		"duplicate sibling names": flow.Sequence("s", noop("a"), noop("a")),
		"nil child":               flow.Sequence("s", nil),
		"nil function":            flow.Sequence("s", flow.Func("f", nil)),
		"unnamed root":            unnamed{},
		"nested problem":          flow.Loop("l", flow.Parallel("p", noop("ok"), flow.Sequence("s", noop("x"), noop("x"))), always, 2),
		"loop without maximum":    flow.Loop("l", noop("b"), always, 0),
		"loop without condition":  flow.Loop("l", noop("b"), nil, 2),
		"route key with bracket":  flow.Route("r", key, map[string]flow.Step{"a[1]": noop("x")}),
		"route without key func":  flow.Route("r", nil, map[string]flow.Step{"a": noop("x")}),
	}
	for name, step := range bad {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if err := flow.Validate(step); !errors.Is(err, flow.ErrInvalidFlow) {
				t.Errorf("Validate error = %v, want ErrInvalidFlow", err)
			}
			if _, err := run(t, flow.NewMemory(), "r", step); !errors.Is(err, flow.ErrInvalidFlow) {
				t.Errorf("Run error = %v, want ErrInvalidFlow", err)
			}
		})
	}

	good := flow.Sequence("s", flow.Named("renamed", unnamed{}), flow.Parallel("p", noop("a"), noop("a")),
		flow.Route("r", key, map[string]flow.Step{"k": noop("x"), "j": noop("x")}), flow.Loop("l", noop("b"), always, 2))
	if err := flow.Validate(good); err != nil {
		t.Errorf("Validate(good) = %v", err)
	}
}

func TestRunAndResumeRefuseBadRequests(t *testing.T) {
	t.Parallel()
	step := flow.Func("f", func(context.Context, *flow.State) error { return nil })
	ctx := t.Context()

	if _, err := run(t, nil, "r", step); !errors.Is(err, flow.ErrNoCheckpointer) {
		t.Errorf("Run without a checkpointer: error = %v, want ErrNoCheckpointer", err)
	}
	if _, err := resume(t, nil, "r", step); !errors.Is(err, flow.ErrNoCheckpointer) {
		t.Errorf("Resume without a checkpointer: error = %v, want ErrNoCheckpointer", err)
	}

	cp := flow.NewMemory()
	if _, err := resume(t, cp, "missing", step); !errors.Is(err, flow.ErrNotFound) {
		t.Errorf("Resume of an unknown run: error = %v, want ErrNotFound", err)
	}
	if _, err := run(t, cp, "r", step); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, err := run(t, cp, "r", step); !errors.Is(err, flow.ErrRunExists) {
		t.Errorf("second Run with the same id: error = %v, want ErrRunExists", err)
	}

	future := load(t, cp, "r")
	future.Version = flow.CheckpointVersion + 1
	if err := cp.Save(ctx, "future", future); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := resume(t, cp, "future", step); !errors.Is(err, flow.ErrCheckpoint) {
		t.Errorf("Resume of a newer checkpoint: error = %v, want ErrCheckpoint", err)
	}
}

// failingSaves wraps a checkpointer and fails the nth Save.
type failingSaves struct {
	flow.Checkpointer
	failOn, calls int
}

var errDisk = errors.New("disk full")

func (f *failingSaves) Save(ctx context.Context, id string, c flow.Checkpoint) error {
	f.calls++
	if f.calls == f.failOn {
		return errDisk
	}
	return f.Checkpointer.Save(ctx, id, c)
}

func TestCheckpointSaveFailureFailsTheStep(t *testing.T) {
	t.Parallel()
	var c calls
	// Saves: 1 = run start, 2 = after a, 3 = after b.
	cp := &failingSaves{Checkpointer: flow.NewMemory(), failOn: 3}
	f := flow.Sequence("seq", trail(&c, "a"), trail(&c, "b"), trail(&c, "c"))

	_, err := run(t, cp, "r", f)
	if !errors.Is(err, flow.ErrCheckpoint) || !errors.Is(err, errDisk) {
		t.Fatalf("Run error = %v, want ErrCheckpoint wrapping the disk error", err)
	}
	equalStrings(t, "steps run", c.all(), []string{"a", "b"})

	// b's checkpoint was never saved, so a resume runs b again but not a.
	if _, err := resume(t, cp, "r", f); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	equalStrings(t, "all runs", c.all(), []string{"a", "b", "b", "c"})
}

func TestPanicBecomesError(t *testing.T) {
	t.Parallel()
	boom := flow.Func("boom", func(context.Context, *flow.State) error { panic("kaboom") })
	for name, f := range map[string]flow.Step{
		"in a sequence": flow.Sequence("seq", boom),
		"in a branch":   flow.Parallel("par", boom, flow.Func("ok", func(context.Context, *flow.State) error { return nil })),
	} {
		_, err := run(t, flow.NewMemory(), "r", f)
		var pe *flow.PanicError
		if !errors.As(err, &pe) || pe.Value != "kaboom" || len(pe.Stack) == 0 {
			t.Errorf("%s: error = %v, want a PanicError", name, err)
		}
	}
}

func TestCheckpointJSONGolden(t *testing.T) {
	t.Parallel()
	cp := flow.NewMemory()
	var c calls
	f := flow.Sequence("review",
		trail(&c, "draft"),
		flow.Parallel("check", trail(&c, "lint"), flow.Approve("legal")),
		trail(&c, "publish"))
	seed := flow.NewState()
	mustSet(t, seed, "topic", "q3")
	if _, err := run(t, cp, "r", f, flow.WithState(seed)); !errors.Is(err, flow.ErrSuspended) {
		t.Fatalf("Run error = %v, want ErrSuspended", err)
	}
	out, err := json.MarshalIndent(load(t, cp, "r"), "", "  ")
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	testkit.Golden(t, "testdata/checkpoint_suspended.json", append(out, '\n'))
}
