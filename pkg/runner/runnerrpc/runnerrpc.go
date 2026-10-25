// Package runnerrpc puts agents on the wire: [Register] serves named
// [runner.Agent] values (harnesses: a model and its tools) on an
// [rpc.Server], and a [Client]'s agents implement runner.Agent by calling
// them. The runner (pkg/runner) keeps a run's log, lease and recovery where
// the store is; only the turn loop moves, so an agent API can run its agents
// in a runner container, and a runner pool (E20) can be one.
//
// A remote agent streams the same events as the local one, and fails with
// the same errors, and suspends the same way: a run whose calls wait for
// people ([harness.EventSuspended]) carries its pending turn, and a resume
// sends the pending turn and its answers back. A failed run is a [*harness.RunError] with what the run
// had done, matching the same harness and llm errors (llm.IsRetryable
// included), and a refusal is a [*harness.RefusalError]. A runner that cannot
// be reached, or goes away mid-run, fails the run as [llm.ErrOverloaded]
// (and [rpc.ErrUnavailable]), so it is retried. What a cut run had spent is
// lost with the connection.
//
// A server's agent that can compact ([runner.Compactor], as a harness can)
// does so for the client: a client's agent is a runner.Compactor whose
// Compact calls the server, and whose ShouldCompact uses the context window
// given to [Client.Agent], which the client must know itself.
//
// Besides the agents it was given, a server may build agents from their
// definitions ([WithStored]): an agent API's stored agents (T075, T132),
// which it does not build itself. The client sends the definition with each
// run ([Client.StoredAgent]), so the server keeps nothing it could not build
// again; it checks one first with [Client.CheckStored].
package runnerrpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/policy"
	"github.com/bsamanvith/agentkit/pkg/rpc"
	"github.com/bsamanvith/agentkit/pkg/runner"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

// Service is the service's name: a component serving it mounts its server at
// rpc.PathPrefix + Service + "/".
const Service = "runner"

// ErrNoAgent means the server runs no agent of that name.
var ErrNoAgent = errors.New("agent not run here")

// ErrNoStored means the server builds no agents from definitions: it was
// registered without [WithStored].
var ErrNoStored = errors.New("stored agents not run here")

// ErrInvalidDefinition means the server could not build an agent from its
// definition. The error is a [*DefinitionError], naming the fields.
var ErrInvalidDefinition = errors.New("invalid agent definition")

// DefinitionError is a definition a [StoredBuilder] refuses, by field. It
// matches [ErrInvalidDefinition].
type DefinitionError struct {
	Fields []FieldError `json:"fields"`
}

// FieldError is one problem of a definition: the field it is in ("" for the
// definition as a whole), and what is wrong with it.
type FieldError struct {
	Field   string `json:"field,omitempty"`
	Message string `json:"message"`
}

func (e *DefinitionError) Error() string {
	var b strings.Builder
	b.WriteString(ErrInvalidDefinition.Error())
	for i, f := range e.Fields {
		sep := ": "
		if i > 0 {
			sep = "; "
		}
		b.WriteString(sep)
		if f.Field != "" {
			b.WriteString(f.Field + ": ")
		}
		b.WriteString(f.Message)
	}
	return b.String()
}

// Is matches [ErrInvalidDefinition].
func (e *DefinitionError) Is(target error) bool { return target == ErrInvalidDefinition }

// StoredBuilder builds the agent of a definition: name's, a JSON object
// in the shape the server's caller saves. It refuses one it cannot build
// with a [*DefinitionError]; any other error refuses it as a whole. It is
// called for every run of a stored agent, so it should keep what it built
// (a definition never changes once saved: key by its bytes). It must not
// keep or change definition itself.
type StoredBuilder func(ctx context.Context, name string, definition json.RawMessage) (runner.Agent, error)

// Option configures [Register].
type Option func(*options)

type options struct{ stored StoredBuilder }

// WithStored makes the server run stored agents too, building each from the
// definition its run carries with build.
func WithStored(build StoredBuilder) Option { return func(o *options) { o.stored = build } }

