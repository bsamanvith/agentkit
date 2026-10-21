package agentclient_test

import (
	"errors"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
	"github.com/bsamanvith/agentkit/pkg/agentclient"
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
