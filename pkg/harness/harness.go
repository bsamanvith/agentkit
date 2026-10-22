package harness

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"slices"
	"time"

	"github.com/bsamanvith/agentkit/pkg/clock"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/policy"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

// Harness runs the agent loop for one model and one set of tools. It holds no
// state between runs and is safe for concurrent use if the model and registry
// are, so one Harness can serve many conversations.
type Harness struct {
	model llm.Model
	tools tool.Registry
	cfg   config
}

// New returns a Harness that talks to model and calls tools from the
// registry. It fails with [ErrInvalidConfig] for a nil model or registry, or
// an option with an unusable value.
func New(model llm.Model, tools tool.Registry, opts ...Option) (*Harness, error) {
	if model == nil {
		return nil, fmt.Errorf("%w: model is nil", ErrInvalidConfig)
	}
	if tools == nil {
		return nil, fmt.Errorf("%w: tool registry is nil", ErrInvalidConfig)
	}
	cfg := config{maxTurns: defaultMaxTurns, maxParallel: defaultMaxParallel, clock: clock.Real(), interaction: Autonomous}
	for _, o := range opts {
		o(&cfg)
	}
	switch {
	case cfg.maxTurns < 1:
		return nil, fmt.Errorf("%w: max turns must be at least 1, got %d", ErrInvalidConfig, cfg.maxTurns)
	case cfg.maxTokens < 0:
		return nil, fmt.Errorf("%w: max total tokens must not be negative, got %d", ErrInvalidConfig, cfg.maxTokens)
	case cfg.request.MaxTokens < 0:
		return nil, fmt.Errorf("%w: output token limit must not be negative, got %d", ErrInvalidConfig, cfg.request.MaxTokens)
	case cfg.maxParallel < 1:
		return nil, fmt.Errorf("%w: max parallel tools must be at least 1, got %d", ErrInvalidConfig, cfg.maxParallel)
	case cfg.clock == nil:
		return nil, fmt.Errorf("%w: clock is nil", ErrInvalidConfig)
	case cfg.window.Tokens < 0 || cfg.window.Share < 0 || cfg.window.Share > 1:
		return nil, fmt.Errorf("%w: context window %+v", ErrInvalidConfig, cfg.window)
	case !cfg.interaction.Valid():
		return nil, fmt.Errorf("%w: interaction %q", ErrInvalidConfig, cfg.interaction)
	}
	if cfg.policy != nil {
		if err := cfg.policy.Validate(); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrInvalidConfig, err)
		}
	}
	for _, f := range cfg.before {
		if f == nil {
			return nil, fmt.Errorf("%w: BeforeTool hook is nil", ErrInvalidConfig)
		}
	}
	for _, f := range cfg.after {
		if f == nil {
			return nil, fmt.Errorf("%w: AfterTool hook is nil", ErrInvalidConfig)
		}
	}
	for _, f := range cfg.prepare {
		if f == nil {
			return nil, fmt.Errorf("%w: Prepare hook is nil", ErrInvalidConfig)
		}
	}
	return &Harness{model: model, tools: tools, cfg: cfg}, nil
}

// Run streams one run: it appends in.Message to in.Transcript and loops until
// the model answers. Events are delivered on the caller's goroutine, in the
// order described on [EventKind]. A run that fails ends the sequence with a
// [*RunError]; see the package documentation for what it carries.
//
// The run starts when the sequence is iterated, and iterating it again starts
// a new run. Stopping the iteration early cancels the model call and the
// running tools, waits for them to return, and leaves no goroutine behind.
// ctx cancels the run the same way, with the context's error as the cause.
func (h *Harness) Run(ctx context.Context, in Input) iter.Seq2[Event, error] {
	return func(yield func(Event, error) bool) {
		r := &run{h: h, yield: yield}
		r.exec(ctx, in)
	}
}