// The service's methods.
var (
	agentsMethod = rpc.Unary[struct{}, agentsResp]{Name: Service + "/Agents"}
	runMethod    = rpc.Stream[runReq, event]{Name: Service + "/Run", Errors: codes}
	// policyRunMethod is Run for a run with a tool policy. It is its own
	// method so that a runner older than tool policies, which would ignore
	// the field and run unrestricted, fails the run with
	// [rpc.ErrUnknownMethod] instead: a policy fails closed.
	policyRunMethod = rpc.Stream[runReq, event]{Name: Service + "/RunWithPolicy", Errors: codes}
	// interruptRunMethod is Run for a run that may wait for people (its
	// interaction is interactive or supervised) or that finishes a pending
	// turn with its answers. A runner older than interrupts would run the
	// approvals' calls as denied and drop the answers: it fails the run with
	// [rpc.ErrUnknownMethod] instead.
	interruptRunMethod = rpc.Stream[runReq, event]{Name: Service + "/RunWithInterrupts", Errors: codes}
	// toolsRunMethod is Run for a run that chooses its tools (toolsets,
	// tool search). A runner older than it fails the run rather than offer
	// the toolsets the run left out.
	toolsRunMethod = rpc.Stream[runReq, event]{Name: Service + "/RunWithTools", Errors: codes}
	// modelRunMethod is Run on a model other than the agent's default
	// (T125). It is the newest method, so a runner that serves it serves
	// every other one: a run on another model is always sent as it, and a
	// runner older than models, which would run the default instead,
	// fails the run with [rpc.ErrUnknownMethod].
	modelRunMethod = rpc.Stream[runReq, event]{Name: Service + "/RunWithModel", Errors: codes}
	// storedRunMethod is Run of a stored agent, its definition in the
	// request (T132). It is newer than every other, so a stored agent's
	// run is always sent as it, and a runner older than it fails the run
	// rather than run a config agent of the same name.
	storedRunMethod = rpc.Stream[runReq, event]{Name: Service + "/RunStored", Errors: codes}
	// checkStoredMethod builds a stored agent's definition, to check it.
	checkStoredMethod = rpc.Unary[checkReq, struct{}]{Name: Service + "/CheckStored", Errors: codes}
	compactMethod     = rpc.Unary[compactReq, harness.Summary]{Name: Service + "/Compact", Errors: codes}
)

// ErrNoModel means the server's agent does not run the model a run names:
// it is not a [ModelAgent], or its ForModel refused the model.
var ErrNoModel = errors.New("model not run here")

// ModelAgent is a served agent that runs on other models than its
// default: ForModel returns it as it runs on model ("<provider>/<id>"),
// failing for one it does not allow. A run that names a model runs on
// what ForModel returns; one that names none runs the agent itself.
type ModelAgent interface {
	runner.Agent
	ForModel(ctx context.Context, model string) (runner.Agent, error)
}

// ErrCannotCompact means the server's agent cannot compact (it is not a
// [runner.Compactor]).
var ErrCannotCompact = errors.New("agent cannot compact")

// codes are the errors a run's caller tells apart: the harness's, the
// model's (retries depend on them) and this package's.
var codes = []rpc.Code{
	{Name: "runner.no_agent", Err: ErrNoAgent},
	{Name: "runner.cannot_compact", Err: ErrCannotCompact},
	{Name: "runner.no_model", Err: ErrNoModel},
	{Name: "runner.no_stored", Err: ErrNoStored},
	{Name: "runner.invalid_definition", Err: ErrInvalidDefinition},
	{Name: "harness.invalid_input", Err: harness.ErrInvalidInput},
	{Name: "harness.invalid_config", Err: harness.ErrInvalidConfig},
	{Name: "harness.budget_exceeded", Err: harness.ErrBudgetExceeded},
	{Name: "harness.max_tokens", Err: harness.ErrMaxTokens},
	{Name: "harness.refused", Err: harness.ErrRefused},
	{Name: "harness.unexpected_stop", Err: harness.ErrUnexpectedStop},
	{Name: "llm.rate_limited", Err: llm.ErrRateLimited},
	{Name: "llm.overloaded", Err: llm.ErrOverloaded},
	{Name: "llm.invalid_request", Err: llm.ErrInvalidRequest},
	{Name: "llm.auth", Err: llm.ErrAuth},
	{Name: "llm.context_too_long", Err: llm.ErrContextTooLong},
}

