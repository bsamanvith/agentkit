package main

// End-to-end test of a task (T071) that outlives its process: agentkit serve
// on a SQLite store runs a background run, is killed while the run waits for
// the model, and is started again; the recovery loop resumes the run, a
// watcher that comes back with Last-Event-ID goes on with it, and its end is
// posted, signed, to a webhook. The model's answers are serve-kill's
// recordings (the same prompt and config as TestE2EServeKilledMidRun).

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/internal/testkit"
	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
	"github.com/bsamanvith/agentkit/pkg/agentclient"
	"github.com/bsamanvith/agentkit/pkg/webhook"
)

// delivery is a webhook post the receiver verified.
type delivery struct {
	id, attempt string
	event       wire.WebhookEvent
}

// receiver is a webhook endpoint that verifies every post's signature with
// the tenant's secret and keeps the ones that verify.
type receiver struct {
	t      *testing.T
	secret []byte

	mu   sync.Mutex
	got  []delivery
	errs []error
}

func (rc *receiver) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err == nil {
		err = webhook.Verify(rc.secret, r.Header.Get(webhook.HeaderSignature), body, time.Now(), 0)
	}
	rc.mu.Lock()
	defer rc.mu.Unlock()
	if err != nil {
		rc.errs = append(rc.errs, err)
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	d := delivery{id: r.Header.Get(webhook.HeaderID), attempt: r.Header.Get(webhook.HeaderAttempt)}
	if err := json.Unmarshal(body, &d.event); err != nil {
		rc.errs = append(rc.errs, err)
	}
	rc.got = append(rc.got, d)
}

func (rc *receiver) deliveries() ([]delivery, []error) {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	return slices.Clone(rc.got), slices.Clone(rc.errs)
}

