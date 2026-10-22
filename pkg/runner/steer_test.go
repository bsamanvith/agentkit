package runner_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/runner"
	"github.com/bsamanvith/agentkit/pkg/store"
)

// steerWorld is a world whose processes read their runs' commands, and
// whose echo tool runs, at its first call for a prompt, the function the
// test sets with gateAt: a person acting while a tool call is in flight.
type steerWorld struct {
	*world
	mu    sync.Mutex
	runID string
}

func newSteerWorld(t *testing.T) *steerWorld {
	w := &steerWorld{world: newWorld(t)}
	w.commands = true
	return w
}

// gateAt runs during at the first call of the echo tool with input s.
func (w *steerWorld) gateAt(s string, during func(runID string)) {
	once := sync.Once{}
	w.toolGate = func(_ context.Context, in string) {
		if in != s {
			return
		}
		once.Do(func() {
			w.mu.Lock()
			id := w.runID
			w.mu.Unlock()
			during(id)
		})
	}
}

// start starts a chat run of prompt on conv and records its ID for gateAt.
func (w *steerWorld) start(p *proc, conv, prompt string) *runner.Execution {
	w.t.Helper()
	x, err := p.r.Run(w.ctx, conv, harness.UserText(prompt))
	if err != nil {
		w.t.Fatal(err)
	}
	w.mu.Lock()
	w.runID = x.Run.ID
	w.mu.Unlock()
	return x
}

// send sends cmd to the run, failing the test on an error.
func (w *steerWorld) send(runID string, cmd store.Command) {
	w.t.Helper()
	if _, err := w.mem.SendCommand(w.ctx, runID, cmd); err != nil {
		w.t.Errorf("SendCommand(%s): %v", cmd.Type, err)
	}
}

func (w *steerWorld) steer(runID, key, text string) {
	w.t.Helper()
	cmd, err := runner.SteerCommand(key, harness.UserText(text))
	if err != nil {
		w.t.Fatal(err)
	}
	w.send(runID, cmd)
}

// index returns the index of the first event of evs that match accepts, or
// -1.
func index(evs []runner.Event, match func(runner.Event) bool) int {
	return slices.IndexFunc(evs, match)
}

func harnessOf(kind harness.EventKind, turn int) func(runner.Event) bool {
	return func(ev runner.Event) bool {
		return ev.Kind == runner.EventHarness && ev.Harness.Kind == kind && ev.Harness.Turn == turn
	}
}

func isKind(kind runner.EventKind) func(runner.Event) bool {
	return func(ev runner.Event) bool { return ev.Kind == kind }
}

// lastUser is the last user message of a request.
func lastUser(t *testing.T, req llm.Request) llm.Message {
	t.Helper()
	m := req.Messages[len(req.Messages)-1]
	if m.Role != llm.RoleUser {
		t.Fatalf("request ends with a %s message", m.Role)
	}
	return m
}

// Delivery: a steering message sent while a tool runs is delivered after
// that call's result and before the next model call, in the message of the
// results, and the run goes on to its answer.
func TestSteeringIsDeliveredAtTheNextTurnBoundary(t *testing.T) {
	t.Parallel()
	w := newSteerWorld(t)
	prompt := w.model.on("steer", 2)
	w.gateAt(prompt+"/0", func(id string) { w.steer(id, "k1", "focus on 2026 data") })
	p := w.process()
	x := w.start(p, "c", prompt)

	reqsAtSteer := -1
	evs, err := p.drain(x, func(ev runner.Event) bool {
		if ev.Kind == runner.EventSteered {
			reqsAtSteer = len(w.model.Requests())
		}
		return false
	})
	if err != nil {
		t.Fatal(err)
	}
	done := lastDone(t, evs)
	if done.Run.Status != store.StatusCompleted || done.Result == nil || done.Result.Final.Text() != "answer to "+prompt {
		t.Fatalf("run ended %s with %s, want completed with its answer", done.Run.Status, js(done.Result))
	}
	toolDone, turnDone, steered := index(evs, harnessOf(harness.EventToolDone, 1)), index(evs, harnessOf(harness.EventTurnDone, 1)), index(evs, isKind(runner.EventSteered))
	if toolDone < 0 || turnDone < toolDone || steered < turnDone {
		t.Fatalf("tool_done at %d, turn_done at %d, steered at %d: want them in that order", toolDone, turnDone, steered)
	}
	res := evs[toolDone].Harness.ToolResult
	if res.IsError || !strings.Contains(res.Content, "echo "+prompt+"/0") {
		t.Errorf("the call in flight ended %+v, want its result", res)
	}
	if reqsAtSteer != 1 {
		t.Errorf("%d model calls before the steering was delivered, want 1", reqsAtSteer)
	}
	if got := evs[steered].Steer; len(got) != 1 || got[0].Text() != "focus on 2026 data" {
		t.Errorf("steered %s, want the message", js(got))
	}
	reqs := w.model.Requests()
	if len(reqs) != 3 {
		t.Fatalf("%d model calls, want 3", len(reqs))
	}
	m := lastUser(t, reqs[1])
	if len(m.Content) != 2 {
		t.Fatalf("second request ends with %s, want the call's result then the steering", js(m))
	}
	if r, ok := m.Content[0].(llm.ToolResult); !ok || !sameJSON(r, res) {
		t.Errorf("second request's last message starts with %s, want the result", js(m.Content[0]))
	}
	if txt, ok := m.Content[1].(llm.Text); !ok || txt.Text != "focus on 2026 data" {
		t.Errorf("second request's last message ends with %s, want the steering", js(m.Content[1]))
	}
	// The third request extends the second: the steering stays where it was.
	if !sameJSON(reqs[2].Messages[:len(reqs[1].Messages)], reqs[1].Messages) {
		t.Error("the request after the steered one does not extend it")
	}
	steers := 0
	for _, e := range w.log("c") {
		if e.Kind == store.KindSteer {
			steers++
		}
	}
	if steers != 1 {
		t.Errorf("%d steer entries, want 1", steers)
	}
	if left, err := w.mem.PendingCommands(w.ctx, x.Run.ID, 0); err != nil || len(left) != 0 {
		t.Errorf("pending commands after the run = %v, %v; want none", left, err)
	}
}