type compactReq struct {
	Agent   string        `json:"agent"`
	History []llm.Message `json:"history"`
	// Definition is a stored agent's (runReq.Definition).
	Definition json.RawMessage `json:"definition,omitempty"`
}

type checkReq struct {
	Agent      string          `json:"agent"`
	Definition json.RawMessage `json:"definition"`
}

type agentsResp struct {
	Agents []string `json:"agents"`
}

type runReq struct {
	Agent string `json:"agent"`
	// Definition is a stored agent's, which the server builds; a run with
	// one is sent as storedRunMethod.
	Definition json.RawMessage `json:"definition,omitempty"`
	// Model is the model to run the agent on, "" for its default; a run
	// with one is sent as modelRunMethod.
	Model string `json:"model,omitempty"`
	// Message is nil to resume a transcript without a new message.
	Message    *llm.Message  `json:"message,omitempty"`
	Transcript []llm.Message `json:"transcript,omitempty"`
	At         time.Time     `json:"at,omitzero"`
	// ToolPolicy narrows the agent's for this run. A run with one is sent
	// as policyRunMethod, which a runner older than it does not serve.
	ToolPolicy *policy.Policy `json:"tool_policy,omitempty"`
	// Interaction, Pending and Answers are the input's; a request that may
	// wait or finishes a pending turn is sent as interruptRunMethod.
	Interaction harness.Interaction `json:"interaction,omitempty"`
	Pending     *harness.Pending    `json:"pending,omitempty"`
	Answers     []harness.Answer    `json:"answers,omitempty"`
	// Tools is the input's choice of tools; a request with one is sent as
	// toolsRunMethod.
	Tools *harness.ToolChoice `json:"tools,omitempty"`
	// Caller is the tenant and user the run acts for, which its tools
	// (on a toolsets component too) read their credentials by.
	Caller tenancy.Carried `json:"caller,omitzero"`
}

// event is a [harness.Event] on the wire: only the fields its kind sets.
type event struct {
	Kind       harness.EventKind     `json:"kind"`
	Turn       int                   `json:"turn,omitempty"`
	At         time.Time             `json:"at,omitzero"`
	Text       string                `json:"text,omitempty"`
	Block      int                   `json:"block,omitempty"`
	Call       int                   `json:"call,omitempty"`
	ToolUse    json.RawMessage       `json:"tool_use,omitempty"`
	ToolResult json.RawMessage       `json:"tool_result,omitempty"`
	Duration   time.Duration         `json:"duration,omitempty"`
	Prompt     *harness.TimedMessage `json:"prompt,omitempty"`
	TurnRecord *harness.TurnRecord   `json:"turn_record,omitempty"`
	Result     *result               `json:"result,omitempty"`
	Decision   *policy.Decision      `json:"decision,omitempty"`
	Interrupt  *harness.Interrupt    `json:"interrupt,omitempty"`
}

// result is a [harness.Result] on the wire. Final is derived on the way back
// for a partial result (see [harness.NewRunError]).
type result struct {
	Transcript []llm.Message  `json:"transcript"`
	Final      *llm.Message   `json:"final,omitempty"`
	Usage      llm.Usage      `json:"usage"`
	Turns      int            `json:"turns"`
	Record     harness.Record `json:"record"`
	// Pending is a suspended run's pending turn.
	Pending *harness.Pending `json:"pending,omitempty"`
}

