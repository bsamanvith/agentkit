package runner_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"maps"
	"math/rand/v2"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/clock/clocktest"
	"github.com/bsamanvith/agentkit/pkg/flow"
	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/llm/llmtest"
	"github.com/bsamanvith/agentkit/pkg/policy"
	"github.com/bsamanvith/agentkit/pkg/runner"
	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/store/memory"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

const ttl = 30 * time.Second

// world is everything outside the runner processes of a test: one store, one
// clock, one model provider and one set of tools, shared by every process the
// test starts. When the test ends it checks the invariants that must hold
// after every run (ENGINEERING §2.6):
//
//  1. Entries mirror events: every prompt or turn event a consumer saw is
//     stored at the position it names, as the same messages and record,
//     once an EventSaved at or past that position followed it. Only events
//     past the last EventSaved of an execution that did not complete are
//     unchecked: their positions may be taken on resume.
//  2. The stored context round-trips to the harness transcript: every
//     conversation's context is a valid transcript, and a completed run's
//     Result transcript is exactly the log up to its last entry.
//  3. No turn is stored twice: no tool call ID appears twice in a log, but
//     for the copies a compaction keeps.
//  4. Leases are released on every exit path: after an execution of a live
//     process ends, it holds no lease, and when the test ends no live
//     process does.
//  5. The cache equals a cold load: after every execution, the runner's
//     context of the conversation equals the store's.
//  6. The model is only sent valid conversations.
type world struct {
	t     *testing.T
	ctx   context.Context
	clk   *clocktest.Fake
	mem   *memory.Store
	model *scriptModel
	tools tool.Registry
	// toolGate, if set, runs inside every tool call.
	toolGate func(ctx context.Context, s string)
	// flows, if set, gives every process's flow factory the step tree.
	flows func(p *proc, conv string) (flow.Step, error)
	// agent, if set, replaces the harness of every process.
	agent func(conv string) runner.Agent
	// hopts are options of every process's harness.
	hopts []harness.Option
	// written counts the calls of the write tool, by input.
	written map[string]int

	mu sync.Mutex
	// placements is every placement any process asked its factory for.
	placements []runner.Placement
	procs      []*proc
	seen       []seen
	ids        int
	// touched is every conversation any process appended to, in its
	// tenant's context: the invariants are checked on all of them, not
	// only on those whose executions a test drained.
	touched map[touch]context.Context
}

type touch struct{ tenant, conv string }

// seen is what a consumer saw of one execution.
type seen struct {
	conv   string
	events []runner.Event
}

// proc is one runner process. Its store dies when the process crashes.
type proc struct {
	w     *world
	r     *runner.Runner
	store *crashStore
	owner string
}

func newWorld(t *testing.T) *world {
	t.Helper()
	acme, err := tenancy.NewTenant("acme")
	if err != nil {
		t.Fatal(err)
	}
	w := &world{
		t:   t,
		ctx: tenancy.With(context.Background(), acme),
		clk: clocktest.NewFake(time.Time{}),
	}
	w.mem = memory.New(memory.WithClock(w.clk))
	w.model = newScriptModel(t)
	w.setTools()
	t.Cleanup(w.check)
	return w
}

// readOnly declares the echo tool read-only, so that its turns are kept
// until a flush. By default it declares nothing, and is taken for a write.
// Call it before starting processes.
func (w *world) readOnly() { w.setTools(tool.WithMetadata(tool.Metadata{Kind: tool.KindRead})) }

func (w *world) setTools(opts ...tool.FuncOption) {
	w.t.Helper()
	echo, err := tool.Func("echo", "Echo s.", func(ctx context.Context, in struct {
		S string `json:"s"`
	},
	) (string, error) {
		if w.toolGate != nil {
			w.toolGate(ctx, in.S)
		}
		return "echo " + in.S, ctx.Err()
	}, opts...)
	if err != nil {
		w.t.Fatal(err)
	}
	// ask asks "yes or no?"; write writes s, and counts its calls.
	ask, err := tool.Func("ask", "Ask the user.", func(context.Context, struct {
		S string `json:"s"`
	},
	) (string, error) {
		return "", tool.Interrupt("yes or no?", "yes", "no")
	}, tool.WithMetadata(tool.Metadata{Kind: tool.KindRead, Risk: tool.RiskLow, Interrupts: true}))
	if err != nil {
		w.t.Fatal(err)
	}
	write, err := tool.Func("write", "Write s.", func(_ context.Context, in struct {
		S string `json:"s"`
	},
	) (string, error) {
		w.mu.Lock()
		defer w.mu.Unlock()
		if w.written == nil {
			w.written = map[string]int{}
		}
		w.written[in.S]++
		return "wrote " + in.S, nil
	}, tool.WithMetadata(tool.Metadata{Kind: tool.KindWrite, Risk: tool.RiskMedium}))
	if err != nil {
		w.t.Fatal(err)
	}
	if w.tools, err = tool.NewRegistry([]tool.Tool{echo, ask, write}); err != nil {
		w.t.Fatal(err)
	}
}