// Collect reads a run to its end and returns its [Result]. If the run fails,
// it returns the error together with the partial result (the transcript of the
// completed turns, usage and record), unlike [llm.Collect], so that the work
// is not lost. A run that suspends gives its result, with Result.Pending set,
// and a [*SuspendedError].
func Collect(seq iter.Seq2[Event, error]) (Result, error) {
	for ev, err := range seq {
		if err != nil {
			var re *RunError
			if errors.As(err, &re) {
				return re.result(), err
			}
			return Result{}, err
		}
		switch ev.Kind {
		case EventRunDone:
			return *ev.Result, nil
		case EventSuspended:
			return *ev.Result, &SuspendedError{Pending: ev.Result.Pending.Clone()}
		}
	}
	return Result{}, fmt.Errorf("harness: %w: the run ended without a final event", llm.ErrIncompleteStream)
}

// run is the state of one Run. Everything here is touched only by the
// goroutine that iterates the run; tool goroutines get their own slots.
type run struct {
	h       *Harness
	yield   func(Event, error) bool
	stopped bool // the consumer stopped iterating, or the error has been delivered

	transcript Transcript
	usage      llm.Usage
	turns      int
	rec        Record
	defs       map[string]tool.Definition // the listed tools, by name
	// policy decides the run's tool calls: the harness's, narrowed by the
	// input's; nil when neither has one.
	policy *policy.Policy
	// mode is the run's interaction: the harness's, narrowed by the input's.
	mode Interaction
	// resume is the pending turn the run finishes first, with its answers by
	// call; suspended is the turn the run suspends in.
	resume    *Pending
	answers   map[int]Answer
	suspended *Pending
}

// toolDef returns the definition of the named tool; for a tool that was not
// listed, an unclassified one.
func (r *run) toolDef(name string) tool.Definition {
	if d, ok := r.defs[name]; ok {
		return d
	}
	return tool.Definition{Name: name}
}

func (r *run) now() time.Time { return r.h.cfg.clock.Now() }

// emit delivers an event. It returns false once the consumer has stopped, and
// never calls yield again after that.
func (r *run) emit(ev Event) bool {
	if r.stopped {
		return false
	}
	if !r.yield(ev, nil) {
		r.stopped = true
		return false
	}
	return true
}

// fail ends the run with err.
func (r *run) fail(err error) {
	if r.stopped {
		return
	}
	r.stopped = true
	r.rec.FinishedAt = r.now()
	r.yield(Event{}, &RunError{
		Err:        err,
		transcript: r.transcript.Clone(),
		usage:      r.usage,
		turns:      r.turns,
		record:     r.rec.clone(),
	})
}

// finish ends a successful run.
func (r *run) finish() {
	r.rec.FinishedAt = r.now()
	res := Result{
		Transcript: r.transcript.Clone(),
		Usage:      r.usage,
		Turns:      r.turns,
		Record:     r.rec.clone(),
	}
	res.Final = res.Transcript[len(res.Transcript)-1].Clone() // not the transcript's own blocks
	r.emit(Event{Kind: EventRunDone, At: r.rec.FinishedAt, Result: &res})
}

// ctxOr returns the context's error if ctx is done, else err. A model or tool
// that stops because of cancellation reports the context's error, and the
// run's cause should be that error whatever it was wrapped in.
func ctxOr(ctx context.Context, err error) error {
	if cerr := ctx.Err(); cerr != nil {
		return cerr
	}
	return err
}

