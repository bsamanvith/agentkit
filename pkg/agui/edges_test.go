package agui_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
	"github.com/bsamanvith/agentkit/pkg/agui"
	"github.com/bsamanvith/agentkit/pkg/clock/clocktest"
)

// TestShutdownEndsStreams: a stream that Shutdown's deadline cuts ends in
// RUN_ERROR shutting_down (retry elsewhere), not internal, and is not
// logged as a failure.
func TestShutdownEndsStreams(t *testing.T) {
	t.Parallel()
	logs := &errorCount{}
	r := newRig(t, agui.WithLogger(slog.New(logs)))
	type result struct {
		status int
		evs    []agui.Event
	}
	done := make(chan result, 1)
	go func() {
		status, evs, _ := r.post("plain", keyA, input("t-drain", user("u", "wait for it")))
		done <- result{status, evs}
	}()
	<-r.model.waiting // the run is under way
	ctx, cancel := context.WithCancel(t.Context())
	cancel() // no time to drain: Shutdown ends the streams at once
	if err := r.srv.Shutdown(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("Shutdown = %v, want context.Canceled", err)
	}
	got := <-done
	if got.evs == nil {
		t.Fatalf("POST = %d, want a stream", got.status)
	}
	if e := last(got.evs); e.Type != agui.EventRunError || e.Code != wire.CodeShuttingDown {
		t.Errorf("the cut stream ended %+v, want RUN_ERROR %s", e, wire.CodeShuttingDown)
	}
	if n := logs.n.Load(); n != 0 {
		t.Errorf("%d errors logged for a shutdown", n)
	}
}

// errorCount is a slog.Handler that counts the records of level Error.
type errorCount struct{ n atomic.Int64 }

func (*errorCount) Enabled(context.Context, slog.Level) bool { return true }

func (h *errorCount) Handle(_ context.Context, rec slog.Record) error {
	if rec.Level >= slog.LevelError {
		h.n.Add(1)
	}
	return nil
}

func (h *errorCount) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *errorCount) WithGroup(string) slog.Handler      { return h }

// TestDotIDs: IDs that are "." or ".." (a threadId, the run of a message
// ID or of an interrupt ID) name nothing. They are not looked up, where the
// API's router would clean them into another path (GET /v1/conversations/.
// is the list of conversations).
func TestDotIDs(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	for _, id := range []string{".", ".."} {
		evs := r.run("plain", keyA, input(id, user("u", "hi")))
		if e := last(evs); e.Type != agui.EventRunFinished || string(e.Result) != `"echo: hi"` {
			t.Errorf("thread %q: %+v, want a new conversation's answer", id, e)
		}
		in := input("t-dots"+id, map[string]any{"id": id + "/t1", "role": "assistant", "content": "x"}, user("u", "hi"))
		if e := last(r.run("plain", keyA, in)); e.Type != agui.EventRunFinished {
			t.Errorf("message ID %q/t1: %+v", id, e)
		}
		res := resume("t-dots-resume"+id, nil, entry(id+"/i/call_1", agui.ResumeResolved, true))
		if e := last(r.run("plain", keyA, res)); e.Type != agui.EventRunError || e.Code != agui.CodeUnknownInterrupt {
			t.Errorf("interrupt ID %q/i/call_1: %+v", id, e)
		}
	}
}

// TestResumeAcrossTenants: another tenant's key, a key without agent:run,
// or a request to another agent answers no interrupt: the run still waits,
// and its call did not run.
func TestResumeAcrossTenants(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	held := r.run("asker", keyA, input("t-held", user("u", "note")))
	_, runID := bound(t, held)
	asked := interrupts(t, held)
	for name, tc := range map[string]struct {
		agent, key, thread, code string
	}{
		"tenant b":            {"asker", keyB, "t-held", agui.CodeUnknownInterrupt},
		"tenant b, a new one": {"asker", keyB, "t-other", agui.CodeUnknownInterrupt},
		"read only":           {"asker", keyRead, "t-held", wire.CodeForbidden},
		"another agent":       {"plain", keyA, "t-other", agui.CodeUnknownInterrupt},
	} {
		for _, status := range []string{agui.ResumeResolved, agui.ResumeCancelled} {
			e := last(r.run(tc.agent, tc.key, resume(tc.thread, nil, entry(asked[0].ID, status, true))))
			if e.Type != agui.EventRunError || e.Code != tc.code {
				t.Errorf("%s, %s: %+v, want RUN_ERROR %s", name, status, e, tc.code)
			}
		}
	}
	if run := r.apiRun(keyA, runID); run.Status != wire.StatusSuspended || r.notes.Load() != 0 {
		t.Errorf("after others' resumes the run is %s and note ran %d times", run.Status, r.notes.Load())
	}
}

// TestKeepAlive: a quiet stream gets an SSE comment on each tick, which a
// client skips, between whole events.
func TestKeepAlive(t *testing.T) {
	t.Parallel()
	clk := clocktest.NewFake(time.Time{})
	r := newRig(t, agui.WithClock(clk), agui.WithKeepAlive(time.Second))
	in := input("t-quiet", user("u", "wait for it"))
	body, _ := json.Marshal(in)
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, r.url(agui.PathPrefix+"plain"), bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+keyA)
	resp, err := r.ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	br := bufio.NewReader(resp.Body)
	<-r.model.waiting // the run is under way, and quiet
	if err := clk.WaitForTimers(t.Context(), 1); err != nil {
		t.Fatal(err)
	}
	clk.Advance(time.Second)
	// The comment comes before or after RUN_STARTED, whole: a line of its
	// own, then the blank line that ends it.
	var seen strings.Builder
	for !strings.HasPrefix(seen.String(), ": keep-alive\n\n") && !strings.Contains(seen.String(), "\n\n: keep-alive\n\n") {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("the stream ended with no keep-alive: %q (%v)", seen.String(), err)
		}
		// Until the model answers, the stream has RUN_STARTED and comments.
		if line != "\n" && line != ": keep-alive\n" && !strings.HasPrefix(line, `data: {"type":"RUN_STARTED"`) {
			t.Fatalf("a quiet stream sent %q", line)
		}
		seen.WriteString(line)
	}
	r.release()
	raw := readSSE(t, strings.NewReader(seen.String()))
	rest, err := io.ReadAll(br)
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw, readSSE(t, bytes.NewReader(rest))...)
	evs := decode(t, raw)
	r.mu.Lock()
	r.streams = append(r.streams, recorded{in: in, events: evs, raw: raw})
	r.mu.Unlock()
	if e := last(evs); e.Type != agui.EventRunFinished || string(e.Result) != `"released"` {
		t.Errorf("the quiet run ended %+v", e)
	}
}

// TestResumeOfTwoRuns: one resume answers the interrupts of one run.
func TestResumeOfTwoRuns(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	one := interrupts(t, r.run("asker", keyA, input("t-one", user("u", "note"))))
	two := interrupts(t, r.run("asker", keyA, input("t-two", user("u", "note"))))
	in := resume("t-two", nil, entry(one[0].ID, agui.ResumeResolved, true), entry(two[0].ID, agui.ResumeResolved, true))
	if e := last(r.run("asker", keyA, in)); e.Type != agui.EventRunError || e.Code != agui.CodeInvalidInput {
		t.Errorf("a resume of two runs: %+v, want RUN_ERROR %s", e, agui.CodeInvalidInput)
	}
	if n := r.notes.Load(); n != 0 {
		t.Errorf("note ran %d times", n)
	}
}