// interactions makes every process's harness interactive, with a policy that
// holds every call of the write tool for an approval. Call it before
// starting processes.
func (w *world) interactions() {
	w.hopts = []harness.Option{
		harness.WithInteraction(harness.Interactive),
		harness.WithToolPolicy(policy.Policy{Rules: []policy.Rule{
			{Name: "hold-writes", Match: policy.Match{Tools: []string{"write"}}, Action: policy.Approve, Reason: "writes need a yes"},
		}, Default: policy.Allow}, nil),
	}
}

// writes returns how many times the write tool ran with s.
func (w *world) writes(s string) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.written[s]
}

// process starts a runner process on the world's store.
func (w *world) process(opts ...runner.Option) *proc {
	w.t.Helper()
	w.mu.Lock()
	n := len(w.procs)
	w.mu.Unlock()
	p := &proc{w: w, store: &crashStore{Store: w.mem, w: w}, owner: fmt.Sprintf("proc%d", n)}
	agents := func(_ context.Context, pl runner.Placement) (runner.Agent, error) {
		conv := pl.ConversationID
		w.mu.Lock()
		w.placements = append(w.placements, pl)
		w.mu.Unlock()
		if w.agent != nil {
			return w.agent(conv), nil
		}
		return harness.New(w.model, w.tools, append([]harness.Option{harness.WithClock(w.clk)}, w.hopts...)...)
	}
	base := []runner.Option{
		runner.WithClock(w.clk), runner.WithOwner(p.owner), runner.WithLease(ttl),
		runner.WithIDs(w.newID), runner.WithRand(rand.NewPCG(1, 2)),
	}
	if w.flows != nil {
		base = append(base, runner.WithFlows(func(_ context.Context, conv string) (flow.Step, error) { return w.flows(p, conv) }))
	}
	r, err := runner.New(p.store, agents, append(base, opts...)...)
	if err != nil {
		w.t.Fatal(err)
	}
	p.r = r
	w.mu.Lock()
	w.procs = append(w.procs, p)
	w.mu.Unlock()
	return p
}

func (w *world) newID() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.ids++
	return fmt.Sprintf("run%d", w.ids)
}

// drain consumes an execution like a client would, stopping after the first
// event for which stop returns true (nil: read to the end). It records what
// it saw for the invariants and checks those of an execution that ended.
func (p *proc) drain(x *runner.Execution, stop func(runner.Event) bool) ([]runner.Event, error) {
	p.w.t.Helper()
	var evs []runner.Event
	var end error
	for ev, err := range x.Events() {
		if err != nil {
			end = err
			break
		}
		evs = append(evs, ev)
		if stop != nil && stop(ev) {
			break
		}
	}
	p.w.mu.Lock()
	p.w.seen = append(p.w.seen, seen{conv: x.Run.ConversationID, events: evs})
	p.w.mu.Unlock()
	p.checkAfter(x)
	return evs, end
}

// checkAfter checks invariants 4 and 5 for an execution that has ended.
func (p *proc) checkAfter(x *runner.Execution) {
	p.w.t.Helper()
	if !p.store.isDead() {
		// The process outlived the execution: the checks below must not
		// make it die.
		p.store.crashAt(nil)
		p.w.checkLeaseFree(x.Run)
		p.w.checkCache(p, x.Run.ConversationID)
	}
}

