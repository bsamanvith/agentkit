package a2astep_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/a2a"
	"github.com/bsamanvith/agentkit/pkg/a2a/a2astep"
	"github.com/bsamanvith/agentkit/pkg/a2a/a2atest"
	"github.com/bsamanvith/agentkit/pkg/a2a/client"
	"github.com/bsamanvith/agentkit/pkg/flow"
)

// notifying is a saver that says when a task ID was saved.
type notifying struct {
	*a2astep.MemorySaver
	saved chan string
}

func (n notifying) Save(ctx context.Context, runID, path, id string) error {
	err := n.MemorySaver.Save(ctx, runID, path, id)
	n.saved <- id
	return err
}

func remote(t *testing.T, s *a2atest.Server) *client.Client {
	t.Helper()
	c, err := client.Connect(t.Context(), s.CardURL(), client.Config{
		HTTPClient: s.Client(), Header: http.Header{"Authorization": {"Bearer " + a2atest.Key}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func input(prompt string) *flow.State {
	st := flow.NewState()
	if err := flow.Set(st, "prompt", prompt); err != nil {
		panic(err)
	}
	return st
}

// TestResumeFollowsTheSameTask (criterion 1): a flow whose A2A step is
// interrupted while the remote task works resumes by polling that task, by
// its saved ID, and never sends the prompt again.
func TestResumeFollowsTheSameTask(t *testing.T) {
	t.Parallel()
	s := a2atest.NewServer(t)
	saver := notifying{a2astep.NewMemorySaver(), make(chan string, 4)}
	step, err := a2astep.New("ask", remote(t, s), saver, a2astep.Keys{Input: "prompt", Output: "answer"})
	if err != nil {
		t.Fatal(err)
	}
	flowStep := flow.Sequence("main", step)
	cp := flow.NewMemory()

	ctx, cancel := context.WithCancel(t.Context())
	errc := make(chan error, 1)
	go func() {
		_, err := flow.Run(ctx, "run-1", flowStep, flow.WithCheckpointer(cp), flow.WithState(input("wait for the numbers")))
		errc <- err
	}()
	var id string
	select {
	case id = <-saver.saved:
	case err := <-errc:
		t.Fatalf("the run ended before it saved a task: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("the step saved no task ID while the task worked")
	}
	cancel() // the process stops while the remote task works
	if err := <-errc; err == nil {
		t.Fatal("the interrupted run succeeded")
	}

	s.Release()
	st, err := flow.Resume(t.Context(), "run-1", flowStep, flow.WithCheckpointer(cp))
	if err != nil {
		t.Fatal(err)
	}
	out, err := flow.Get[a2astep.Output](st, "answer")
	if err != nil {
		t.Fatal(err)
	}
	if out.TaskID != id || out.Answer != "released" {
		t.Errorf("output %+v, want task %s's answer", out, id)
	}
	if n := s.ModelCalls(); n != 1 {
		t.Errorf("the remote agent ran %d tasks, want 1", n)
	}
	select {
	case again := <-saver.saved:
		t.Errorf("resuming saved another task, %s", again)
	default:
	}
}

// TestStepRunsATask: a step that is not interrupted writes the answer.
func TestStepRunsATask(t *testing.T) {
	t.Parallel()
	s := a2atest.NewServer(t)
	step, err := a2astep.New("ask", remote(t, s), a2astep.NewMemorySaver(), a2astep.Keys{Input: "prompt", Output: "answer"})
	if err != nil {
		t.Fatal(err)
	}
	st, err := flow.Run(t.Context(), "r", step, flow.WithCheckpointer(flow.NewMemory()), flow.WithState(input("hello")))
	if err != nil {
		t.Fatal(err)
	}
	out, _ := flow.Get[a2astep.Output](st, "answer")
	if out.Answer != "echo: hello" || out.TaskID == "" || out.ContextID == "" {
		t.Errorf("output %+v", out)
	}
	if err := step.Run(t.Context(), flow.NewState()); !errors.Is(err, a2astep.ErrNoRun) {
		t.Errorf("outside a flow: %v, want ErrNoRun", err)
	}
}

// fake is a remote agent scripted per task.
type fake struct {
	mu      sync.Mutex
	tasks   map[string]a2a.Task
	started []string
	ids     []string // the message IDs of the started tasks
	next    a2a.Task // what Start creates
}

func (f *fake) Start(_ context.Context, msg a2a.Message) (a2a.Task, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t := f.next
	f.tasks[t.ID] = t
	f.started = append(f.started, *msg.Parts[0].Text)
	f.ids = append(f.ids, msg.MessageID)
	return t, nil
}

func (f *fake) GetTask(_ context.Context, id string) (a2a.Task, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.tasks[id]
	if !ok {
		return a2a.Task{}, a2a.ErrTaskNotFound
	}
	return t, nil
}

func (f *fake) Wait(ctx context.Context, id string) (a2a.Task, error) { return f.GetTask(ctx, id) }

func done(id, state, text string) a2a.Task {
	return a2a.Task{
		ID: id, Status: a2a.TaskStatus{State: state, Message: &a2a.Message{Role: a2a.RoleAgent, Parts: []a2a.Part{a2a.TextPart(text)}}},
		Artifacts: []a2a.Artifact{{ArtifactID: "answer", Parts: []a2a.Part{a2a.TextPart(text)}}},
	}
}

// TestSavedTaskStates: what a step does with the task it saved, by the
// task's state.
func TestSavedTaskStates(t *testing.T) {
	t.Parallel()
	keys := a2astep.Keys{Input: "prompt", Output: "answer"}
	run := func(f *fake, saved string) (*flow.State, error) {
		saver := a2astep.NewMemorySaver()
		cp := flow.NewMemory()
		step, err := a2astep.New("ask", f, saver, keys)
		if err != nil {
			t.Fatal(err)
		}
		if saved != "" {
			if err := saver.Save(t.Context(), "r", "ask", saved); err != nil {
				t.Fatal(err)
			}
		}
		return flow.Run(t.Context(), "r", step, flow.WithCheckpointer(cp), flow.WithState(input("go")))
	}

	t.Run("completed: its answer, no new task", func(t *testing.T) {
		t.Parallel()
		f := &fake{tasks: map[string]a2a.Task{"old": done("old", a2a.StateCompleted, "kept")}, next: done("new", a2a.StateCompleted, "new")}
		st, err := run(f, "old")
		if err != nil {
			t.Fatal(err)
		}
		out, _ := flow.Get[a2astep.Output](st, "answer")
		if out.TaskID != "old" || out.Answer != "kept" || len(f.started) != 0 {
			t.Errorf("output %+v, started %v: want the saved task's answer", out, f.started)
		}
	})
	t.Run("failed: a retry starts a new task", func(t *testing.T) {
		t.Parallel()
		f := &fake{tasks: map[string]a2a.Task{"old": done("old", a2a.StateFailed, "boom")}, next: done("new", a2a.StateCompleted, "fine")}
		st, err := run(f, "old")
		if err != nil {
			t.Fatal(err)
		}
		out, _ := flow.Get[a2astep.Output](st, "answer")
		if out.TaskID != "new" || len(f.started) != 1 {
			t.Errorf("output %+v, started %v: want one new task", out, f.started)
		}
	})
	t.Run("unknown: an error, no new task", func(t *testing.T) {
		t.Parallel()
		f := &fake{tasks: map[string]a2a.Task{}, next: done("new", a2a.StateCompleted, "fine")}
		if _, err := run(f, "gone"); !errors.Is(err, a2a.ErrTaskNotFound) || len(f.started) != 0 {
			t.Errorf("%v, started %v: want TaskNotFound and no new task", err, f.started)
		}
	})
	t.Run("a new task that fails", func(t *testing.T) {
		t.Parallel()
		f := &fake{tasks: map[string]a2a.Task{}, next: done("new", a2a.StateRejected, "no")}
		var te *a2astep.TaskError
		if _, err := run(f, ""); !errors.As(err, &te) || te.TaskID != "new" || te.State != a2a.StateRejected || te.Message != "no" {
			t.Errorf("%v, want a TaskError", err)
		}
	})
	t.Run("input required", func(t *testing.T) {
		t.Parallel()
		f := &fake{tasks: map[string]a2a.Task{}, next: done("new", a2a.StateInputRequired, "which month?")}
		if _, err := run(f, ""); !errors.Is(err, a2astep.ErrNeedsInput) {
			t.Errorf("%v, want ErrNeedsInput", err)
		}
	})
}

// crashing is a saver whose first Save fails, as if the process stopped
// between the agent creating the task and the step saving its ID.
type crashing struct {
	*a2astep.MemorySaver
	mu      sync.Mutex
	crashed bool
}

func (c *crashing) Save(ctx context.Context, runID, path, id string) error {
	c.mu.Lock()
	first := !c.crashed
	c.crashed = true
	c.mu.Unlock()
	if first {
		return errors.New("crash")
	}
	return c.MemorySaver.Save(ctx, runID, path, id)
}

// TestCrashBeforeSaveResendsTheSameMessage: a step run again after a crash
// before its save sends the prompt with the same message ID, so the agent
// can tell it is the same message; a retry after a failed task gets a new
// one; and the output is labelled third-party data.
func TestCrashBeforeSaveResendsTheSameMessage(t *testing.T) {
	t.Parallel()
	f := &fake{tasks: map[string]a2a.Task{}, next: done("t1", a2a.StateFailed, "boom")}
	saver := &crashing{MemorySaver: a2astep.NewMemorySaver()}
	step, err := a2astep.New("ask", f, saver, a2astep.Keys{Input: "prompt", Output: "answer"})
	if err != nil {
		t.Fatal(err)
	}
	run := func() (*flow.State, error) {
		return flow.Run(t.Context(), "r", step, flow.WithCheckpointer(flow.NewMemory()), flow.WithState(input("go")))
	}
	if _, err := run(); err == nil {
		t.Fatal("the crash: no error")
	}
	var te *a2astep.TaskError
	if _, err := run(); !errors.As(err, &te) {
		t.Fatalf("run again: %v, want the TaskError", err)
	}
	if !strings.Contains(te.Error(), "third-party") {
		t.Errorf("TaskError %q: want the agent's message labelled third-party", te.Error())
	}
	f.mu.Lock()
	f.next = done("t2", a2a.StateCompleted, "fine")
	f.mu.Unlock()
	st, err := run() // a retry of the failed t1
	if err != nil {
		t.Fatal(err)
	}
	if len(f.ids) != 3 || f.ids[0] == "" || f.ids[0] != f.ids[1] || f.ids[2] == f.ids[1] {
		t.Errorf("message IDs %q: want the first two equal (the crash), the retry's new", f.ids)
	}
	out, _ := flow.Get[a2astep.Output](st, "answer")
	if out.TaskID != "t2" || out.Notice != a2astep.Notice || !strings.Contains(out.Notice, "Third-party") {
		t.Errorf("output %+v: want t2 labelled third-party", out)
	}
}

func TestNewChecksItsConfig(t *testing.T) {
	t.Parallel()
	f := &fake{}
	saver := a2astep.NewMemorySaver()
	for name, try := range map[string]func() error{
		"no name": func() error { _, err := a2astep.New("", f, saver, a2astep.Keys{Input: "a", Output: "b"}); return err },
		"no agent": func() error {
			_, err := a2astep.New("s", (*client.Client)(nil), saver, a2astep.Keys{Input: "a", Output: "b"})
			return err
		},
		"no saver":  func() error { _, err := a2astep.New("s", f, nil, a2astep.Keys{Input: "a", Output: "b"}); return err },
		"no keys":   func() error { _, err := a2astep.New("s", f, saver, a2astep.Keys{}); return err },
		"same keys": func() error { _, err := a2astep.New("s", f, saver, a2astep.Keys{Input: "a", Output: "a"}); return err },
	} {
		if err := try(); !errors.Is(err, a2astep.ErrInvalidConfig) {
			t.Errorf("%s: %v", name, err)
		}
	}
}
