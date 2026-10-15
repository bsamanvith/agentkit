//go:build integration

package main

// End-to-end test of streaming across replicas (T090): two agentkit serve
// processes on one Postgres store and one Redis bus. The model's answers are
// serve-agent's first run (testdata/e2e/serve-replicas: the tool call, then
// the answer).

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	goredis "github.com/redis/go-redis/v9"

	"github.com/bsamanvith/agentkit/internal/testkit"
	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
	"github.com/bsamanvith/agentkit/pkg/agentclient"
)

// TestE2EServeReplicas: a run held by replica A streams live from replica B
// with A's event IDs; B is killed mid-run and the client goes on from A with
// Last-Event-ID, missing nothing and getting nothing twice. Once the run is
// over and its bus history is gone, either replica still serves every stored
// event and the whole text. A run stopped through B, which does not hold it,
// stops on A.
func TestE2EServeReplicas(t *testing.T) {
	t.Parallel()
	base := testkit.IntegrationEnv(t, "AGENTKIT_PG_URL")
	redisURL := testkit.IntegrationEnv(t, "AGENTKIT_REDIS_URL")
	c, api := newReplayAPI(t, "serve-replicas")
	c.holdAt = 2  // the answer, held while B follows the run and dies
	c.stallAt = 3 // the run that is stopped through B: never answered
	env := []string{
		envComponents + "=agent,mcp-server", envAPIToken + "=" + e2eToken,
		envStore + "=" + testSchema(t, base), envBus + "=" + redisURL,
		"ANTHROPIC_BASE_URL=" + api.URL, "ANTHROPIC_API_KEY=e2e-dummy-key",
	}
	const prompt = "Use the demo-b__echo tool to echo the word pineapple, then tell me exactly what the tool returned."

	a := startServe(t, agentConfigJSON, 5*time.Second, env...)
	b := startServe(t, agentConfigJSON, 5*time.Second, env...)
	ca := agentClient{t: t, url: a.url, token: e2eToken}.client()
	cb := agentClient{t: t, url: b.url, token: e2eToken}.client()

	s, err := ca.Stream(t.Context(), agentclient.RunRequest{Message: prompt})
	if err != nil {
		t.Fatal(err)
	}
	id := s.RunID()
	read := make(chan []agentapi.Event, 1)
	go func() {
		events, err := readStream(s)
		if !errors.Is(err, io.EOF) {
			t.Errorf("A's stream ended with %v", err)
		}
		read <- events
	}()
	select {
	case <-c.held:
	case <-time.After(2 * time.Minute):
		t.Fatal("the run never asked the model for its answer")
	}

	// B follows the run A holds, up to its first turn, and dies.
	fromB, err := cb.Events(t.Context(), id, "")
	if err != nil {
		t.Fatal(err)
	}
	var seen []agentapi.Event
	for len(seen) == 0 || seen[len(seen)-1].Type != agentapi.TypeTurnDone {
		e, err := fromB.Next()
		if err != nil {
			t.Fatalf("B's stream ended with %v after %v", err, eventTypes(seen))
		}
		seen = append(seen, e)
	}
	if err := b.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	<-b.done
	if _, err := fromB.Next(); err == nil || errors.Is(err, io.EOF) {
		t.Errorf("B's stream after the kill: %v, want it cut short", err)
	}
	fromB.Close()

	// The client goes on from A, after the last event B sent.
	rest, err := ca.Events(t.Context(), id, seen[len(seen)-1].ID)
	if err != nil {
		t.Fatal(err)
	}
	close(c.release)
	tail, err := readStream(rest)
	if !errors.Is(err, io.EOF) {
		t.Fatalf("A's stream from %s ended with %v after %v", seen[len(seen)-1].ID, err, eventTypes(tail))
	}
	whole := <-read
	checkAgentStream(t, whole)
	if got := append(seen, tail...); !slices.EqualFunc(got, whole, sameEvent) {
		t.Errorf("B then A from Last-Event-ID streamed %v %v, want the run's %v %v", ids(got), eventTypes(got), ids(whole), eventTypes(whole))
	}
	if end := whole[len(whole)-1]; end.Type != agentapi.TypeRunDone || !strings.Contains(end.Answer, "pineapple") {
		t.Fatalf("the run ended with %+v, want the answer", end)
	}

	// The bus forgets the run: the store still has every stored event, and
	// the turns' text.
	forgetRun(t, redisURL, id)
	b = startServe(t, agentConfigJSON, 5*time.Second, env...)
	cb = agentClient{t: t, url: b.url, token: e2eToken}.client()
	for name, cl := range map[string]*agentclient.Client{"A": ca, "B": cb} {
		es, err := cl.Events(t.Context(), id, "")
		if err != nil {
			t.Fatal(err)
		}
		got, err := readStream(es)
		if !errors.Is(err, io.EOF) {
			t.Fatalf("%s: the stored run's events ended with %v", name, err)
		}
		checkAgentStream(t, got)
		if !slices.EqualFunc(stored(got), stored(whole), sameEvent) || text(got) != text(whole) {
			t.Errorf("%s serves the run without its bus history as %v, want %v with the same text", name, eventTypes(got), eventTypes(whole))
		}
	}

	// A stop sent to B, which does not hold the run, reaches A.
	s, err = ca.Stream(t.Context(), agentclient.RunRequest{Message: "Reply with just the word: ready"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-c.stalled:
	case <-time.After(2 * time.Minute):
		t.Fatal("the second run never asked the model")
	}
	run, err := cb.Stop(t.Context(), s.RunID())
	if err != nil || run.Status != agentapi.StatusInterrupted {
		t.Fatalf("stop through B = %+v, %v; want the run interrupted", run, err)
	}
	events, err := readStream(s)
	if end := events[len(events)-1]; !errors.Is(err, io.EOF) || end.Error == nil || end.Error.Code != agentapi.CodeRunInterrupted {
		t.Errorf("A's stream of the stopped run ended with %+v, %v; want run_error interrupted", end, err)
	}
}

// testSchema creates a schema of the test's own in the database base names,
// dropped when the test ends, and returns base with it as the search path.
func testSchema(t *testing.T, base string) string {
	t.Helper()
	ctx := context.Background()
	schema := "t_" + strings.ToLower(rand.Text())
	conn, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(ctx) })
	if _, err := conn.Exec(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := conn.Exec(ctx, `DROP SCHEMA `+schema+` CASCADE`); err != nil {
			t.Errorf("DROP SCHEMA %s: %v", schema, err)
		}
	})
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	return u.String()
}