// TestE2ETaskSurvivesARestart: POST /v1/tasks starts a run that the process
// (SIGKILL) dies in, while it waits for the model's second turn. Started
// again on the same store, the server resumes the task by itself (the
// recovery loop), with no client asking; a watcher that reconnects with the
// last event it had goes on from there, the two streams together being the
// run's events with nothing missing or repeated; and the receiver gets the
// end once, signed with the tenant's secret.
func TestE2ETaskSurvivesARestart(t *testing.T) {
	t.Parallel()
	c, api := newReplayAPI(t, "serve-kill")
	c.stallAt = 2 // the model's second turn: after the tool call and its result
	master := strings.Repeat("e2e-webhook-master-secret-", 2)
	rc := &receiver{t: t, secret: webhook.DeriveSecret([]byte(master), agentapi.DefaultTenant)}
	hooks := httptest.NewServer(rc)
	t.Cleanup(hooks.Close)
	env := []string{
		envComponents + "=agent,mcp-server", envAPIToken + "=" + e2eToken,
		envStore + "=sqlite:" + filepath.Join(t.TempDir(), "agentkit.db"),
		envRunLease + "=2s", envRecoveryInterval + "=200ms",
		envWebhookSecret + "=" + master, envWebhookAllowHosts + "=127.0.0.1",
		"ANTHROPIC_BASE_URL=" + api.URL, "ANTHROPIC_API_KEY=e2e-dummy-key",
	}
	const prompt = "Use the demo-b__echo tool to echo the word pineapple, then tell me exactly what the tool returned."

	first := startServe(t, agentConfigJSON, 5*time.Second, env...)
	client := agentClient{t: t, url: first.url, token: e2eToken}.client()
	task, err := client.StartTask(t.Context(), wire.TaskRequest{Input: prompt, Notify: &wire.Notify{URL: hooks.URL + "/done"}})
	if err != nil {
		t.Fatal(err)
	}
	s, err := client.Events(t.Context(), task.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	read, turned := make(chan []agentapi.Event, 1), make(chan struct{})
	turn := sync.OnceFunc(func() { close(turned) })
	go func() {
		defer s.Close()
		var events []agentapi.Event
		for {
			e, err := s.Next()
			if err != nil {
				if !errors.Is(err, agentclient.ErrStreamTruncated) {
					t.Errorf("the killed process's stream ended with %v, want it cut short", err)
				}
				read <- events
				return
			}
			if events = append(events, e); e.Type == agentapi.TypeTurnDone {
				turn()
			}
		}
	}()
	for _, ch := range []<-chan struct{}{c.stalled, turned} {
		select {
		case <-ch:
		case <-time.After(2 * time.Minute):
			t.Fatal("the task never reached the model's second turn")
		}
	}
	if err := first.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	<-first.done
	before := <-read
	if !slices.Contains(eventTypes(before), agentapi.TypeTurnDone) {
		t.Fatalf("before the kill the stream had %v, want the tool call's turn done", eventTypes(before))
	}

	second := startServe(t, agentConfigJSON, 5*time.Second, env...)
	client = agentClient{t: t, url: second.url, token: e2eToken}.client()
	last := before[len(before)-1].ID
	rs, err := client.Events(t.Context(), task.ID, last)
	if err != nil {
		t.Fatal(err)
	}
	rest, err := readStream(rs) // waits for the recovery loop to take the task up
	if !errors.Is(err, io.EOF) {
		t.Fatalf("the reconnected stream ended with %v after %v", err, eventTypes(rest))
	}
	if end := rest[len(rest)-1]; end.Type != agentapi.TypeRunDone || !strings.Contains(end.Answer, "pineapple") {
		t.Fatalf("the task ended with %+v, want the answer", end)
	}

	es, err := client.Events(t.Context(), task.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	whole, err := readStream(es)
	if !errors.Is(err, io.EOF) {
		t.Fatalf("events ended with %v after %v", err, eventTypes(whole))
	}
	checkAgentStream(t, whole)
	// The stored events, from the two streams, are the run's, each once: a
	// text_delta has another ID once replayed.
	stored := func(evs []agentapi.Event) (ids []string) {
		for _, e := range evs {
			if e.Type != agentapi.TypeTextDelta {
				ids = append(ids, e.Type+"@"+e.ID)
			}
		}
		return ids
	}
	if got, want := stored(append(slices.Clone(before), rest...)), stored(whole); !slices.Equal(got, want) {
		t.Errorf("the watcher saw %v across the restart, want the run's %v", got, want)
	}
	if !slices.Contains(eventTypes(rest), agentapi.TypeRewind) {
		t.Errorf("after the restart the watcher saw %v, want the rewind of the turn the killed process did not keep", eventTypes(rest))
	}

	// The webhook: once, signed, the run as it ended.
	testkit.Eventually(t, time.Minute, func() bool {
		got, _ := rc.deliveries()
		return len(got) > 0
	})
	var run agentapi.Run
	testkit.Eventually(t, time.Minute, func() bool {
		if run, err = client.GetRun(t.Context(), task.ID); err != nil {
			t.Fatal(err)
		}
		return run.Notify != nil && run.Notify.State == wire.NotificationDelivered
	})
	got, errs := rc.deliveries()
	if len(errs) > 0 {
		t.Errorf("the receiver refused posts: %v", errs)
	}
	if len(got) != 1 || got[0].id != task.ID || got[0].attempt != "1" || got[0].event.Type != wire.TypeRunEnded ||
		got[0].event.Run.ID != task.ID || got[0].event.Run.Status != agentapi.StatusDone || !strings.Contains(got[0].event.Run.Answer, "pineapple") {
		t.Errorf("the receiver got %+v, want the task's end once", got)
	}
	if len(run.Notify.Attempts) != 1 || run.Notify.Attempts[0].Status != http.StatusOK {
		t.Errorf("the task's webhook = %+v, want one attempt, answered 200", run.Notify)
	}
	if run.Status != agentapi.StatusDone || !run.Background {
		t.Errorf("task = %+v, want done, background", run)
	}
	reqs := c.all()
	if len(reqs) != 4 {
		t.Errorf("%d model calls, want 4: the stalled one, and the resumed run's", len(reqs))
	}
	checkConversation(t, slices.Delete(reqs, 1, 2)) // the stalled request was never answered
}
