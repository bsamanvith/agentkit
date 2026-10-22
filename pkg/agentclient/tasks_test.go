package agentclient_test

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/internal/testkit"
	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
	"github.com/bsamanvith/agentkit/pkg/agentclient"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/llm/llmtest"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

// A task through the client: started, followed from the middle with the last
// event's ID, listed as a background run, its webhook shown; past the
// tenant's limit a task is ErrTooManyBackgroundRuns with Retry-After; the
// secret is the tenant's.
func TestTasks(t *testing.T) {
	t.Parallel()
	m := &model{replies: []llmtest.Reply{llmtest.Text("first")}, gate: make(chan struct{})}
	r := newRig(t, m, agentapi.WithMaxBackgroundRuns(1), agentapi.WithWebhooks(agentapi.Webhooks{
		Secret: func(tn tenancy.Tenant) (string, error) { return "whsec_" + tn.ID(), nil },
	}))
	ctx := t.Context()
	run, err := r.client.StartTask(ctx, wire.TaskRequest{Input: "go", Notify: &wire.Notify{URL: "https://hooks.example/x"}})
	if err != nil || !run.Background || run.Status != wire.StatusRunning {
		t.Fatalf("StartTask = %+v, %v; want a running background run", run, err)
	}
	_, err = r.client.StartTask(ctx, wire.TaskRequest{Input: "one too many"})
	var apiErr *agentclient.APIError
	if !errors.Is(err, agentclient.ErrTooManyBackgroundRuns) || !errors.As(err, &apiErr) || apiErr.RetryAfter == 0 {
		t.Errorf("a task past the limit: %v, want ErrTooManyBackgroundRuns with Retry-After", err)
	}

	s, err := r.client.Events(ctx, run.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	start, err := s.Next()
	if err != nil || start.Type != wire.TypeRunStart {
		t.Fatalf("first event %+v, %v", start, err)
	}
	_ = s.Close()
	rest, err := r.client.Events(ctx, run.ID, start.ID)
	if err != nil {
		t.Fatal(err)
	}
	m.gate <- struct{}{}
	var last wire.Event
	for {
		e, err := rest.Next()
		if err != nil {
			t.Fatalf("following from %s: %v", start.ID, err)
		}
		if e.Type == wire.TypeRunStart || wire.CompareIDs(e.ID, start.ID) <= 0 {
			t.Errorf("event %s resumed after %s", e.ID, start.ID)
		}
		if e.Type == wire.TypeRunDone || e.Type == wire.TypeRunError {
			last = e
			break
		}
	}
	_ = rest.Close()
	if last.Type != wire.TypeRunDone || last.Answer != "first" {
		t.Errorf("the task ended with %+v, want run_done", last)
	}

	yes := true
	list, err := r.client.Runs(ctx, agentclient.RunsQuery{Background: &yes, Agent: agentapi.DefaultAgent})
	if err != nil || len(list.Runs) != 1 || list.Runs[0].ID != run.ID {
		t.Errorf("background runs %+v, %v; want the task", list.Runs, err)
	}
	got := r.waitEnded(run.ID)
	if got.Notify == nil || got.Notify.URL != "https://hooks.example/x" || got.Notify.State != wire.NotificationPending {
		t.Errorf("the task's webhook %+v, want it pending", got.Notify)
	}
	if secret, err := r.client.WebhookSecret(ctx); err != nil || secret != "whsec_"+agentapi.DefaultTenant {
		t.Errorf("WebhookSecret = %q, %v", secret, err)
	}
}

// Steering a task through the client (T086): a message and a pause sent
// while its model call waits reach it at the boundary after that turn; it
// pauses (PauseTask again says so), ResumeTask continues it, the model reads
// the message after the tool result, and once done a message or a resume is
// ErrRunState; no task is ErrNotFound.
func TestSteerTask(t *testing.T) {
	t.Parallel()
	m := &model{replies: []llmtest.Reply{
		llmtest.ToolCalls(call("toolu_1", "echo", `{"text":"x"}`)), llmtest.Text("Done, without the PDF."),
	}, gate: make(chan struct{})}
	r := newRig(t, m)
	ctx := t.Context()
	task, err := r.client.StartTask(ctx, wire.TaskRequest{Input: "report"})
	if err != nil {
		t.Fatal(err)
	}
	testkit.Eventually(t, 30*time.Second, func() bool { return len(m.requests()) == 1 })
	msg, err := r.client.SteerTask(ctx, task.ID, "skip the PDF")
	if err != nil || msg.RunID != task.ID || msg.Seq != 1 || msg.Text != "skip the PDF" {
		t.Fatalf("SteerTask = %+v, %v", msg, err)
	}
	if run, err := r.client.PauseTask(ctx, task.ID); err != nil || run.Status != wire.StatusRunning {
		t.Fatalf("PauseTask = %s, %v; want running until the boundary", run.Status, err)
	}
	m.gate <- struct{}{}
	if run := r.waitEnded(task.ID); run.Status != wire.StatusPaused {
		t.Fatalf("task %s, want paused", run.Status)
	}
	if run, err := r.client.PauseTask(ctx, task.ID); err != nil || run.Status != wire.StatusPaused {
		t.Errorf("PauseTask on a paused task = %s, %v", run.Status, err)
	}
	if run, err := r.client.ResumeTask(ctx, task.ID); err != nil || run.Status != wire.StatusRunning {
		t.Fatalf("ResumeTask = %s, %v", run.Status, err)
	}
	m.gate <- struct{}{}
	if run := r.waitEnded(task.ID); run.Status != wire.StatusDone || run.Answer != "Done, without the PDF." {
		t.Fatalf("task %s %q, want done", run.Status, run.Answer)
	}
	reqs := m.requests()
	last := reqs[1].Messages[len(reqs[1].Messages)-1]
	isResult := func(b llm.Block) bool { _, ok := b.(llm.ToolResult); return ok }
	if txt, ok := last.Content[len(last.Content)-1].(llm.Text); !ok || txt.Text != "skip the PDF" || !slices.ContainsFunc(last.Content, isResult) {
		t.Errorf("the model read %+v last, want the tool result then the message", last)
	}
	if _, err := r.client.SteerTask(ctx, task.ID, "too late"); !errors.Is(err, agentclient.ErrRunState) {
		t.Errorf("SteerTask after the end: %v, want ErrRunState", err)
	}
	if _, err := r.client.ResumeTask(ctx, task.ID); !errors.Is(err, agentclient.ErrRunState) {
		t.Errorf("ResumeTask after the end: %v, want ErrRunState", err)
	}
	if _, err := r.client.SteerTask(ctx, "nope", "hi"); !errors.Is(err, agentclient.ErrNotFound) {
		t.Errorf("SteerTask on no task: %v, want ErrNotFound", err)
	}
}