// failure is what a failed run's error carries besides its codes.
type failure struct {
	// Partial is what a run that failed as a *harness.RunError had done.
	Partial *result  `json:"partial,omitempty"`
	Refusal *refusal `json:"refusal,omitempty"`
}

type refusal struct {
	Category    string `json:"category,omitempty"`
	Explanation string `json:"explanation,omitempty"`
}

// Register serves agents on s, by name. Each call of Run runs its agent's
// Run on the request's input and streams the events back; each call of
// Compact its Compact, if it is a [runner.Compactor]. Agents lists the
// agents given, never stored ones.
func Register(s *rpc.Server, agents map[string]runner.Agent, opts ...Option) error {
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	for name, a := range agents {
		if a == nil {
			return fmt.Errorf("runnerrpc: agent %q is nil", name)
		}
	}
	agents = maps.Clone(agents)
	names := slices.Sorted(maps.Keys(agents))
	// find is the agent a request names: one of agents, or the stored agent
	// its definition describes.
	find := func(ctx context.Context, name string, definition json.RawMessage) (runner.Agent, error) {
		if definition == nil {
			if a := agents[name]; a != nil {
				return a, nil
			}
			return nil, fmt.Errorf("%w: %q", ErrNoAgent, name)
		}
		if o.stored == nil {
			return nil, fmt.Errorf("%w: agent %q", ErrNoStored, name)
		}
		a, err := o.stored(ctx, name, definition)
		if err != nil {
			return nil, encodeDefinition(err)
		}
		if a == nil {
			return nil, fmt.Errorf("runnerrpc: the stored agent %q was built nil", name)
		}
		return a, nil
	}
	agentsMethod.Handle(s, func(context.Context, struct{}) (agentsResp, error) {
		return agentsResp{Agents: slices.Clone(names)}, nil
	})
	checkStoredMethod.Handle(s, func(ctx context.Context, r checkReq) (struct{}, error) {
		if r.Definition == nil {
			return struct{}{}, fmt.Errorf("%w: no definition", rpc.ErrBadRequest)
		}
		_, err := find(ctx, r.Agent, r.Definition)
		return struct{}{}, err
	})
	compactMethod.Handle(s, func(ctx context.Context, r compactReq) (harness.Summary, error) {
		a, err := find(ctx, r.Agent, r.Definition)
		if err != nil {
			return harness.Summary{}, err
		}
		c, ok := a.(runner.Compactor)
		if !ok {
			return harness.Summary{}, fmt.Errorf("%w: %q", ErrCannotCompact, r.Agent)
		}
		return c.Compact(ctx, r.History)
	})
	run := func(ctx context.Context, r runReq) iter.Seq2[event, error] {
		return func(yield func(event, error) bool) {
			ctx, err := r.Caller.Into(ctx)
			if err != nil {
				yield(event{}, fmt.Errorf("%w: caller: %w", rpc.ErrBadRequest, err))
				return
			}
			a, err := find(ctx, r.Agent, r.Definition)
			if err != nil {
				yield(event{}, err)
				return
			}
			if r.Model != "" {
				ma, ok := a.(ModelAgent)
				if !ok {
					yield(event{}, fmt.Errorf("%w: agent %q runs only its default model, not %q", ErrNoModel, r.Agent, r.Model))
					return
				}
				if a, err = ma.ForModel(ctx, r.Model); err != nil {
					yield(event{}, fmt.Errorf("%w: agent %q on %q: %w", ErrNoModel, r.Agent, r.Model, err))
					return
				}
			}
			in := harness.Input{
				Transcript: r.Transcript, At: r.At, ToolPolicy: r.ToolPolicy,
				Interaction: r.Interaction, Pending: r.Pending, Answers: r.Answers, Tools: r.Tools,
			}
			if r.Message != nil {
				in.Message = *r.Message
			}
			for ev, err := range a.Run(ctx, in) {
				if err != nil {
					yield(event{}, encodeFailure(err))
					return
				}
				w, err := toWire(ev)
				if err != nil {
					yield(event{}, err)
					return
				}
				if !yield(w, nil) {
					return
				}
			}
		}
	}
	runMethod.Handle(s, run)
	policyRunMethod.Handle(s, run)
	interruptRunMethod.Handle(s, run)
	toolsRunMethod.Handle(s, run)
	modelRunMethod.Handle(s, run)
	storedRunMethod.Handle(s, run)
	return nil
}