// drainInBackground drains x on another goroutine while the test acts on the
// run, and sends the execution's end on the channel. The test may still be
// writing when the execution ends (a Cancel appending its marker), so the
// per-execution checks wait for the end of the test, when all is quiet.
func (p *proc) drainInBackground(x *runner.Execution) <-chan error {
	done := make(chan error, 1)
	go func() {
		var evs []runner.Event
		var end error
		for ev, err := range x.Events() {
			if err != nil {
				end = err
				break
			}
			evs = append(evs, ev)
		}
		p.w.mu.Lock()
		p.w.seen = append(p.w.seen, seen{conv: x.Run.ConversationID, events: evs})
		p.w.mu.Unlock()
		done <- end
	}()
	p.w.t.Cleanup(func() { p.checkAfter(x) })
	return done
}

// run starts a chat run and drains it.
func (p *proc) run(conv, prompt string) ([]runner.Event, error) {
	p.w.t.Helper()
	x, err := p.r.Run(p.w.ctx, conv, harness.UserText(prompt))
	if err != nil {
		return nil, err
	}
	return p.drain(x, nil)
}

// resume resumes a run and drains it.
func (p *proc) resume(runID string, opts ...runner.ResumeOption) ([]runner.Event, error) {
	p.w.t.Helper()
	x, err := p.r.Resume(p.w.ctx, runID, opts...)
	if err != nil {
		return nil, err
	}
	return p.drain(x, nil)
}

// checkLeaseFree checks that the execution made with claimed no longer holds
// its lease. Another execution of the same process (a Cancel, say) may hold a
// lease of its own by now, so only the execution's own owner counts.
func (w *world) checkLeaseFree(claimed store.Run) {
	w.t.Helper()
	if claimed.Lease.Owner == "" {
		if claimed.Status.Final() || claimed.Status == store.StatusSuspended {
			return // a final run, or an answer that resumes nothing, is returned without a claim
		}
		w.t.Errorf("invariant 4: execution of run %q does not name its lease", claimed.ID)
		return
	}
	run, err := w.mem.Get(w.ctx, claimed.ID)
	if err != nil {
		w.t.Errorf("invariant 4: get run %q: %v", claimed.ID, err)
		return
	}
	if run.Lease.Owner == claimed.Lease.Owner && run.Lease.Expires.After(w.clk.Now()) {
		w.t.Errorf("invariant 4: run %q still leased to %q after its execution ended", claimed.ID, run.Lease.Owner)
	}
}

func (w *world) checkCache(p *proc, conv string) {
	w.t.Helper()
	got, seq, err := p.r.Context(w.ctx, conv)
	if err != nil {
		w.t.Errorf("invariant 5: runner context of %q: %v", conv, err)
		return
	}
	cv, err := w.mem.Context(w.ctx, conv)
	if err != nil {
		w.t.Fatal(err)
	}
	if seq != cv.Seq || !sameJSON(got, messagesOf(cv.Entries)) {
		w.t.Errorf("invariant 5: cached context of %q at %d = %s, cold load at %d = %s", conv, seq, js(got), cv.Seq, js(messagesOf(cv.Entries)))
	}
}

// log returns every entry of a conversation.
func (w *world) log(conv string) []store.Entry {
	w.t.Helper()
	return w.logIn(w.ctx, conv)
}

// logIn is the log of conv in the tenant of ctx.
func (w *world) logIn(ctx context.Context, conv string) []store.Entry {
	w.t.Helper()
	var out []store.Entry
	p := store.Page{Order: store.Ascending, Limit: store.MaxPageLimit}
	for {
		page, err := w.mem.History(ctx, conv, p)
		if err != nil {
			w.t.Fatal(err)
		}
		out = append(out, page.Entries...)
		if page.Next == "" {
			return out
		}
		p = store.Page{Cursor: page.Next, Order: store.Ascending, Limit: store.MaxPageLimit}
	}
}

// transcript is a conversation's stored context.
func (w *world) transcript(conv string) harness.Transcript {
	w.t.Helper()
	return w.transcriptIn(w.ctx, conv)
}

func (w *world) transcriptIn(ctx context.Context, conv string) harness.Transcript {
	w.t.Helper()
	cv, err := w.mem.Context(ctx, conv)
	if err != nil {
		w.t.Fatal(err)
	}
	return messagesOf(cv.Entries)
}

func (w *world) get(runID string) store.Run {
	w.t.Helper()
	run, err := w.mem.Get(w.ctx, runID)
	if err != nil {
		w.t.Fatal(err)
	}
	return run
}

// expire moves the clock past every lease.
func (w *world) expire() { w.clk.Advance(ttl + time.Second) }

