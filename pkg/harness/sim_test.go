package harness_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/llm/llmtest"
	"github.com/bsamanvith/agentkit/pkg/policy"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

// The simulation runs the harness on scripts a seed turns into: what the model
// says (including replies no real model should send), which tools fail, what
// the hooks do, which budgets apply, whether the conversation starts from an
// earlier transcript, and what the consumer does with the events (read them,
// stop, panic, cancel the context, or scribble over everything it is given).
// Every run goes through rig.drive, so every invariant in invariants_test.go
// is checked on every combination.
//
// Two further properties are checked here, because they compare runs:
//   - a consumer, hook and registry that scribble over every value they are
//     handed change nothing: the same seed gives the same transcript, the same
//     requests and the same outcome as with well-behaved ones (and the race
//     detector sees no shared writes);
//   - however and wherever a run ends early, no tool is still running when Run
//     has returned, and no model call is made after a cancellation.
//
// A failure names its seed; TestSimulatedRuns/seed=N reruns it.

type behavior int

const (
	bNormal       behavior = iota
	bCancelAt              // cancel the context at event k
	bStopAt                // stop iterating at event k
	bPanicAt               // panic in the consumer at event k
	bCancelOnTool          // cancel at the first tool start; the "block" tool may be called
	bStopOnTool            // stop at the first tool start; the "block" tool may be called
)

type scenario struct {
	replies   []llmtest.Reply
	opts      []harness.Option
	lim       limits
	in        harness.Input
	beh       behavior
	k         int
	scribbler bool
	// mode is the harness's interaction; seed drives the answers to a run
	// that suspends.
	mode harness.Interaction
	seed uint64
}

type simInput struct {
	N int `json:"n"`
}

var simOpaque = llm.Opaque{Provider: "sim", Data: json.RawMessage("{ \"s\" : \"<&>é\" }")}

func idHash(id string) int {
	h := 0
	for _, c := range id {
		h = h*31 + int(c)
	}
	return h & 0xffff
}

func scribbleBytes(b []byte) {
	for i := range b {
		b[i] = 'X'
	}
}