// encodeDefinition is a stored builder's refusal as the server sends it:
// a *DefinitionError with its fields, any other error as it is.
func encodeDefinition(err error) error {
	if de, ok := errors.AsType[*DefinitionError](err); ok {
		return rpc.WithData(de, de)
	}
	return err
}

// decodeDefinition is a refusal of a definition back as the
// *DefinitionError the server's builder returned.
func decodeDefinition(err error) error {
	we, ok := errors.AsType[*rpc.Error](err)
	var de DefinitionError
	if ok && errors.Is(err, ErrInvalidDefinition) && we.Data(&de) {
		return &de
	}
	return err
}

// Client calls a server's agents. It is safe for concurrent use.
type Client struct{ conn rpc.Conn }

// NewClient returns a client that calls through conn.
func NewClient(conn rpc.Conn) *Client { return &Client{conn: conn} }

// Agents lists the names of the server's agents, sorted.
func (c *Client) Agents(ctx context.Context) ([]string, error) {
	resp, err := agentsMethod.Call(ctx, c.conn, struct{}{})
	return resp.Agents, err
}

// Agent returns the server's agent name, compacted ahead of time per window
// (the zero window: never). It does not call the server: an agent it does
// not run fails each run with [ErrNoAgent], and one that cannot compact each
// compaction with [ErrCannotCompact].
func (c *Client) Agent(name string, window harness.ContextWindow) runner.Compactor {
	return &agent{c: c, name: name, window: window}
}

// StoredAgent returns the stored agent name that definition describes, as
// the server builds it ([WithStored]), compacted ahead of time per window.
// Each run sends the definition: the server fails it with [ErrNoStored]
// when it builds no stored agents, and with a [*DefinitionError] when it
// cannot build this one. definition must not change afterwards.
func (c *Client) StoredAgent(name string, definition json.RawMessage, window harness.ContextWindow) runner.Compactor {
	return &agent{c: c, name: name, definition: definition, window: window}
}

// StoredModelAgent is [Client.StoredAgent] on model ("<provider>/<id>"), as
// [Client.ModelAgent] is [Client.Agent].
func (c *Client) StoredModelAgent(name, model string, definition json.RawMessage, window harness.ContextWindow) runner.Compactor {
	return &agent{c: c, name: name, model: model, definition: definition, window: window}
}

// CheckStored has the server build the stored agent name that definition
// describes: nil when it can, a [*DefinitionError] naming the fields when it
// refuses it, [ErrNoStored] when it builds no stored agents, and the
// transport's errors ([rpc.ErrUnavailable]).
func (c *Client) CheckStored(ctx context.Context, name string, definition json.RawMessage) error {
	_, err := checkStoredMethod.Call(ctx, c.conn, checkReq{Agent: name, Definition: definition})
	if err != nil {
		return decodeDefinition(err)
	}
	return nil
}

// ModelAgent returns the server's agent name as it runs on model
// ("<provider>/<id>"), as [Client.Agent] does for its default: the server
// fails each run with [ErrNoModel] when its agent does not run that model.
// Compaction is the agent's, on its default model.
func (c *Client) ModelAgent(name, model string, window harness.ContextWindow) runner.Compactor {
	return &agent{c: c, name: name, model: model, window: window}
}

type agent struct {
	c          *Client
	name       string
	model      string
	definition json.RawMessage // a stored agent's
	window     harness.ContextWindow
}

var _ runner.Compactor = (*agent)(nil)