func (w *world) check() {
	t := w.t
	w.mu.Lock()
	defer w.mu.Unlock()
	convs := maps.Clone(w.touched)
	if convs == nil {
		convs = map[touch]context.Context{}
	}
	acme := tenancy.MustFrom(w.ctx).ID()
	for _, s := range w.seen {
		convs[touch{acme, s.conv}] = w.ctx
		var last, saved int64
		for _, ev := range s.events {
			if ev.Kind == runner.EventSaved {
				if ev.Seq < last || ev.Seq <= saved {
					t.Errorf("invariant 1: saved up to %d of %q after events up to %d, saved before up to %d", ev.Seq, s.conv, last, saved)
				}
				saved = ev.Seq
			}
			if ev.Kind == runner.EventDone && ev.Result != nil && saved < last {
				t.Errorf("invariant 1: run %q completed with entries %d-%d of %q not saved", ev.RunID, saved+1, last, s.conv)
			}
			last = max(last, ev.Seq)
		}
		for _, ev := range s.events {
			if ev.Kind != runner.EventHarness {
				continue
			}
			if ev.Seq == 0 {
				if ev.Harness.Kind == harness.EventRunStart && ev.Harness.Prompt != nil ||
					ev.Harness.Kind == harness.EventTurnDone && ev.Harness.TurnRecord != nil {
					t.Errorf("invariant 1: %s event of %q names no entry", ev.Harness.Kind, s.conv)
				}
				continue
			}
			if ev.Seq > saved {
				continue
			}
			log := w.log(s.conv)
			if ev.Seq > int64(len(log)) {
				t.Errorf("invariant 1: %s event at %d of %q, log has %d entries", ev.Harness.Kind, ev.Seq, s.conv, len(log))
				continue
			}
			e := log[ev.Seq-1]
			switch ev.Harness.Kind {
			case harness.EventRunStart:
				rec, err := runner.DecodeRecord(e)
				if err != nil || e.Kind != store.KindPrompt || rec.Prompt == nil || !rec.Prompt.At.Equal(ev.Harness.Prompt.At) ||
					!sameJSON(e.Messages, []llm.Message{ev.Harness.Prompt.Message}) || e.RunID != ev.RunID {
					t.Errorf("invariant 1: prompt entry %d of %q = %s, event %s", ev.Seq, s.conv, js(e), js(ev.Harness.Prompt))
				}
			case harness.EventTurnDone:
				got, err := runner.HarnessTurn(e)
				if err != nil || !sameJSON(got, *ev.Harness.TurnRecord) || e.RunID != ev.RunID {
					t.Errorf("invariant 1: turn entry %d of %q = %s (%v), event %s", ev.Seq, s.conv, js(got), err, js(ev.Harness.TurnRecord))
				}
			case harness.EventSuspended:
				got, answers, err := runner.PendingTurn(e)
				if err != nil || !sameJSON(got, *ev.Harness.Result.Pending) || len(answers) != 0 || e.RunID != ev.RunID {
					t.Errorf("invariant 1: interrupt entry %d of %q = %s (%v), event %s", ev.Seq, s.conv, js(got), err, js(ev.Harness.Result.Pending))
				}
			default:
				t.Errorf("invariant 1: %s event has a Seq", ev.Harness.Kind)
			}
		}
		for _, ev := range s.events {
			if ev.Kind == runner.EventDone && ev.Result != nil {
				log := w.log(s.conv)
				if !sameJSON(ev.Result.Transcript, messagesOf(log[:min(int(last), len(log))])) {
					t.Errorf("invariant 2: run %q Result transcript %s, log up to %d %s", ev.RunID, js(ev.Result.Transcript), last, js(messagesOf(log)))
				}
			}
		}
	}
	for c, ctx := range convs {
		conv := c.conv
		if tr := w.transcriptIn(ctx, conv); len(tr) > 0 {
			if err := tr.Validate(); err != nil {
				t.Errorf("invariant 2: context of %q is not a valid transcript: %v\n%s", conv, err, js(tr))
			}
		}
		ids := map[string]bool{}
		w.checkInterrupts(ctx, conv)
		for _, e := range w.logIn(ctx, conv) {
			if e.Kind == store.KindCompaction || e.Kind == store.KindInterrupt {
				continue // its tail is a copy of the entries before it; the reply is the next turn's
			}
			for _, m := range e.Messages {
				for _, b := range m.Content {
					if u, ok := b.(llm.ToolUse); ok {
						if ids[u.ID] {
							t.Errorf("invariant 3: tool call %q stored twice in %q", u.ID, conv)
						}
						ids[u.ID] = true
					}
				}
			}
		}
	}
	dead := map[string]bool{}
	for _, p := range w.procs {
		if p.store.isDead() {
			dead[p.owner] = true
		}
	}
	for c, ctx := range convs {
		conv := c.conv
		page, err := w.mem.List(ctx, conv, store.Page{Limit: store.MaxPageLimit})
		if err != nil {
			t.Fatal(err)
		}
		for _, run := range page.Runs {
			owner, _, _ := strings.Cut(run.Lease.Owner, "/")
			if run.Lease.Owner != "" && run.Lease.Expires.After(w.clk.Now()) && !dead[owner] {
				t.Errorf("invariant 4: run %q of %q leased to %q, a live process, at the end", run.ID, conv, run.Lease.Owner)
			}
		}
	}
	for i, req := range w.model.Requests() {
		if err := harness.Transcript(req.Messages).Validate(); err != nil {
			t.Errorf("invariant 6: request %d is not a valid conversation: %v", i, err)
		}
	}
}