func genScenario(seed uint64, scribbler bool) scenario {
	rng := rand.New(rand.NewPCG(seed, 0x9e3779b97f4a7c15))
	sc := scenario{scribbler: scribbler}

	// How the consumer behaves.
	switch x := rng.IntN(100); {
	case x < 50:
		sc.beh = bNormal
	case x < 62:
		sc.beh, sc.k = bCancelAt, rng.IntN(12)
	case x < 72:
		sc.beh, sc.k = bStopAt, rng.IntN(12)
	case x < 80:
		sc.beh, sc.k = bPanicAt, rng.IntN(12)
	case x < 90:
		sc.beh = bCancelOnTool
	default:
		sc.beh = bStopOnTool
	}
	allowBlock := sc.beh == bCancelOnTool || sc.beh == bStopOnTool

	// What the model says.
	n := 1 + rng.IntN(5)
	for turn := 0; turn < n; turn++ {
		sc.replies = append(sc.replies, genReply(rng, turn, allowBlock))
	}
	final := llmtest.Text("final answer").WithUsage(genUsage(rng))
	sc.replies = append(sc.replies, final)

	// What the harness is configured with.
	if x := rng.IntN(4); x > 0 {
		sc.opts = append(sc.opts, harness.WithMaxParallelTools(x))
	}
	if rng.IntN(3) == 0 {
		sc.lim.maxTurns = 1 + rng.IntN(6)
		sc.opts = append(sc.opts, harness.WithMaxTurns(sc.lim.maxTurns))
	}
	if rng.IntN(3) == 0 {
		sc.lim.maxTokens = 20 + rng.IntN(200)
		sc.opts = append(sc.opts, harness.WithMaxTotalTokens(sc.lim.maxTokens))
	}
	if rng.IntN(3) == 0 {
		sc.opts = append(sc.opts,
			harness.WithSystem("sim"),
			harness.WithRequest(harness.RequestOptions{MaxTokens: 100 + rng.IntN(100), Provider: "sim-provider"}))
	}
	if rng.IntN(2) == 0 {
		sc.opts = append(sc.opts, harness.WithBeforeTool(func(_ context.Context, c llm.ToolUse) error {
			if scribbler {
				scribbleBytes(c.Input)
			}
			if idHash(c.ID)%4 == 0 {
				return errors.New("denied by policy")
			}
			return nil
		}))
	}
	if rng.IntN(2) == 0 {
		sc.opts = append(sc.opts, harness.WithAfterTool(func(_ context.Context, c llm.ToolUse, r llm.ToolResult) llm.ToolResult {
			if scribbler {
				scribbleBytes(c.Input)
			}
			if idHash(c.ID)%5 == 0 {
				return llm.ToolResult{Content: "[redacted]", IsError: idHash(c.ID)%2 == 0}
			}
			return r
		}))
	}

	// Deferred tools: fail and boom wait for a search, or for a call of ok,
	// which loads them as a loader.
	if rng.IntN(3) == 0 {
		sc.opts = append(sc.opts, harness.WithDeferLoading(func(name string) bool { return name == "fail" || name == "boom" }))
		if rng.IntN(2) == 0 {
			sc.opts = append(sc.opts, harness.WithNativeToolSearch())
		}
		if rng.IntN(2) == 0 {
			sc.opts = append(sc.opts, harness.WithToolLoader("ok", func(json.RawMessage) []string { return []string{"f*", "nope"} }))
		}
	}

	// The tool policy: the sim's tools are unclassified (write/medium).
	if rng.IntN(3) == 0 {
		sc.opts = append(sc.opts, harness.WithToolPolicy(policy.Policy{Rules: []policy.Rule{
			{Match: policy.Match{Tools: []string{"fail"}}, Action: policy.Deny},
			{Match: policy.Match{Tools: []string{"ok"}}, Action: policy.Flag},
			{Match: policy.Match{Tools: []string{"boom"}}, Action: policy.Approve},
		}, Default: policy.Allow}, nil))
	}
	var narrow *policy.Policy
	if rng.IntN(5) == 0 {
		p := policy.MustPreset(policy.Presets()[rng.IntN(len(policy.Presets()))])
		narrow = &p
	}

	// What the conversation starts from.
	// Interrupts: the mode decides whether "ask" and the policy's approvals
	// wait, and the input may narrow it.
	sc.seed = seed
	modes := harness.Interactions()
	sc.mode = modes[rng.IntN(len(modes))]
	sc.opts = append(sc.opts, harness.WithInteraction(sc.mode))
	var inMode harness.Interaction
	if rng.IntN(4) == 0 {
		inMode = modes[rng.IntN(len(modes))]
	}
	defer func() { sc.in.ToolPolicy, sc.in.Interaction = narrow, inMode }()
	q := harness.UserText("question")
	earlier := harness.Transcript{q, llm.Assistant(simOpaque, llm.Text{Text: "earlier answer"})}
	switch rng.IntN(4) {
	case 0:
		sc.in = harness.Input{Message: q}
	case 1:
		sc.in = harness.Input{Message: harness.UserText("follow-up"), Transcript: earlier}
	case 2:
		sc.in = harness.Input{Transcript: harness.Transcript{
			q, llm.Assistant(simOpaque, use("p1", "ok", `{"n":7}`)), llm.User(llm.ToolResult{ToolUseID: "p1", Content: "7"}),
		}}
	default:
		sc.in = harness.Input{Message: q, At: clockStart.Add(-time.Hour)}
	}
	return sc
}

func genUsage(rng *rand.Rand) llm.Usage {
	return llm.Usage{
		InputTokens: 1 + rng.IntN(30), OutputTokens: 1 + rng.IntN(10),
		CacheReadTokens: rng.IntN(20), CacheWriteTokens: rng.IntN(5),
	}
}

func genCalls(rng *rand.Rand, turn int, allowBlock bool) []llm.Block {
	var blocks []llm.Block
	if rng.IntN(3) == 0 {
		blocks = append(blocks, simOpaque)
	}
	if rng.IntN(3) == 0 {
		blocks = append(blocks, llm.Text{Text: fmt.Sprintf("thinking about turn %d", turn)})
	}
	for i := range 1 + rng.IntN(5) {
		id := fmt.Sprintf("t%dc%d", turn, i)
		if rng.IntN(10) == 0 {
			q := []string{"fail", "boom", "double", ""}[rng.IntN(4)]
			blocks = append(blocks, use(id, harness.SearchTool, fmt.Sprintf(`{"query":%q}`, q)))
			continue
		}
		if rng.IntN(8) == 0 {
			blocks = append(blocks, use(id, "ask", fmt.Sprintf(`{"n":%d}`, rng.IntN(3))))
			continue
		}
		switch x := rng.IntN(10); {
		case x < 5:
			blocks = append(blocks, use(id, "ok", fmt.Sprintf(`{"n":%d}`, rng.IntN(100))))
		case x == 5:
			blocks = append(blocks, use(id, "fail", `{}`))
		case x == 6:
			blocks = append(blocks, use(id, "boom", `{}`))
		case x == 7:
			blocks = append(blocks, use(id, "no_such_tool", `{}`))
		case x == 8:
			blocks = append(blocks, use(id, "ok", `{"n":"not a number"}`))
		default:
			if allowBlock {
				blocks = append(blocks, use(id, "block", `{}`))
			} else {
				blocks = append(blocks, use(id, "ok", `{"n":1,"unknown":true}`))
			}
		}
	}
	return blocks
}