// Ordering: messages queued during one tool call are delivered together, in
// the order they were sent; once the run is final, a message is refused.
func TestSteeringMessagesAreDeliveredTogetherInOrder(t *testing.T) {
	t.Parallel()
	w := newSteerWorld(t)
	prompt := w.model.on("order", 1)
	w.gateAt(prompt+"/0", func(id string) {
		w.steer(id, "k1", "first")
		w.steer(id, "k2", "second")
		w.steer(id, "k1", "first") // a retry of the first: the same command
	})
	p := w.process()
	x := w.start(p, "c", prompt)
	evs, err := p.drain(x, nil)
	if err != nil {
		t.Fatal(err)
	}
	if done := lastDone(t, evs); done.Run.Status != store.StatusCompleted {
		t.Fatalf("run %s, want completed", done.Run.Status)
	}
	var steered [][]string
	for _, ev := range evs {
		if ev.Kind == runner.EventSteered {
			var texts []string
			for _, m := range ev.Steer {
				texts = append(texts, m.Text())
			}
			steered = append(steered, texts)
		}
	}
	if want := [][]string{{"first", "second"}}; fmt.Sprint(steered) != fmt.Sprint(want) {
		t.Errorf("steered %v, want %v", steered, want)
	}
	reqs := w.model.Requests()
	m := lastUser(t, reqs[1])
	var texts []string
	for _, b := range m.Content[1:] {
		texts = append(texts, b.(llm.Text).Text)
	}
	if !slices.Equal(texts, []string{"first", "second"}) {
		t.Errorf("the request after the boundary has %v after the results, want first, second", texts)
	}
	for _, e := range w.log("c") {
		if e.Kind == store.KindSteer {
			rec, err := runner.DecodeRecord(e)
			if err != nil || rec.Steer == nil || !slices.Equal(rec.Steer.Commands, []int64{1, 2}) {
				t.Errorf("steer entry record %s (%v), want commands 1 and 2", e.Record, err)
			}
		}
	}

	cmd, _ := runner.SteerCommand("late", harness.UserText("too late"))
	if _, err := w.mem.SendCommand(w.ctx, x.Run.ID, cmd); !errors.Is(err, store.ErrFinal) {
		t.Errorf("steering a completed run = %v, want ErrFinal", err)
	}
}

// A steering message sent as the model gives its answer is delivered after
// the answer, and the model answers it: nothing sent is lost.
func TestSteeringAfterTheLastTurnIsAnswered(t *testing.T) {
	t.Parallel()
	w := newSteerWorld(t)
	prompt := w.model.on("last", 0)
	w.model.gate = func(_ context.Context, p string, turn int) {
		if p == prompt && turn == 0 {
			w.mu.Lock()
			id := w.runID
			w.mu.Unlock()
			w.steer(id, "k1", "and in French?")
		}
	}
	p := w.process()
	x := w.start(p, "c", prompt)
	evs, err := p.drain(x, nil)
	if err != nil {
		t.Fatal(err)
	}
	done := lastDone(t, evs)
	reqs := w.model.Requests()
	if len(reqs) != 2 || done.Run.Status != store.StatusCompleted {
		t.Fatalf("%d model calls, run %s; want 2, completed", len(reqs), done.Run.Status)
	}
	msgs := reqs[1].Messages
	if n := len(msgs); n != 3 || msgs[1].Role != llm.RoleAssistant || msgs[2].Text() != "and in French?" {
		t.Errorf("second request %s, want the answer and the steering after it", js(msgs))
	}
}