func messagesOf(es []store.Entry) harness.Transcript {
	var tr harness.Transcript
	for _, e := range es {
		if e.Kind == store.KindInterrupt {
			continue // not part of the context
		}
		if e.Kind == store.KindCompaction {
			tr = nil
		}
		tr = append(tr, e.Messages...)
	}
	return tr
}

func js(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("<%v>", err)
	}
	return string(b)
}

func sameJSON(a, b any) bool {
	ja, err1 := json.Marshal(a)
	jb, err2 := json.Marshal(b)
	return err1 == nil && err2 == nil && bytes.Equal(ja, jb)
}

// scriptModel is a model provider whose answers depend only on the request:
// for the prompt "q:<name>" it calls the echo tool script.tools times, then
// answers. A run interrupted anywhere and resumed therefore gets the same
// answers as one that was not. Scripted failures are consumed once, like a
// provider's outage.
type scriptModel struct {
	t       *testing.T
	mu      sync.Mutex
	scripts map[string]*script
	reqs    []llm.Request
	// gate, if set, runs before every reply, with the prompt and the turn.
	gate func(ctx context.Context, prompt string, turn int)
}

type script struct {
	tools int
	// calls, if set, replaces tools: calls[turn] are the names of the
	// tools turn calls, with IDs prompt#turn.k and inputs prompt/turn.k.
	calls [][]string
	// fail[turn] is how many times that turn fails with err first.
	fail map[int]int
	err  error
}

func newScriptModel(t *testing.T) *scriptModel {
	return &scriptModel{t: t, scripts: map[string]*script{}}
}

// on scripts the prompt "q:"+name and returns the prompt.
func (m *scriptModel) on(name string, tools int) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.scripts["q:"+name] = &script{tools: tools, fail: map[int]int{}}
	return "q:" + name
}

// onCalls scripts the prompt "q:"+name to call, at each turn, the tools
// named, then answer.
func (m *scriptModel) onCalls(name string, turns ...[]string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.scripts["q:"+name] = &script{calls: turns, fail: map[int]int{}}
	return "q:" + name
}

// failing makes turn of name's script fail n times with err.
func (m *scriptModel) failing(name string, turn, n int, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.scripts["q:"+name]
	s.fail[turn], s.err = n, err
}

func (m *scriptModel) Requests() []llm.Request {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]llm.Request, len(m.reqs))
	for i, r := range m.reqs {
		out[i] = r.Clone()
	}
	return out
}

// requestsFor returns the requests made for the prompt.
func (m *scriptModel) requestsFor(prompt string) []llm.Request {
	var out []llm.Request
	for _, r := range m.Requests() {
		if p, _ := promptOf(r.Messages); p == prompt {
			out = append(out, r)
		}
	}
	return out
}

// promptOf returns the last scripted prompt of msgs and how many replies
// follow it.
func promptOf(msgs []llm.Message) (string, int) {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == llm.RoleUser && strings.HasPrefix(msgs[i].Text(), "q:") {
			n := 0
			for _, m := range msgs[i+1:] {
				if m.Role == llm.RoleAssistant {
					n++
				}
			}
			return msgs[i].Text(), n
		}
	}
	return "", 0
}

