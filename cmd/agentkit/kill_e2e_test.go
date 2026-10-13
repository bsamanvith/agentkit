package main

// End-to-end test of a run that outlives its process: agentkit serve on a
// SQLite store, killed while a chat run waits for the model, then started
// again. The model's answers are serve-agent's first two recordings, for the
// same prompt and config (testdata/e2e/serve-kill: the tool call, again for
// the resumed run, then the answer).

import (
	"bytes"
	"errors"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/internal/testkit"
	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/agentclient"
)

// readStream reads a stream until it ends, returning its events and the
// error that ended it (io.EOF after the run's last event).
func readStream(s *agentclient.Stream) ([]agentapi.Event, error) {
	defer s.Close()
	var events []agentapi.Event
	for {
		e, err := s.Next()
		if err != nil {
			return events, err
		}
		events = append(events, e)
	}
}

// TestE2EServeKilledMidRun: the process dies (SIGKILL) while a chat run waits
// for the model's second turn. Started again on the same store, the server
// shows the run interrupted once its lease runs out, and does not resume it
// itself; POST /v1/runs/{id}/resume finishes it, and GET /v1/runs/{id}/events
// then shows the whole run. The killed process had not stored the run's
// first turn (its tool reads, so the runner batches it): the resumed run
// does it again, as the killed stream showed it.
func TestE2EServeKilledMidRun(t *testing.T) {
	t.Parallel()
	c, api := newReplayAPI(t, "serve-kill")
	c.stallAt = 2 // the model's second turn: after the tool call and its result
	env := []string{
		envComponents + "=agent,mcp-server", envAPIToken + "=" + e2eToken,
		envStore + "=sqlite:" + filepath.Join(t.TempDir(), "agentkit.db"),
		envRunLease + "=2s",
		"ANTHROPIC_BASE_URL=" + api.URL, "ANTHROPIC_API_KEY=e2e-dummy-key",
	}
	const prompt = "Use the demo-b__echo tool to echo the word pineapple, then tell me exactly what the tool returned."

	first := startServe(t, agentConfigJSON, 5*time.Second, env...)
	s, err := agentClient{t: t, url: first.url, token: e2eToken}.client().Stream(t.Context(), agentclient.RunRequest{Message: prompt})
	if err != nil {
		t.Fatal(err)
	}
	id := s.RunID()
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
	// The model got the second request, and the client the first turn.
	for _, ch := range []<-chan struct{}{c.stalled, turned} {
		select {
		case <-ch:
		case <-time.After(2 * time.Minute):
			t.Fatal("the run never reached the model's second turn")
		}
	}
	if err := first.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	<-first.done
	before := <-read
	if calls, _ := toolEvents(before); len(calls) != 1 || !slices.Contains(eventTypes(before), agentapi.TypeTurnDone) {
		t.Fatalf("before the kill the stream had %v, want the tool call's turn done", eventTypes(before))
	}

	second := startServe(t, agentConfigJSON, 5*time.Second, env...)
	client := agentClient{t: t, url: second.url, token: e2eToken}.client()
	var run agentapi.Run
	testkit.Eventually(t, time.Minute, func() bool {
		if run, err = client.GetRun(t.Context(), id); err != nil {
			t.Fatal(err)
		}
		return run.Status != agentapi.StatusRunning
	})
	if run.Status != agentapi.StatusInterrupted || run.Error == nil || run.Error.Code != agentapi.CodeRunInterrupted {
		t.Fatalf("after the restart the run is %+v, want interrupted", run)
	}
	if n := len(c.all()); n != 2 {
		t.Fatalf("%d model calls before the resume, want 2: nothing resumes a chat run but its user", n)
	}

	rs, err := client.Resume(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := readStream(rs)
	if !errors.Is(err, io.EOF) {
		t.Fatalf("resumed stream ended with %v after %v", err, eventTypes(resumed))
	}
	if end := resumed[len(resumed)-1]; end.Type != agentapi.TypeRunDone || !strings.Contains(end.Answer, "pineapple") {
		t.Errorf("resumed run ended with %+v, want the answer", end)
	}

	es, err := client.Events(t.Context(), id, -1)
	if err != nil {
		t.Fatal(err)
	}
	whole, err := readStream(es)
	if !errors.Is(err, io.EOF) {
		t.Fatalf("events ended with %v after %v", err, eventTypes(whole))
	}
	checkAgentStream(t, whole)
	if got, want := eventTypes(whole[:len(before)]), eventTypes(before); !slices.Equal(got, want) {
		t.Errorf("the run's events start %v, want the killed stream's %v", got, want)
	}
	if got, want := eventTypes(whole[len(whole)-len(resumed):]), eventTypes(resumed); !slices.Equal(got, want) {
		t.Errorf("the run's events end %v, want the resumed stream's %v", got, want)
	}
	if run, err := client.GetRun(t.Context(), id); err != nil || run.Status != agentapi.StatusDone || run.Turns != 2 {
		t.Errorf("run = %+v, %v; want done after two turns", run, err)
	}
	reqs := c.all()
	if len(reqs) != 4 || !bytes.Equal(reqs[2].body, reqs[0].body) {
		t.Errorf("%d model calls, want 4, the resumed run's first the same as the killed one's", len(reqs))
	}
	checkConversation(t, slices.Delete(reqs, 1, 2)) // the stalled request was never answered
}