func (a *agent) Compact(ctx context.Context, history harness.Transcript) (harness.Summary, error) {
	s, err := compactMethod.Call(ctx, a.c.conn, compactReq{Agent: a.name, History: history, Definition: a.definition})
	if err != nil {
		return harness.Summary{}, transient(decodeDefinition(err))
	}
	return s, nil
}

func (a *agent) ShouldCompact(u llm.Usage) bool { return a.window.Full(u) }

func (a *agent) Run(ctx context.Context, in harness.Input) iter.Seq2[harness.Event, error] {
	return func(yield func(harness.Event, error) bool) {
		req := runReq{
			Agent: a.name, Definition: a.definition, Model: a.model, Transcript: in.Transcript, At: in.At, ToolPolicy: in.ToolPolicy,
			Interaction: in.Interaction, Pending: in.Pending, Answers: in.Answers, Tools: in.Tools,
			Caller: tenancy.Carry(ctx),
		}
		if in.Message.Role != "" || len(in.Message.Content) > 0 {
			m := in.Message
			req.Message = &m
		}
		method, waits := runMethod, mayWait(in)
		switch {
		case req.Definition != nil:
			method = storedRunMethod
		case req.Model != "":
			method = modelRunMethod
		case req.Tools != nil:
			method = toolsRunMethod
		case waits:
			method = interruptRunMethod
		case req.ToolPolicy != nil:
			method = policyRunMethod
		}
		for w, err := range method.Call(ctx, a.c.conn, req) {
			if err != nil {
				switch {
				case req.Definition != nil && errors.Is(err, rpc.ErrUnknownMethod):
					err = fmt.Errorf("agent %q: the runner is too old to run stored agents: %w", a.name, err)
				case req.Model != "" && errors.Is(err, rpc.ErrUnknownMethod):
					err = fmt.Errorf("agent %q: the runner is too old to run another model than the agent's default: %w", a.name, err)
				case req.Tools != nil && errors.Is(err, rpc.ErrUnknownMethod):
					err = fmt.Errorf("agent %q: the runner is too old to choose the run's tools: %w", a.name, err)
				case waits && errors.Is(err, rpc.ErrUnknownMethod):
					err = fmt.Errorf("agent %q: the runner is too old to wait for people: %w", a.name, err)
				case req.ToolPolicy != nil && errors.Is(err, rpc.ErrUnknownMethod):
					err = fmt.Errorf("agent %q: the runner is too old to enforce the run's tool policy: %w", a.name, err)
				}
				yield(harness.Event{}, decodeFailure(decodeDefinition(err)))
				return
			}
			ev, err := fromWire(w)
			if err != nil {
				yield(harness.Event{}, fmt.Errorf("agent %q: %w: %w", a.name, rpc.ErrUnavailable, err))
				return
			}
			if !yield(ev, nil) {
				return
			}
		}
	}
}

// mayWait reports whether a run of in may wait for people, or finishes a
// pending turn: what a runner older than interrupts cannot do.
func mayWait(in harness.Input) bool {
	return in.Pending != nil || len(in.Answers) > 0 ||
		in.Interaction == harness.Interactive || in.Interaction == harness.Supervised
}

func toWire(ev harness.Event) (event, error) {
	w := event{
		Kind: ev.Kind, Turn: ev.Turn, At: ev.At, Text: ev.Text, Block: ev.Block, Call: ev.Call,
		Duration: ev.Duration, Prompt: ev.Prompt, TurnRecord: ev.TurnRecord,
	}
	var err error
	switch ev.Kind {
	case harness.EventToolStart:
		w.ToolUse, err = json.Marshal(ev.ToolUse)
	case harness.EventToolPolicy, harness.EventToolFlagged:
		w.ToolUse, err = json.Marshal(ev.ToolUse)
		d := ev.Decision
		w.Decision = &d
	case harness.EventToolDone:
		w.ToolResult, err = json.Marshal(ev.ToolResult)
	case harness.EventInterrupt:
		w.ToolUse, err = json.Marshal(ev.ToolUse)
		if ev.Interrupt != nil {
			in := *ev.Interrupt
			w.Interrupt = &in
		}
	case harness.EventRunDone, harness.EventSuspended:
		if ev.Result != nil {
			r := resultToWire(*ev.Result)
			w.Result = &r
		}
	}
	return w, err
}