func (r *run) exec(ctx context.Context, in Input) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	r.rec.StartedAt = r.now()
	if err := r.begin(ctx, in); err != nil {
		r.fail(err)
		return
	}
	start := Event{Kind: EventRunStart, At: r.rec.StartedAt}
	if r.rec.Prompt != nil {
		p := r.rec.Prompt.clone()
		start.Prompt = &p
	}
	if !r.emit(start) {
		return
	}

	// Listed once per run: the definitions stay the same on every turn (and
	// on a resume), whatever a remote registry does meanwhile.
	defs, err := r.h.tools.List(ctx)
	if err != nil {
		r.fail(ctxOr(ctx, fmt.Errorf("list tools: %w", err)))
		return
	}
	r.defs = make(map[string]tool.Definition, len(defs))
	offered := defs[:0:0]
	for _, d := range defs {
		r.defs[d.Name] = d.Clone()
		if d.Interrupts && !r.mode.waits() {
			continue // nobody could answer it
		}
		offered = append(offered, d)
	}

	if r.resume != nil {
		err := r.finishPending(ctx)
		switch {
		case r.stopped:
			return
		case err != nil:
			r.fail(err)
			return
		case r.suspended != nil:
			r.suspend()
			return
		}
	}
	for {
		if err := ctx.Err(); err != nil {
			r.fail(err)
			return
		}
		if err := r.checkBudget(); err != nil {
			r.fail(err)
			return
		}
		done, err := r.turn(ctx, offered)
		if r.stopped {
			return
		}
		if err != nil {
			r.fail(err)
			return
		}
		if r.suspended != nil {
			r.suspend()
			return
		}
		if done {
			r.finish()
			return
		}
	}
}

// begin validates the input, runs the prepare hooks on a new message, and
// sets up the transcript, the policy, the interaction and the pending turn.
func (r *run) begin(ctx context.Context, in Input) error {
	if in.Interaction != "" && !in.Interaction.Valid() {
		return invalidInput("interaction %q", in.Interaction)
	}
	r.mode = NarrowInteraction(r.h.cfg.interaction, in.Interaction)
	r.policy = r.h.cfg.policy
	if p := in.ToolPolicy; p != nil {
		if err := p.Validate(); err != nil {
			return invalidInput("tool policy: %v", err)
		}
		narrowed := p.Clone()
		if r.policy != nil {
			narrowed = policy.Narrow(*r.policy, *p)
		}
		r.policy = &narrowed
	}
	t := in.Transcript.Clone()
	if err := t.Validate(); err != nil {
		return err
	}
	r.transcript = t
	endsWithUser := len(t) > 0 && t[len(t)-1].Role == llm.RoleUser

	if in.Pending == nil && len(in.Answers) > 0 {
		return invalidInput("answers without a pending turn")
	}
	if in.Pending != nil {
		if hasMessage(in.Message) {
			return invalidInput("a new message with a pending turn: answer the turn first")
		}
		p := in.Pending.Clone()
		if err := p.check(t); err != nil {
			return err
		}
		answers, err := p.checkAnswers(in.Answers)
		if err != nil {
			return err
		}
		r.resume, r.answers = &p, answers
		return nil
	}
	if !hasMessage(in.Message) {
		if !endsWithUser {
			return invalidInput("no message to answer: the transcript does not end with a user message and no new message was given")
		}
		return nil
	}
	if endsWithUser {
		return invalidInput("the transcript already ends with a user message; resume it without a new message")
	}
	// The new message is checked like every other: as part of the whole
	// transcript, so that what the model is sent is always one Validate accepts.
	m := in.Message.Clone()
	t = append(t, m)
	if err := t.Validate(); err != nil {
		return err
	}
	if len(r.h.cfg.prepare) > 0 {
		var added []llm.Block
		for _, f := range r.h.cfg.prepare {
			blocks, err := f(ctx, in.Message.Clone())
			if err != nil {
				return ctxOr(ctx, fmt.Errorf("prepare the message: %w", err))
			}
			added = append(added, blocks...)
		}
		if len(added) > 0 {
			m = llm.Message{Role: m.Role, Content: append(added, m.Content...)}.Clone() // no block a hook still holds
			t[len(t)-1] = m
			if err := t.Validate(); err != nil {
				return fmt.Errorf("prepare the message: %w", err)
			}
		}
	}
	at := in.At
	if at.IsZero() {
		at = r.now()
	}
	prompt := TimedMessage{Message: m.Clone(), At: at}
	r.rec.Prompt = &prompt
	r.transcript = t
	return nil
}