// genReply returns a reply for one turn: usually tool calls, sometimes
// something a real model would not send, or that ends the run.
func genReply(rng *rand.Rand, turn int, allowBlock bool) llmtest.Reply {
	u := genUsage(rng)
	calls := genCalls(rng, turn, allowBlock)
	var bad llm.ToolUse
	switch x := rng.IntN(100); {
	case x < 62:
		return reply(llm.StopToolUse, u, calls...)
	case x < 66:
		return reply(llm.StopEndTurn, u, llm.Text{Text: "early answer"})
	case x < 68:
		return reply(llm.StopMaxTokens, u, llm.Text{Text: "cut off"})
	case x < 70:
		return reply(llm.StopMaxTokens, u, append([]llm.Block{llm.Text{Text: "cut off"}}, calls[len(calls)-1])...)
	case x < 72:
		r := reply(llm.StopRefusal, u, llm.Text{Text: "no"})
		r.Response.Stop = llm.StopReason{Kind: llm.StopRefusal, Category: "sim", Explanation: "because"}
		return r
	case x < 74:
		r := reply(llm.StopOther, u, llm.Text{Text: "paused"})
		r.Response.Stop = llm.StopReason{Kind: llm.StopOther, Raw: "pause_turn"}
		return r
	case x < 76:
		return reply(llm.StopToolUse, u, llm.Text{Text: "meant to call a tool"})
	case x < 78:
		return reply(llm.StopEndTurn, u, calls...)
	case x < 80:
		bad = use("", "ok", `{"n":1}`)
		return reply(llm.StopToolUse, u, bad)
	case x < 82:
		bad = use("t-noname", "", `{}`)
		return reply(llm.StopToolUse, u, bad)
	case x < 84:
		return reply(llm.StopToolUse, u, use("dup", "ok", `{"n":1}`), use("dup", "ok", `{"n":2}`))
	case x < 86:
		return reply(llm.StopToolUse, u, use("t-badjson", "ok", `{"n":`))
	case x < 88:
		r := reply(llm.StopEndTurn, u, llm.Text{Text: "from the user?"})
		r.Response.Message.Role = llm.RoleUser
		return r
	case x < 90:
		return reply(llm.StopEndTurn, u, llm.ToolResult{ToolUseID: "x", Content: "a result in a reply"})
	case x < 92:
		return reply(llm.StopEndTurn, u, llm.Opaque{Provider: "", Data: json.RawMessage(`{}`)})
	case x < 94:
		return llmtest.Fail(fmt.Errorf("%w: sim", llm.ErrOverloaded))
	default:
		return reply(llm.StopToolUse, u, calls...)
	}
}

