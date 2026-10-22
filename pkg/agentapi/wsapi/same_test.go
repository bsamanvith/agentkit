package wsapi_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/bsamanvith/agentkit/internal/testkit"
	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
	"github.com/bsamanvith/agentkit/pkg/agentclient"
	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/llm/llmtest"
	"github.com/bsamanvith/agentkit/pkg/policy"
)

// transcript records what a client saw, comparable across rigs: events
// and resources without their times and durations, errors by status and
// code.
type transcript struct {
	t     *testing.T
	lines []string
}

func (tr *transcript) add(format string, args ...any) {
	tr.lines = append(tr.lines, fmt.Sprintf(format, args...))
}

// value records a resource or event.
func (tr *transcript) value(what string, v any) {
	tr.t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		tr.t.Fatal(err)
	}
	tr.add("%s %s", what, normalized(tr.t, b))
}

// err records an error: an API error by status and code, a stream's end by
// its sentinel.
func (tr *transcript) err(what string, err error) {
	var ae *agentclient.APIError
	switch {
	case err == nil:
		tr.add("%s ok", what)
	case errors.As(err, &ae):
		tr.add("%s error %d %s retryable=%v retry-after=%s run=%s", what, ae.Status, ae.Code, ae.Retryable, ae.RetryAfter, ae.RunID)
	case errors.Is(err, io.EOF):
		tr.add("%s EOF", what)
	case errors.Is(err, agentclient.ErrStreamTruncated):
		tr.add("%s truncated", what)
	default:
		tr.add("%s other error", what)
		tr.t.Logf("%s: %v", what, err)
	}
}

// stream records every event of s and how it ended.
func (tr *transcript) stream(what string, s *agentclient.Stream, err error) {
	tr.t.Helper()
	tr.err(what+" open", err)
	if err != nil {
		return
	}
	defer s.Close()
	tr.add("%s run=%s conversation=%s", what, s.RunID(), s.ConversationID())
	for {
		_, err := s.Next()
		if err != nil {
			tr.err(what+" end", err)
			return
		}
		tr.add("%s event %s", what, normalized(tr.t, s.RawData()))
	}
}

// until records s's events up to one of type typ, and returns it.
func (tr *transcript) until(what string, s *agentclient.Stream, typ string) wire.Event {
	tr.t.Helper()
	for {
		e, err := s.Next()
		if err != nil {
			tr.t.Fatalf("%s ended before a %s: %v", what, typ, err)
		}
		tr.add("%s event %s", what, normalized(tr.t, s.RawData()))
		if e.Type == typ {
			return e
		}
	}
}

// normalized is JSON without what differs between two runs of a script:
// times ("at", "*_at") and durations ("*_ms").
func normalized(t *testing.T, b []byte) string {
	t.Helper()
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("%s: %v", b, err)
	}
	var strip func(any)
	strip = func(v any) {
		switch v := v.(type) {
		case map[string]any:
			for k, x := range v {
				if k == "at" || strings.HasSuffix(k, "_at") || strings.HasSuffix(k, "_ms") {
					delete(v, k)
					continue
				}
				strip(x)
			}
		case []any:
			for _, x := range v {
				strip(x)
			}
		}
	}
	strip(v)
	out, _ := json.Marshal(v)
	return string(out)
}

type sameCase struct {
	name    string
	replies []llmtest.Reply
	gated   bool
	harness []harness.Option
	api     []agentapi.Option
	script  func(tr *transcript, c *agentclient.Client, m *model)
}

var approveEcho = []harness.Option{
	harness.WithInteraction(harness.Interactive),
	harness.WithToolPolicy(policy.Policy{Rules: []policy.Rule{
		{Name: "hold", Match: policy.Match{Tools: []string{"echo"}}, Action: policy.Approve, Reason: "echo needs a yes"},
	}, Default: policy.Allow}, nil),
}