// checkBudget is called before every model call.
func (r *run) checkBudget() error {
	cfg := &r.h.cfg
	if r.turns >= cfg.maxTurns {
		return fmt.Errorf("%w: %d turns used, the limit is %d", ErrBudgetExceeded, r.turns, cfg.maxTurns)
	}
	if cfg.maxTokens > 0 && totalTokens(r.usage) >= cfg.maxTokens {
		return fmt.Errorf("%w: %d tokens used, the limit is %d", ErrBudgetExceeded, totalTokens(r.usage), cfg.maxTokens)
	}
	return nil
}

// turn makes one model call and handles its reply. It returns true when the
// run is complete. If it returns with r.stopped set, the consumer has gone.
func (r *run) turn(ctx context.Context, defs []tool.Definition) (done bool, err error) {
	r.turns++
	tr := TurnRecord{Turn: r.turns}
	// The model gets a view of the transcript it cannot append to, and it is
	// never modified once sent: only whole turns are added at the end.
	n := len(r.transcript)
	req := llm.Request{
		System:    r.h.cfg.system,
		Messages:  r.transcript[:n:n],
		Tools:     defs,
		MaxTokens: r.h.cfg.request.MaxTokens,
		Provider:  r.h.cfg.request.Provider,
	}

	tr.RequestedAt = r.now()
	var resp *llm.Response
	for ev, err := range r.h.model.Generate(ctx, req) {
		if err != nil {
			return false, ctxOr(ctx, fmt.Errorf("model call %d: %w", tr.Turn, err))
		}
		if tr.FirstEventAt.IsZero() {
			tr.FirstEventAt = r.now()
		}
		switch ev.Kind {
		case llm.EventTextDelta:
			if !r.emit(Event{Kind: EventTextDelta, Turn: tr.Turn, At: r.now(), Text: ev.Text, Block: ev.Index}) {
				return false, nil
			}
		case llm.EventMessageDone:
			resp = ev.Response
		}
		if resp != nil {
			break
		}
	}
	if resp == nil {
		return false, ctxOr(ctx, fmt.Errorf("%w: model call %d: %w", ErrUnexpectedStop, tr.Turn, llm.ErrIncompleteStream))
	}

	tr.DoneAt = r.now()
	tr.Usage, tr.Stop = resp.Usage, resp.Stop
	r.usage = r.usage.Add(resp.Usage)
	// The response belongs to the model; the transcript keeps its own copy.
	msg := resp.Message.Clone()
	uses := toolUses(msg)

	// A reply the transcript could not hold (Validate and JSON encoding would
	// reject it) is never committed, whatever the model stopped for.
	malformed := replyProblem(msg)
	if malformed != nil && (resp.Stop.Kind == llm.StopToolUse || resp.Stop.Kind == llm.StopEndTurn) {
		return false, fmt.Errorf("%w: %w", ErrUnexpectedStop, malformed)
	}

	switch resp.Stop.Kind {
	case llm.StopToolUse:
		if len(uses) == 0 {
			return false, fmt.Errorf("%w: the model stopped to use tools but called none", ErrUnexpectedStop)
		}
		return r.useTools(ctx, &tr, msg, uses)
	case llm.StopEndTurn:
		if len(uses) > 0 {
			return false, fmt.Errorf("%w: the model called tools but ended its turn", ErrUnexpectedStop)
		}
		r.commit(&tr, TimedMessage{Message: msg, At: tr.DoneAt})
		return true, nil
	}

	// The model stopped for a reason that ends the run. Its reply is part of
	// the conversation and is kept, unless it calls tools that nothing will
	// answer (that would leave a transcript no model accepts) or is malformed.
	var cause error
	switch resp.Stop.Kind {
	case llm.StopMaxTokens:
		cause = ErrMaxTokens
	case llm.StopRefusal:
		cause = &RefusalError{Category: resp.Stop.Category, Explanation: resp.Stop.Explanation}
	default:
		cause = fmt.Errorf("%w: stop reason %q %q", ErrUnexpectedStop, resp.Stop.Kind, resp.Stop.Raw)
	}
	if len(uses) == 0 && malformed == nil {
		r.commit(&tr, TimedMessage{Message: msg, At: tr.DoneAt})
	}
	return false, cause
}