func fromWire(w event) (harness.Event, error) {
	ev := harness.Event{
		Kind: w.Kind, Turn: w.Turn, At: w.At, Text: w.Text, Block: w.Block, Call: w.Call,
		Duration: w.Duration, Prompt: w.Prompt, TurnRecord: w.TurnRecord,
	}
	if w.ToolUse != nil {
		b, err := llm.UnmarshalBlock(w.ToolUse)
		u, ok := b.(llm.ToolUse)
		if err != nil || !ok {
			return ev, fmt.Errorf("tool use: %w", errors.Join(err, errors.New("not a tool use")))
		}
		ev.ToolUse = u
	}
	if w.ToolResult != nil {
		b, err := llm.UnmarshalBlock(w.ToolResult)
		r, ok := b.(llm.ToolResult)
		if err != nil || !ok {
			return ev, fmt.Errorf("tool result: %w", errors.Join(err, errors.New("not a tool result")))
		}
		ev.ToolResult = r
	}
	if w.Result != nil {
		r := w.Result.toResult()
		ev.Result = &r
	}
	if w.Interrupt != nil {
		// An interrupt's event names the call; the interrupt has its input.
		ev.Interrupt = w.Interrupt
		ev.ToolUse = llm.ToolUse{ID: ev.ToolUse.ID, Name: ev.ToolUse.Name}
	}
	if w.Decision != nil {
		ev.Decision = *w.Decision
		// A decision's event names the call without its input.
		ev.ToolUse = llm.ToolUse{ID: ev.ToolUse.ID, Name: ev.ToolUse.Name}
	}
	return ev, nil
}

func resultToWire(r harness.Result) result {
	w := result{Transcript: r.Transcript, Usage: r.Usage, Turns: r.Turns, Record: r.Record, Pending: r.Pending}
	if r.Final.Role != "" {
		f := r.Final
		w.Final = &f
	}
	return w
}

func (w result) toResult() harness.Result {
	r := harness.Result{Transcript: w.Transcript, Usage: w.Usage, Turns: w.Turns, Record: w.Record, Pending: w.Pending}
	if w.Final != nil {
		r.Final = *w.Final
	}
	return r
}

// encodeFailure is a run's error as the server sends it: the cause, with
// what the run had done and the details of a refusal.
func encodeFailure(err error) error {
	var f failure
	if re, ok := errors.AsType[*harness.RunError](err); ok {
		p := resultToWire(re.Partial())
		f.Partial = &p
		err = re.Err
	}
	if r, ok := errors.AsType[*harness.RefusalError](err); ok {
		f.Refusal = &refusal{Category: r.Category, Explanation: r.Explanation}
	}
	if f.Partial == nil && f.Refusal == nil {
		return err
	}
	return rpc.WithData(err, f)
}

// transient makes a runner that could not be reached, or that went away
// mid-run (a restart, a rollout), a failure the runner retries, as a local
// model's dropped connection is (llm.ErrOverloaded).
func transient(err error) error {
	if errors.Is(err, rpc.ErrUnavailable) && !llm.IsRetryable(err) {
		return fmt.Errorf("%w: %w", llm.ErrOverloaded, err)
	}
	return err
}

// decodeFailure turns the error of a remote run back into what the local
// run would have returned.
func decodeFailure(err error) error {
	we, ok := errors.AsType[*rpc.Error](err)
	var f failure
	if !ok || !we.Data(&f) {
		return transient(err)
	}
	cause := err
	if f.Refusal != nil {
		cause = &harness.RefusalError{Category: f.Refusal.Category, Explanation: f.Refusal.Explanation}
	}
	if f.Partial == nil {
		return cause
	}
	return harness.NewRunError(cause, f.Partial.toResult())
}