// simTools are the tools of every scenario. running counts calls in progress.
func simTools(t testing.TB, running *atomic.Int32) []tool.Tool {
	count := func() func() {
		running.Add(1)
		return func() { running.Add(-1) }
	}
	ok, err := tool.Func("ok", "Double n.", func(_ context.Context, in simInput) (int, error) {
		defer count()()
		return in.N * 2, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// ask asks with n choices (none: any text).
	ask, err := tool.Func("ask", "Ask the user.", func(_ context.Context, in simInput) (string, error) {
		defer count()()
		return "", tool.Interrupt("which?", []string{"a", "b"}[:min(max(in.N, 0), 2)]...)
	}, tool.WithMetadata(tool.Metadata{Interrupts: true}))
	if err != nil {
		t.Fatal(err)
	}
	return []tool.Tool{
		ok, ask,
		fn(t, "fail", func(context.Context) (string, error) { defer count()(); return "", errors.New("sim failure") }),
		fn(t, "boom", func(context.Context) (string, error) { defer count()(); panic("sim panic") }),
		fn(t, "block", func(ctx context.Context) (string, error) {
			defer count()()
			<-ctx.Done()
			<-time.After(10 * time.Millisecond) // slow cleanup, see TestConsumerStopCancelsEverything
			return "", ctx.Err()
		}),
	}
}

// scribblingRegistry overwrites the input of every call after the call. A
// registry has no business doing that, which is the point: the harness must not
// hand it memory that belongs to the transcript.
type scribblingRegistry struct{ tool.Registry }

func (s scribblingRegistry) Invoke(ctx context.Context, name string, in json.RawMessage) (json.RawMessage, error) {
	out, err := s.Registry.Invoke(ctx, name, in)
	scribbleBytes(in)
	return out, err
}

func scribbleMessage(m *llm.Message) {
	for i, b := range m.Content {
		switch b := b.(type) {
		case llm.ToolUse:
			scribbleBytes(b.Input)
		case llm.Opaque:
			scribbleBytes(b.Data)
		}
		m.Content[i] = llm.Text{Text: "X"}
	}
}

func scribbleEvent(ev harness.Event) {
	scribbleBytes(ev.ToolUse.Input)
	if ev.Prompt != nil {
		scribbleMessage(&ev.Prompt.Message)
	}
	if ev.TurnRecord != nil {
		for i := range ev.TurnRecord.Messages {
			scribbleMessage(&ev.TurnRecord.Messages[i].Message)
		}
		for i := range ev.TurnRecord.Tools {
			ev.TurnRecord.Tools[i].Name = "X"
		}
	}
}

type simResult struct {
	o        observation
	snap     []llm.Request
	class    string
	usage    llm.Usage
	endState []byte // the transcript as the run ended, to compare runs
	resumes  int    // runs that finished a pending turn
}

func classify(err error) string {
	switch {
	case err == nil:
		return "ok"
	case errors.Is(err, harness.ErrBudgetExceeded):
		return "budget"
	case errors.Is(err, harness.ErrMaxTokens):
		return "max_tokens"
	case errors.Is(err, harness.ErrRefused):
		return "refused"
	case errors.Is(err, harness.ErrUnexpectedStop):
		return "unexpected_stop"
	case errors.Is(err, harness.ErrInvalidInput):
		return "invalid_input"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, llm.ErrOverloaded):
		return "model_error"
	}
	return "other: " + err.Error()
}

func simulate(t *testing.T, seed uint64, scribbler bool) simResult {
	t.Helper()
	sc := genScenario(seed, scribbler)
	var running atomic.Int32
	reg := newRegistry(t, simTools(t, &running)...)
	if scribbler {
		reg = scribblingRegistry{reg}
	}
	fake := llmtest.NewFake(t, sc.replies...)
	h := newHarness(t, fake, reg, sc.opts...)
	h.lim = sc.lim

	ctx, cancelTimeout := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancelTimeout()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	events, requestsAtCancel := 0, -1
	cancelNow := func() {
		if requestsAtCancel < 0 {
			requestsAtCancel = len(fake.Requests())
		}
		cancel()
	}
	o := h.drive(ctx, t, sc.in, func(ev harness.Event) bool {
		n := events
		events++
		if scribbler && ev.Kind != harness.EventRunDone {
			scribbleEvent(ev)
		}
		switch sc.beh {
		case bCancelAt:
			if n == sc.k {
				cancelNow()
			}
		case bStopAt:
			return n == sc.k
		case bPanicAt:
			if n == sc.k {
				panic("consumer bug")
			}
		case bCancelOnTool:
			if ev.Kind == harness.EventToolStart {
				cancelNow()
			}
		case bStopOnTool:
			return ev.Kind == harness.EventToolStart
		}
		return false
	})

	// However the run ended, Run has returned, so no tool may still be running
	// and, once cancelled, the model is not called again.
	if n := running.Load(); n != 0 {
		t.Errorf("seed %d: %d tool calls still running after Run returned", seed, n)
	}
	if requestsAtCancel >= 0 {
		if got := len(fake.Requests()); got != requestsAtCancel {
			t.Errorf("seed %d: %d model calls after the context was cancelled", seed, got-requestsAtCancel)
		}
		if o.complete && o.err != nil && !errors.Is(o.err, context.Canceled) && !errors.Is(o.err, harness.ErrMaxTokens) &&
			!errors.Is(o.err, harness.ErrRefused) && !errors.Is(o.err, harness.ErrUnexpectedStop) {
			t.Errorf("seed %d: a cancelled run ended with %v", seed, o.err)
		}
	}
	switch {
	case sc.beh == bPanicAt && o.panicked == nil && !o.complete:
		t.Errorf("seed %d: the consumer panicked but the run neither ended nor reported it", seed)
	case sc.beh != bPanicAt && o.panicked != nil:
		t.Errorf("seed %d: unexpected panic %v", seed, o.panicked)
	}

	// A run that suspended is answered and resumed, from its saved pending
	// turn, until it ends otherwise; a bad answer comes first now and then,
	// and is refused without a call.
	arng := rand.New(rand.NewPCG(seed, 7))
	resumes := 0
	for ; o.suspended && resumes < 8; resumes++ {
		p := saved(t, o.res.Pending)
		var answers []harness.Answer
		for _, in := range p.Interrupts {
			a := harness.Answer{ID: in.ID}
			switch {
			case in.Kind == harness.InterruptApproval:
				a.Approved, a.Reason, a.By = arng.IntN(2) == 0, "sim", "tester"
			case len(in.Choices) > 0:
				a.Text = in.Choices[arng.IntN(len(in.Choices))]
			default:
				a.Text = fmt.Sprintf("free text %d", arng.IntN(10))
			}
			answers = append(answers, a)
		}
		in := harness.Input{Transcript: o.res.Transcript, Pending: p, Answers: answers, ToolPolicy: sc.in.ToolPolicy, Interaction: sc.in.Interaction}
		if arng.IntN(4) == 0 {
			bad := in
			bad.Answers = append(slices.Clone(answers), answers[0]) // answered twice
			if bo := h.drive(ctx, t, bad, nil); !errors.Is(bo.err, harness.ErrInvalidInput) {
				t.Errorf("seed %d: a doubled answer gave %v", seed, bo.err)
			}
		}
		o = h.drive(ctx, t, in, func(ev harness.Event) bool {
			if scribbler && ev.Kind != harness.EventRunDone && ev.Kind != harness.EventSuspended {
				scribbleEvent(ev)
			}
			return false
		})
		if n := running.Load(); n != 0 {
			t.Errorf("seed %d: %d tool calls still running after a resumed run returned", seed, n)
		}
	}

	res := simResult{o: o, class: classify(o.err), usage: o.res.Usage, resumes: resumes}
	if o.suspended {
		res.class = "suspended"
	}
	_, res.snap = h.rec.requests(0)
	if o.complete {
		res.endState = mustMarshal(t, o.res.Transcript)
		checkNoAliasing(t, seed, o)
	}
	return res
}

// checkNoAliasing changes everything the caller got back from a finished run
// except the transcript itself, and checks the transcript did not change.
func checkNoAliasing(t *testing.T, seed uint64, o observation) {
	t.Helper()
	before := mustMarshal(t, o.res.Transcript)
	scribbleMessage(&o.res.Final)
	for i := range o.res.Record.Turns {
		for j := range o.res.Record.Turns[i].Messages {
			scribbleMessage(&o.res.Record.Turns[i].Messages[j].Message)
		}
	}
	if o.res.Record.Prompt != nil {
		scribbleMessage(&o.res.Record.Prompt.Message)
	}
	if string(before) != string(mustMarshal(t, o.res.Transcript)) {
		t.Errorf("seed %d: changing Final or the record changed the transcript", seed)
	}
	var re *harness.RunError
	if errors.As(o.err, &re) {
		first := re.Transcript()
		for i := range first {
			scribbleMessage(&first[i])
		}
		diff(t, "RunError.Transcript after a copy was changed", o.res.Transcript, re.Transcript())
	}
}

func simulateSeed(t *testing.T, seed uint64) (resumes int) {
	t.Helper()
	clean := simulate(t, seed, false)
	if sc := genScenario(seed, false); sc.beh != bNormal {
		return clean.resumes // where the run stops depends on the order tools finish in
	}
	// The same run with a consumer, hooks and registry that scribble.
	messy := simulate(t, seed, true)
	if clean.class != messy.class || clean.usage != messy.usage || string(clean.endState) != string(messy.endState) {
		t.Errorf("seed %d: scribbling over values the harness handed out changed the outcome: %s vs %s",
			seed, clean.class, messy.class)
	}
	diff(t, fmt.Sprintf("seed %d: requests with and without scribbling", seed), clean.snap, messy.snap)
	return clean.resumes
}

func TestSimulatedRuns(t *testing.T) {
	leakcheck(t)
	resumed := 0
	for seed := uint64(1); seed <= 250; seed++ {
		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
			if simulateSeed(t, seed) > 0 {
				resumed++
			}
		})
	}
	// The simulation must keep covering interrupts.
	if resumed < 10 {
		t.Errorf("only %d of 250 seeds suspended and resumed", resumed)
	}
	t.Logf("%d seeds suspended and resumed", resumed)
}

func FuzzSimulatedRun(f *testing.F) {
	for _, seed := range []uint64{0, 1, 2, 3, 42, 1 << 40} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, seed uint64) { simulateSeed(t, seed) })
}