// useTools runs the model's tool calls and, once they are all done, adds the
// reply and the results to the transcript as one turn.
func (r *run) useTools(ctx context.Context, tr *TurnRecord, msg llm.Message, uses []llm.ToolUse) (bool, error) {
	jobs := make([]job, len(uses))
	for i, u := range uses {
		jobs[i] = job{call: i, use: u}
	}
	calls, err := r.runTools(ctx, tr.Turn, jobs)
	if r.stopped || err != nil {
		return false, err
	}
	blocks := make([]llm.Block, 0, len(calls))
	tr.Tools = make([]ToolRecord, len(calls))
	var waiting []Interrupt
	for i, c := range calls {
		tr.Tools[i] = c.rec
		if c.interrupt != nil {
			waiting = append(waiting, *c.interrupt)
			continue
		}
		blocks = append(blocks, c.result)
	}
	if len(waiting) > 0 {
		tr.Messages = []TimedMessage{{Message: msg, At: tr.DoneAt}}
		r.suspended = &Pending{Turn: tr.clone(), Done: llm.User(append([]llm.Block(nil), blocks...)...), Interrupts: waiting}
		return false, nil
	}
	r.commit(tr,
		TimedMessage{Message: msg, At: tr.DoneAt},
		TimedMessage{Message: llm.User(blocks...), At: r.now()},
	)
	return false, nil
}

// finishPending finishes the pending turn the run resumes, as turn 0: the
// calls that waited get their answers (an approved one runs), and the turn is
// committed. A call that waits again (an approved tool that asks a person)
// suspends the run on the same turn.
func (r *run) finishPending(ctx context.Context) error {
	p := r.resume
	uses := toolUses(p.Reply())
	jobs := make([]job, len(p.Interrupts))
	for k, in := range p.Interrupts {
		a := r.answers[in.Call]
		jobs[k] = job{call: in.Call, use: uses[in.Call], interrupt: &in, answer: &a}
	}
	calls, err := r.runTools(ctx, 0, jobs)
	if r.stopped || err != nil {
		return err
	}
	tr := p.Turn.clone()
	got := map[int]llm.ToolResult{}
	var waiting []Interrupt
	for k, c := range calls {
		call := jobs[k].call
		tr.Tools[call] = c.rec
		if c.interrupt != nil {
			waiting = append(waiting, *c.interrupt)
			continue
		}
		got[call] = c.result
	}
	if len(waiting) > 0 {
		var done []llm.Block
		for _, b := range p.results(got).Content {
			res := b.(llm.ToolResult)
			if !slices.ContainsFunc(waiting, func(in Interrupt) bool { return in.ID == res.ToolUseID }) {
				done = append(done, res)
			}
		}
		r.suspended = &Pending{Turn: tr, Done: llm.User(done...), Interrupts: waiting}
		return nil
	}
	reply := tr.Messages[0]
	tr.Turn, tr.Messages = 0, nil
	r.commit(&tr, reply, TimedMessage{Message: p.results(got), At: r.now()})
	return nil
}

// suspend ends a run whose turn waits for people.
func (r *run) suspend() {
	r.rec.FinishedAt = r.now()
	p := r.suspended.Clone()
	res := Result{
		Transcript: r.transcript.Clone(),
		Usage:      r.usage,
		Turns:      r.turns,
		Record:     r.rec.clone(),
		Pending:    &p,
	}
	r.emit(Event{Kind: EventSuspended, At: r.rec.FinishedAt, Result: &res})
}

// commit adds a finished turn's messages to the transcript and announces it.
// The messages must be copies the transcript can keep.
func (r *run) commit(tr *TurnRecord, msgs ...TimedMessage) {
	for _, m := range msgs {
		r.transcript = append(r.transcript, m.Message)
		tr.Messages = append(tr.Messages, m.clone())
	}
	r.rec.Turns = append(r.rec.Turns, *tr)
	ev := tr.clone()
	r.emit(Event{Kind: EventTurnDone, Turn: tr.Turn, At: r.now(), TurnRecord: &ev})
}
