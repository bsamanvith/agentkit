//go:build integration

package main

// End-to-end test of steering across replicas (T090, after T086): two
// agentkit serve processes on one Postgres store and one Redis bus. The
// model's answers are serve-replicas' two recordings, once per task
// (testdata/e2e/serve-steer: the tool call, the answer, the tool call, the
// answer); the replay serves them in order whatever the requests say, so
// what the tests check is what the binaries sent.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/internal/testkit"
	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
)

// TestE2ESteeringAcrossReplicas: a steering message sent to replica B
// reaches the task replica A runs at its next turn boundary, after the tool
// call's result and before the next model call. A second task, steered
// through B while A waits for the model, is then left by A (killed): B's
// recovery takes it over once A's lease runs out and delivers the message
// first. B's notification stream (GET /v1/events) has both tasks' statuses
// meanwhile, A's and its own, through Redis.
func TestE2ESteeringAcrossReplicas(t *testing.T) {
	t.Parallel()
	base := testkit.IntegrationEnv(t, "AGENTKIT_PG_URL")
	redisURL := testkit.IntegrationEnv(t, "AGENTKIT_REDIS_URL")
	c, api := newReplayAPI(t, "serve-steer")
	c.holdAt = 1  // the first task's first turn, held while B steers it
	c.stallAt = 3 // the second task's first request: never answered, A dies in it
	env := []string{
		envComponents + "=agent,mcp-server", envAPIToken + "=" + e2eToken,
		envStore + "=" + testSchema(t, base), envBus + "=" + redisURL,
		envRunLease + "=2s", envRecoveryInterval + "=200ms",
		"ANTHROPIC_BASE_URL=" + api.URL, "ANTHROPIC_API_KEY=e2e-dummy-key",
	}
	const prompt = "Use the demo-b__echo tool to echo the word pineapple, then tell me exactly what the tool returned."
	const steer1, steer2 = "Also say the word mango.", "Keep it to one sentence."

	a := startServe(t, agentConfigJSON, 5*time.Second, env...)
	b := startServe(t, agentConfigJSON, 5*time.Second, env...)
	ca := agentClient{t: t, url: a.url, token: e2eToken}.client()
	cb := agentClient{t: t, url: b.url, token: e2eToken}.client()
	notes := userEvents(t, b.url)

	// The first task runs on A; B, which does not hold it, takes the
	// message while the model works on the first turn.
	task, err := ca.StartTask(t.Context(), wire.TaskRequest{Input: prompt})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-c.held:
	case <-time.After(2 * time.Minute):
		t.Fatal("the task never asked the model")
	}
	if m, err := cb.SteerTask(t.Context(), task.ID, steer1); err != nil || m.RunID != task.ID {
		t.Fatalf("steering through B = %+v, %v", m, err)
	}
	close(c.release)
	es, err := cb.Events(t.Context(), task.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	events, err := readStream(es)
	if !errors.Is(err, io.EOF) {
		t.Fatalf("the steered task's events ended with %v after %v", err, eventTypes(events))
	}
	checkAgentStream(t, events)
	checkSteered(t, events, steer1)
	reqs := c.all()
	if len(reqs) != 2 || strings.Contains(string(reqs[0].body), steer1) || !strings.Contains(string(reqs[1].body), steer1) {
		t.Fatalf("model requests %d; want 2, the message in the second only", len(reqs))
	}
	checkConversation(t, reqs)

	// The second task: steered through B while A waits for the model, then
	// A dies. B takes the task over and delivers the message first.
	task2, err := ca.StartTask(t.Context(), wire.TaskRequest{Input: prompt})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-c.stalled:
	case <-time.After(2 * time.Minute):
		t.Fatal("the second task never asked the model")
	}
	if _, err := cb.SteerTask(t.Context(), task2.ID, steer2); err != nil {
		t.Fatal(err)
	}
	if err := a.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	<-a.done
	es, err = cb.Events(t.Context(), task2.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	events, err = readStream(es) // waits for B's recovery to take the task up
	if !errors.Is(err, io.EOF) {
		t.Fatalf("the taken-over task's events ended with %v after %v", err, eventTypes(events))
	}
	checkAgentStream(t, events)
	checkSteered(t, events, steer2)
	if end := events[len(events)-1]; end.Type != agentapi.TypeRunDone || !strings.Contains(end.Answer, "pineapple") {
		t.Errorf("the taken-over task ended with %+v, want the answer", end)
	}
	reqs = c.all()
	if len(reqs) != 5 {
		t.Fatalf("%d model calls, want 5: the first task's 2, the stalled one, B's 2", len(reqs))
	}
	if strings.Contains(string(reqs[2].body), steer2) || !strings.Contains(string(reqs[3].body), steer2) {
		t.Errorf("the stalled request has the message, or B's first does not: B did not deliver it first")
	}
	checkConversation(t, reqs[3:])
	run, err := cb.GetRun(t.Context(), task2.ID)
	if err != nil || run.Status != agentapi.StatusDone {
		t.Errorf("the second task = %+v, %v; want done", run, err)
	}

	// B's notifications: the first task running and done on A; the second
	// running on A, then again on B, and done. Other tests share this
	// Postgres and Redis as the same caller, so their runs are on the stream
	// too: only this test's tasks count.
	type status struct{ run, status string }
	want := []status{
		{task.ID, agentapi.StatusRunning},
		{task.ID, agentapi.StatusDone},
		{task2.ID, agentapi.StatusRunning},
		{task2.ID, agentapi.StatusRunning},
		{task2.ID, agentapi.StatusDone},
	}
	var got []status
	for len(got) < len(want) {
		select {
		case n, ok := <-notes:
			if !ok {
				t.Fatalf("B's notification stream ended after %v", got)
			}
			if n.Type == agentapi.TypeRunStatus && (n.RunID == task.ID || n.RunID == task2.ID) {
				got = append(got, status{n.RunID, n.Status})
			}
		case <-time.After(time.Minute):
			t.Fatalf("B's notifications: %v, want %v", got, want)
		}
	}
	if !slices.Equal(got, want) {
		t.Errorf("B's notifications: %v, want %v", got, want)
	}
}