// Pause and resume: a pause sent while a tool runs lets the call finish and
// stops the run at the boundary, paused; a restart leaves it paused, and a
// resume continues it to its answer.
func TestPauseStopsAtTheBoundaryAndSurvivesARestart(t *testing.T) {
	t.Parallel()
	w := newSteerWorld(t)
	prompt := w.model.on("pause", 2)
	w.gateAt(prompt+"/0", func(id string) { w.send(id, runner.PauseCommand("p1")) })
	p := w.process()
	x := w.start(p, "c", prompt)
	evs, err := p.drain(x, nil)
	if err != nil {
		t.Fatal(err)
	}
	done := lastDone(t, evs)
	if done.Run.Status != store.StatusPaused {
		t.Fatalf("run %s after a pause, want paused", done.Run.Status)
	}
	if i := index(evs, harnessOf(harness.EventToolDone, 1)); i < 0 || evs[i].Harness.ToolResult.IsError {
		t.Errorf("the call in flight did not finish: %s", js(evs))
	}
	if n := len(w.model.Requests()); n != 1 {
		t.Errorf("%d model calls before the pause took, want 1", n)
	}
	if run := w.get(x.Run.ID); run.Status != store.StatusPaused || run.Usage.InputTokens == 0 {
		t.Errorf("stored run %+v, want paused with the usage so far", run)
	}

	// The process dies; another starts, and its dispatcher passes.
	w.expire()
	p2 := w.process()
	if rep, err := w.recovery(p2).Recover(w.ctx); err != nil || rep.Resumed != 0 {
		t.Fatalf("recovery after a restart = %+v, %v; want nothing resumed", rep, err)
	}
	if run := w.get(x.Run.ID); run.Status != store.StatusPaused {
		t.Fatalf("run %s after a restart, want paused", run.Status)
	}
	if n := len(w.model.Requests()); n != 1 {
		t.Fatalf("%d model calls while paused, want 1", n)
	}

	evs, err = p2.resume(x.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if done := lastDone(t, evs); done.Run.Status != store.StatusCompleted || done.Result.Final.Text() != "answer to "+prompt {
		t.Fatalf("resumed run ended %s with %s, want its answer", done.Run.Status, js(done.Result))
	}
	if n := len(w.model.Requests()); n != 3 {
		t.Errorf("%d model calls in all, want 3: none done twice", n)
	}
}

// A paused run can be canceled, and is final then: steering it is refused.
func TestAPausedRunCanBeCanceled(t *testing.T) {
	t.Parallel()
	w := newSteerWorld(t)
	prompt := w.model.on("cancel", 2)
	w.gateAt(prompt+"/0", func(id string) { w.send(id, runner.PauseCommand("p1")) })
	p := w.process()
	x := w.start(p, "c", prompt)
	evs, err := p.drain(x, nil)
	if err != nil {
		t.Fatal(err)
	}
	if done := lastDone(t, evs); done.Run.Status != store.StatusPaused {
		t.Fatalf("run %s after a pause, want paused", done.Run.Status)
	}
	if run, err := p.r.Cancel(w.ctx, x.Run.ID); err != nil || run.Status != store.StatusCanceled {
		t.Fatalf("Cancel of a paused run = %s, %v; want canceled", run.Status, err)
	}
	if _, err := p.r.Resume(w.ctx, x.Run.ID); !errors.Is(err, store.ErrFinal) {
		t.Errorf("Resume of the canceled run: %v, want ErrFinal", err)
	}
	if n := len(w.model.Requests()); n != 1 {
		t.Errorf("%d model calls, want 1", n)
	}
}

// A resume sent after a pause, before the run reached a boundary, withdraws
// it; a pause at the model's answer lets the run complete.
func TestResumeWithdrawsAPauseNotActedOn(t *testing.T) {
	t.Parallel()
	for name, cmds := range map[string][]store.Command{
		"pause then resume": {runner.PauseCommand("p"), runner.ResumeCommand("r")},
		"pause at the end":  nil,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			w := newSteerWorld(t)
			tools := 1
			if cmds == nil {
				tools = 0
			}
			prompt := w.model.on("w", tools)
			w.gateAt(prompt+"/0", func(id string) {
				for _, c := range cmds {
					w.send(id, c)
				}
			})
			if cmds == nil {
				w.model.gate = func(_ context.Context, _ string, _ int) {
					w.mu.Lock()
					id := w.runID
					w.mu.Unlock()
					w.send(id, runner.PauseCommand("p"))
				}
			}
			p := w.process()
			x := w.start(p, "c", prompt)
			evs, err := p.drain(x, nil)
			if err != nil {
				t.Fatal(err)
			}
			if done := lastDone(t, evs); done.Run.Status != store.StatusCompleted {
				t.Errorf("run %s, want completed", done.Run.Status)
			}
		})
	}
}