// forgetRun deletes the bus's streams of the run, as their trimming or
// expiry would.
func forgetRun(t *testing.T, redisURL, runID string) {
	t.Helper()
	opt, err := goredis.ParseURL(redisURL)
	if err != nil {
		t.Fatal(err)
	}
	rc := goredis.NewClient(opt)
	defer rc.Close()
	keys, err := rc.Keys(t.Context(), "agentkit:bus:*"+runID+"*").Result()
	if err != nil || len(keys) == 0 {
		t.Fatalf("the run's bus keys: %v, %v", keys, err)
	}
	if err := rc.Del(t.Context(), keys...).Err(); err != nil {
		t.Fatal(err)
	}
}

func sameEvent(a, b agentapi.Event) bool { return a.ID == b.ID && a.Type == b.Type }

func ids(events []agentapi.Event) []string {
	out := make([]string, len(events))
	for i, e := range events {
		out[i] = e.ID
	}
	return out
}

// stored returns the stored events of events: those whose ID has no ".n".
func stored(events []agentapi.Event) []agentapi.Event {
	return slices.DeleteFunc(slices.Clone(events), func(e agentapi.Event) bool {
		_, n, _ := wire.ParseID(e.ID)
		return n != 0
	})
}

// text is the text of every turn, in order.
func text(events []agentapi.Event) string {
	var sb strings.Builder
	for _, e := range events {
		if e.Type == agentapi.TypeTextDelta {
			sb.WriteString(e.Text)
		}
	}
	return sb.String()
}