func (m *scriptModel) Generate(ctx context.Context, req llm.Request) iter.Seq2[llm.Event, error] {
	prompt, turn := promptOf(req.Messages)
	m.mu.Lock()
	m.reqs = append(m.reqs, req.Clone())
	s := m.scripts[prompt]
	gate := m.gate
	var reply llmtest.Reply
	usage := llm.Usage{InputTokens: 10 + turn, OutputTokens: 3}
	switch {
	case s == nil:
		reply = llmtest.Text("unscripted")
	case s.fail[turn] > 0:
		s.fail[turn]--
		reply = llmtest.Fail(s.err)
	case turn < len(s.calls):
		var uses []llm.ToolUse
		for k, name := range s.calls[turn] {
			id := fmt.Sprintf("%s#%d.%d", prompt, turn, k)
			uses = append(uses, llm.ToolUse{ID: id, Name: name, Input: json.RawMessage(fmt.Sprintf(`{"s":%q}`, id))})
		}
		reply = llmtest.ToolCalls(uses...).WithUsage(usage)
	case turn < s.tools:
		reply = llmtest.ToolCalls(llm.ToolUse{
			ID: fmt.Sprintf("%s#%d", prompt, turn), Name: "echo",
			Input: json.RawMessage(fmt.Sprintf(`{"s":%q}`, fmt.Sprintf("%s/%d", prompt, turn))),
		}).WithUsage(usage)
	default:
		reply = llmtest.Text("answer to " + prompt).WithUsage(usage)
	}
	m.mu.Unlock()
	if gate != nil {
		gate(ctx, prompt, turn)
	}
	return llmtest.NewFake(m.t, reply).Generate(ctx, req)
}

// crashStore is a process's view of the shared store. When the process
// crashes, at the call die picks, that call fails (after reaching the store if
// landed says so) and so does every later one: nothing the process does after
// its crash reaches the store, not even releasing its leases.
type crashStore struct {
	runner.Store
	w      *world // told of every conversation appended to
	mu     sync.Mutex
	counts map[string]int
	total  int
	dead   bool
	// die, if set, is asked before every call whether the process crashes
	// at it, and whether the call reaches the store first.
	die func(op string, n, total int) (crash, landed bool)
	// fail, if set, is asked the same, for a call that fails (a timeout)
	// without the process dying.
	fail func(op string, n int) (fail, landed bool)
	// before, if set, runs before the n-th call of op reaches the store:
	// another process acting at that point.
	before func(op string, n int)
}

var (
	errCrashed = errors.New("process crashed")
	errFlaky   = errors.New("store timed out")
)

// writes returns how many times the process called each method that writes.
func (c *crashStore) writes() map[string]int {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := map[string]int{}
	for _, op := range []string{"Append", "AppendBatch", "Create", "Transition", "Claim", "Renew", "Release", "Save"} {
		if n := c.counts[op]; n > 0 {
			out[op] = n
		}
	}
	return out
}

func (c *crashStore) isDead() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.dead
}

func (c *crashStore) crashAt(die func(op string, n, total int) (crash, landed bool)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.die = die
}