// The shared table (T091's first criterion): each script runs once on a
// client over HTTP and SSE and once on a client over the WebSocket binding,
// each on a fresh server, and both see the same thing: the same events,
// resources, statuses and error codes. Over the WebSocket, none of the
// script's streams and commands reached the API over HTTP.
func TestSameResultsEitherWay(t *testing.T) {
	t.Parallel()
	for _, tc := range []sameCase{
		{
			name:    "send streams a run",
			replies: []llmtest.Reply{llmtest.ToolCalls(call("toolu_1", "echo", `{"text":"<b>&"}`)), llmtest.Text("Echoed <b>&, done.")},
			script: func(tr *transcript, c *agentclient.Client, _ *model) {
				s, err := c.Stream(tr.t.Context(), agentclient.RunRequest{Message: "echo <b>&"})
				tr.stream("send", s, err)
			},
		},
		{
			name:    "subscribe from the start and after an ID",
			replies: []llmtest.Reply{llmtest.ToolCalls(call("toolu_1", "echo", `{"text":"x"}`)), llmtest.Text("Echoed x.")},
			script: func(tr *transcript, c *agentclient.Client, _ *model) {
				ctx := tr.t.Context()
				s, err := c.Stream(ctx, agentclient.RunRequest{Message: "echo x"})
				if err != nil {
					tr.t.Fatal(err)
				}
				var ids []string
				for {
					e, err := s.Next()
					if err != nil {
						break
					}
					ids = append(ids, e.ID)
				}
				all, err := c.Events(ctx, s.RunID(), "")
				tr.stream("all", all, err)
				for _, after := range []string{ids[1], ids[len(ids)-2], ids[len(ids)-1]} {
					rest, err := c.Events(ctx, s.RunID(), after)
					tr.stream("after "+after, rest, err)
				}
			},
		},
		{
			name: "send to a busy conversation, stop, resume", gated: true,
			replies: []llmtest.Reply{llmtest.Text("first"), llmtest.Text("Resumed.")},
			script: func(tr *transcript, c *agentclient.Client, m *model) {
				ctx := tr.t.Context()
				s, err := c.Stream(ctx, agentclient.RunRequest{Message: "hi"})
				if err != nil {
					tr.t.Fatal(err)
				}
				tr.until("first", s, wire.TypeRunStart)
				// The fake model answers its calls in order: the stop
				// must come once the first run's call took "first", or
				// the resume's would (run_start comes before the call).
				testkit.Eventually(tr.t, 20*time.Second, func() bool { return m.calls.Load() >= 1 })
				busy, err := c.Stream(ctx, agentclient.RunRequest{Message: "again", ConversationID: s.ConversationID()})
				tr.stream("busy", busy, err)
				run, err := c.Stop(ctx, s.RunID())
				tr.err("stop", err)
				tr.add("stopped status=%s", run.Status)
				tr.stream("first rest", s, nil)
				close(m.gate)
				resumed, err := c.Resume(ctx, s.RunID())
				tr.stream("resume", resumed, err)
				_, err = c.Resume(ctx, s.RunID())
				tr.err("resume again", err)
			},
		},
		{
			name: "a server at its limit of runs", gated: true,
			api:     []agentapi.Option{agentapi.WithMaxConcurrentRuns(1)},
			replies: []llmtest.Reply{llmtest.Text("one")},
			script: func(tr *transcript, c *agentclient.Client, m *model) {
				ctx := tr.t.Context()
				s, err := c.Stream(ctx, agentclient.RunRequest{Message: "hi"})
				if err != nil {
					tr.t.Fatal(err)
				}
				tr.until("first", s, wire.TypeRunStart)
				more, err := c.Stream(ctx, agentclient.RunRequest{Message: "another"})
				tr.stream("second", more, err)
				close(m.gate)
				tr.stream("first rest", s, nil)
			},
		},
		{
			name:    "answer an interrupt, then cancel",
			harness: approveEcho,
			replies: []llmtest.Reply{llmtest.ToolCalls(call("toolu_1", "echo", `{"text":"x"}`)), llmtest.Text("Echoed.")},
			script: func(tr *transcript, c *agentclient.Client, _ *model) {
				ctx := tr.t.Context()
				s, err := c.Stream(ctx, agentclient.RunRequest{Message: "echo x", Interaction: "interactive"})
				if err != nil {
					tr.t.Fatal(err)
				}
				end := tr.until("ask", s, wire.TypeRunError)
				_, err = c.Answer(ctx, s.RunID(), wire.Answer{ID: "toolu_wrong", Approved: true})
				tr.err("wrong answer", err)
				run, err := c.Answer(ctx, s.RunID(), wire.Answer{ID: "toolu_1", Approved: true})
				tr.err("answer", err)
				tr.add("answered suspended=%v", run.Status == wire.StatusSuspended)
				// Whether the events after the answer come live or from the
				// store depends on timing, not on the transport: they are
				// read once the run is done.
				testkit.Eventually(tr.t, 20*time.Second, func() bool {
					run, err := c.GetRun(ctx, s.RunID())
					return err == nil && run.Status == wire.StatusDone
				})
				rest, err := c.Events(ctx, s.RunID(), end.ID)
				tr.stream("after the answer", rest, err)
				_, err = c.Answer(ctx, s.RunID(), wire.Answer{ID: "toolu_1", Approved: false})
				tr.err("answer a done run", err)
				_, err = c.Cancel(ctx, s.RunID())
				tr.err("cancel a done run", err)
				_, err = c.Decide(ctx, s.RunID(), wire.DecisionRequest{Path: "x", Approved: true})
				tr.err("decide a chat run", err)
			},
		},
		{
			name: "cancel a suspended run", harness: approveEcho,
			replies: []llmtest.Reply{llmtest.ToolCalls(call("toolu_1", "echo", `{"text":"x"}`))},
			script: func(tr *transcript, c *agentclient.Client, _ *model) {
				ctx := tr.t.Context()
				s, err := c.Stream(ctx, agentclient.RunRequest{Message: "echo x", Interaction: "interactive"})
				tr.stream("ask", s, err)
				run, err := c.Cancel(ctx, s.RunID())
				tr.err("cancel", err)
				tr.value("canceled", run)
				all, err := c.Events(ctx, s.RunID(), "")
				tr.stream("events", all, err)
			},
		},
		{
			name: "steer, pause and resume a task", gated: true,
			replies: []llmtest.Reply{llmtest.Text("one, two, three"), llmtest.Text("one, two, three, four")},
			script: func(tr *transcript, c *agentclient.Client, m *model) {
				ctx := tr.t.Context()
				task, err := c.StartTask(ctx, wire.TaskRequest{Input: "count to three"})
				if err != nil {
					tr.t.Fatal(err)
				}
				msg, err := c.SteerTask(ctx, task.ID, "count to four instead")
				if err != nil {
					tr.t.Fatalf("steer: %v", err)
				}
				tr.value("steered", msg)
				_, err = c.SteerTask(ctx, task.ID, " ")
				tr.err("steer with nothing", err)
				run, err := c.PauseTask(ctx, task.ID)
				if err != nil {
					tr.t.Fatalf("pause: %v", err)
				}
				tr.add("paused status=%s", run.Status)
				run, err = c.ResumeTask(ctx, task.ID)
				tr.err("resume", err)
				tr.add("resumed status=%s", run.Status)
				// The resume took the pause back: the task runs to its end.
				close(m.gate)
				var end wire.Run
				testkit.Eventually(tr.t, 20*time.Second, func() bool {
					end, err = c.GetRun(ctx, task.ID)
					return err == nil && end.Status != wire.StatusRunning
				})
				tr.add("task ended status=%s answer=%q", end.Status, end.Answer)
				_, err = c.SteerTask(ctx, "run_nope", "x")
				tr.err("steer unknown", err)
				_, err = c.PauseTask(ctx, "run_nope")
				tr.err("pause unknown", err)
				_, err = c.ResumeTask(ctx, "run_nope")
				tr.err("resume unknown", err)
			},
		},
		{
			name: "requests the API refuses",
			script: func(tr *transcript, c *agentclient.Client, _ *model) {
				ctx := tr.t.Context()
				s, err := c.Stream(ctx, agentclient.RunRequest{})
				tr.stream("empty message", s, err)
				s, err = c.Stream(ctx, agentclient.RunRequest{Message: "hi", ConversationID: "conv_nope"})
				tr.stream("unknown conversation", s, err)
				s, err = c.Events(ctx, "run_nope", "")
				tr.stream("unknown run", s, err)
				s, err = c.Events(ctx, "run_nope", "not-an-id")
				tr.stream("bad Last-Event-ID", s, err)
				s, err = c.Resume(ctx, "run/nope")
				tr.stream("resume unknown", s, err)
				for _, f := range []func() error{
					func() error { _, err := c.Stop(ctx, "run_nope"); return err },
					func() error { _, err := c.Cancel(ctx, "run_nope"); return err },
					func() error { _, err := c.Answer(ctx, "run_nope", wire.Answer{ID: "a", Text: "b"}); return err },
					func() error { _, err := c.Answer(ctx, "run_nope"); return err },
					func() error { _, err := c.Decide(ctx, "run_nope", wire.DecisionRequest{}); return err },
				} {
					tr.err("command", f())
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			run := func(ws bool) (*rig, []string) {
				m := &model{replies: tc.replies}
				if tc.gated {
					m.gate = make(chan struct{})
				}
				r := newRig(t, m, rigConfig{harness: tc.harness, api: tc.api})
				c := r.httpClient(testToken)
				if ws {
					c = r.wsClient(testToken)
				}
				tr := &transcript{t: t}
				tc.script(tr, c, m)
				return r, tr.lines
			}
			_, overHTTP := run(false)
			r, overWS := run(true)
			if diff := cmp.Diff(overHTTP, overWS); diff != "" {
				t.Errorf("over WebSocket (+) vs over HTTP (-):\n%s", diff)
			}
			if got := slices.DeleteFunc(r.httpRequests(), func(s string) bool {
				// GetRun and StartTask are not commands of the binding.
				return s == "POST /v1/tasks" || strings.HasPrefix(s, "GET /v1/runs/run_") && !strings.HasSuffix(s, "/events")
			}); len(got) > 0 {
				t.Errorf("over WebSocket, these reached the API over HTTP: %q", got)
			}
			if len(overHTTP) < 2 {
				t.Errorf("the script saw almost nothing: %q", overHTTP)
			}
		})
	}
}

// A client whose token the API refuses gets the same 401 either way: over
// the WebSocket, its hello is refused.
func TestUnauthorizedEitherWay(t *testing.T) {
	t.Parallel()
	r := newRig(t, &model{}, rigConfig{})
	for _, c := range []*agentclient.Client{r.httpClient("wrong-token-0123456789"), r.wsClient("wrong-token-0123456789")} {
		_, err := c.Stream(t.Context(), agentclient.RunRequest{Message: "hi"})
		var ae *agentclient.APIError
		if !errors.As(err, &ae) || ae.Status != 401 || ae.Code != wire.CodeUnauthorized {
			t.Errorf("Stream with a wrong token: %v, want 401 unauthorized", err)
		}
		_, err = c.Stop(t.Context(), "run_1")
		if !errors.Is(err, agentclient.ErrUnauthorized) {
			t.Errorf("Stop with a wrong token: %v, want ErrUnauthorized", err)
		}
	}
}