// checkSteered checks that events have one steered event, with text, and
// that it comes at a turn boundary: after a turn_done (or first of the
// run's turns) and before the next turn's events.
func checkSteered(t *testing.T, events []agentapi.Event, text string) {
	t.Helper()
	i := slices.IndexFunc(events, func(e agentapi.Event) bool { return e.Type == agentapi.TypeSteered })
	if i < 0 || !slices.Equal(events[i].Steering, []string{text}) {
		t.Fatalf("events %v: want one steered event with %q", eventTypes(events), text)
	}
	if n := len(slices.DeleteFunc(slices.Clone(events), func(e agentapi.Event) bool { return e.Type != agentapi.TypeSteered })); n != 1 {
		t.Errorf("%d steered events, want 1: %v", n, eventTypes(events))
	}
	switch prev := events[i-1].Type; prev {
	case agentapi.TypeTurnDone, agentapi.TypeRunStart, agentapi.TypeRewind:
	default:
		t.Errorf("steered after %s, want it at a turn boundary: %v", prev, eventTypes(events))
	}
	if i+1 >= len(events) || events[i+1].Type == agentapi.TypeToolResult {
		t.Errorf("events after steered: %v", eventTypes(events[i:]))
	}
}

// userEvents follows GET /v1/events on the server at url until the test
// ends.
func userEvents(t *testing.T, url string) <-chan wire.UserEvent {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url+"/v1/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+e2eToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		cancel()
		t.Fatalf("GET /v1/events = %v, %v", resp, err)
	}
	ch, done := make(chan wire.UserEvent, 100), make(chan struct{})
	go func() {
		defer close(done)
		defer close(ch)
		defer resp.Body.Close()
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			if data, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
				var n wire.UserEvent
				if err := json.Unmarshal([]byte(data), &n); err != nil {
					t.Errorf("a notification %q: %v", data, err)
				}
				ch <- n
			}
		}
	}()
	t.Cleanup(func() { cancel(); <-done })
	return ch
}