// enter decides the fate of one call: run reports whether it reaches the
// store, and err what it returns instead of the store's answer.
func (c *crashStore) enter(op string) (run bool, err error) {
	c.mu.Lock()
	n, before := c.counts[op]+1, c.before
	c.mu.Unlock()
	if before != nil {
		before(op, n)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.dead {
		return false, errCrashed
	}
	if c.counts == nil {
		c.counts = map[string]int{}
	}
	c.counts[op]++
	c.total++
	if c.die != nil {
		if crash, landed := c.die(op, c.counts[op], c.total); crash {
			c.dead = true
			return landed, errCrashed
		}
	}
	if c.fail != nil {
		if fail, landed := c.fail(op, c.counts[op]); fail {
			return landed, errFlaky
		}
	}
	return true, nil
}

func call[T any](c *crashStore, op string, f func() (T, error)) (T, error) {
	run, cerr := c.enter(op)
	var zero T
	if !run {
		return zero, cerr
	}
	v, err := f()
	if cerr != nil {
		return zero, cerr
	}
	return v, err
}

func call0(c *crashStore, op string, f func() error) error {
	_, err := call(c, op, func() (struct{}, error) { return struct{}{}, f() })
	return err
}

func (c *crashStore) Append(ctx context.Context, id string, e store.Entry) error {
	c.w.touch(ctx, id)
	return call0(c, "Append", func() error { return c.Store.Append(ctx, id, e) })
}

func (c *crashStore) AppendBatch(ctx context.Context, id string, es []store.Entry) error {
	c.w.touch(ctx, id)
	return call0(c, "AppendBatch", func() error { return c.Store.AppendBatch(ctx, id, es) })
}

func (c *crashStore) History(ctx context.Context, id string, p store.Page) (store.EntryPage, error) {
	return call(c, "History", func() (store.EntryPage, error) { return c.Store.History(ctx, id, p) })
}

func (c *crashStore) Context(ctx context.Context, id string) (store.ContextView, error) {
	return call(c, "Context", func() (store.ContextView, error) { return c.Store.Context(ctx, id) })
}

func (c *crashStore) Create(ctx context.Context, id, conv string, opts ...store.CreateOption) (store.Run, error) {
	return call(c, "Create", func() (store.Run, error) { return c.Store.Create(ctx, id, conv, opts...) })
}

func (c *crashStore) Get(ctx context.Context, id string) (store.Run, error) {
	return call(c, "Get", func() (store.Run, error) { return c.Store.Get(ctx, id) })
}

func (c *crashStore) List(ctx context.Context, conv string, p store.Page) (store.RunPage, error) {
	return call(c, "List", func() (store.RunPage, error) { return c.Store.List(ctx, conv, p) })
}

func (c *crashStore) Transition(ctx context.Context, id string, from, to store.Status, d store.Details) (store.Run, error) {
	return call(c, "Transition", func() (store.Run, error) { return c.Store.Transition(ctx, id, from, to, d) })
}

func (c *crashStore) ListResumable(ctx context.Context, now time.Time, limit int) ([]store.Run, error) {
	return call(c, "ListResumable", func() ([]store.Run, error) { return c.Store.ListResumable(ctx, now, limit) })
}

func (c *crashStore) ListSuspended(ctx context.Context, p store.Page) (store.RunPage, error) {
	return call(c, "ListSuspended", func() (store.RunPage, error) { return c.Store.ListSuspended(ctx, p) })
}

func (c *crashStore) Unfinished(ctx context.Context, conv string) (store.Run, error) {
	return call(c, "Unfinished", func() (store.Run, error) { return c.Store.Unfinished(ctx, conv) })
}

func (c *crashStore) Claim(ctx context.Context, id, owner string, ttl time.Duration) (store.Lease, error) {
	return call(c, "Claim", func() (store.Lease, error) { return c.Store.Claim(ctx, id, owner, ttl) })
}

func (c *crashStore) Renew(ctx context.Context, id, owner string, ttl time.Duration) (store.Lease, error) {
	return call(c, "Renew", func() (store.Lease, error) { return c.Store.Renew(ctx, id, owner, ttl) })
}

func (c *crashStore) Release(ctx context.Context, id, owner string) error {
	return call0(c, "Release", func() error { return c.Store.Release(ctx, id, owner) })
}

func (c *crashStore) Save(ctx context.Context, id string, ck flow.Checkpoint) error {
	return call0(c, "Save", func() error { return c.Store.Save(ctx, id, ck) })
}

func (c *crashStore) Load(ctx context.Context, id string) (flow.Checkpoint, error) {
	return call(c, "Load", func() (flow.Checkpoint, error) { return c.Store.Load(ctx, id) })
}

// crashAtNth crashes at the n-th call of op, which reaches the store if
// landed.
func crashAtNth(op string, n int, landed bool) func(string, int, int) (bool, bool) {
	return func(o string, k, _ int) (bool, bool) { return o == op && k == n, landed }
}

// lastDone returns the EventDone of evs, failing the test without one.
func lastDone(t *testing.T, evs []runner.Event) runner.Event {
	t.Helper()
	if len(evs) == 0 || evs[len(evs)-1].Kind != runner.EventDone {
		t.Fatalf("execution did not end with EventDone: %s", js(evs))
	}
	return evs[len(evs)-1]
}

func (w *world) touch(ctx context.Context, conv string) {
	tn, ok := tenancy.From(ctx)
	if !ok {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.touched == nil {
		w.touched = map[touch]context.Context{}
	}
	w.touched[touch{tn.ID(), conv}] = tenancy.With(context.Background(), tn)
}