// Steering sent to a run nobody runs (its process died) is delivered when
// the run is resumed, before its next model call; and a crash after the
// steering was stored but before its command was consumed delivers it once.
func TestSteeringReachesARunAcrossACrash(t *testing.T) {
	t.Parallel()
	for _, how := range []string{"sent while down", "crash before consuming"} {
		t.Run(how, func(t *testing.T) {
			t.Parallel()
			w := newSteerWorld(t)
			prompt := w.model.on("crash", 2)
			p := w.process()
			if how == "sent while down" {
				p.store.crashAt(crashAtNth("Append", 2, true)) // the prompt and turn 1 stored, then dead
			} else {
				w.gateAt(prompt+"/0", func(id string) { w.steer(id, "k1", "skip the PDF") })
				p.store.crashAt(crashAtNth("ConsumeCommands", 1, false))
			}
			x := w.start(p, "c", prompt)
			if _, err := p.drain(x, nil); !errors.Is(err, errCrashed) {
				t.Fatalf("first execution ended with %v, want the crash", err)
			}
			if how == "sent while down" {
				w.steer(x.Run.ID, "k1", "skip the PDF")
			}
			w.expire()
			p2 := w.process()
			if rep, err := w.recovery(p2).Recover(w.ctx); err != nil || rep.Resumed != 1 {
				t.Fatalf("recovery = %+v, %v; want the run resumed", rep, err)
			}
			if run := w.get(x.Run.ID); run.Status != store.StatusCompleted {
				t.Fatalf("run %s, want completed", run.Status)
			}
			steers := 0
			for _, e := range w.log("c") {
				if e.Kind == store.KindSteer {
					steers++
				}
			}
			if steers != 1 {
				t.Errorf("%d steer entries, want 1", steers)
			}
			reqs := w.model.Requests()
			m := lastUser(t, reqs[len(reqs)-2])
			if txt, ok := m.Content[len(m.Content)-1].(llm.Text); !ok || txt.Text != "skip the PDF" {
				t.Errorf("the model call after the resume has %s last, want the steering", js(m))
			}
			if left, err := w.mem.PendingCommands(w.ctx, x.Run.ID, 0); err != nil || len(left) != 0 {
				t.Errorf("pending commands after the run = %v, %v; want none", left, err)
			}
		})
	}
}

// The runner reads no further than a command that is not its own (an API
// server's stop or cancel): what follows waits for that one's owner.
func TestSteeringWaitsBehindAnotherCommand(t *testing.T) {
	t.Parallel()
	w := newSteerWorld(t)
	prompt := w.model.on("behind", 1)
	w.gateAt(prompt+"/0", func(id string) {
		w.send(id, store.Command{Key: "s", Type: "stop"})
		w.steer(id, "k1", "later")
	})
	p := w.process()
	x := w.start(p, "c", prompt)
	evs, err := p.drain(x, nil)
	if err != nil {
		t.Fatal(err)
	}
	if index(evs, isKind(runner.EventSteered)) >= 0 {
		t.Error("steering behind a stop was delivered")
	}
	left, err := w.mem.PendingCommands(w.ctx, x.Run.ID, 0)
	if err != nil || len(left) != 2 {
		t.Errorf("pending commands = %v, %v; want the stop and the steering", left, err)
	}
}

// A steering message is a user message of text.
func TestSteerCommandRefusesWhatIsNotText(t *testing.T) {
	t.Parallel()
	for name, msg := range map[string]llm.Message{
		"empty":      llm.User(),
		"assistant":  llm.Assistant(llm.Text{Text: "x"}),
		"empty text": llm.User(llm.Text{}),
		"a result":   llm.User(llm.ToolResult{ToolUseID: "a", Content: "x"}),
	} {
		if _, err := runner.SteerCommand("k", msg); !errors.Is(err, runner.ErrInvalidInput) {
			t.Errorf("%s: SteerCommand = %v, want ErrInvalidInput", name, err)
		}
	}
}
