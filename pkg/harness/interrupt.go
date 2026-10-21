package harness

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"

	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/policy"
)

// Interaction says how a run may involve a person ([WithInteraction],
// [Input.Interaction]).
type Interaction string

// The interaction modes, from the one that involves a person most.
const (
	// Interactive: a person is in the conversation. Tools that interrupt
	// ([tool.Metadata.Interrupts], such as ask_user) are offered, and a call
	// the tool policy wants approved waits for the approval: the run
	// suspends ([EventSuspended]) until the answers come back
	// ([Input.Answers]).
	Interactive Interaction = "interactive"
	// Supervised: nobody watches the run live, but it may wait. The harness
	// treats it as Interactive; whoever runs it notifies someone instead of
	// asking inline.
	Supervised Interaction = "supervised"
	// Autonomous: the run never waits. Interrupting tools are not offered
	// (a call to one anyway is an error result), and a call the policy
	// wants approved is refused as if denied, saying why.
	Autonomous Interaction = "autonomous"
)

var interactions = []Interaction{Interactive, Supervised, Autonomous}

// Interactions returns every mode, from the one that involves a person most.
func Interactions() []Interaction { return slices.Clone(interactions) }

// Valid reports whether m is one of [Interactions].
func (m Interaction) Valid() bool { return slices.Contains(interactions, m) }

// waits reports whether a run in mode m may suspend for a person.
func (m Interaction) waits() bool { return m == Interactive || m == Supervised }

// NarrowInteraction returns the narrower of two modes: the one that involves
// a person less. An empty mode leaves the other; an unknown one counts as
// [Autonomous].
func NarrowInteraction(a, b Interaction) Interaction {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	}
	rank := func(m Interaction) int {
		if i := slices.Index(interactions, m); i >= 0 {
			return i
		}
		return len(interactions) - 1
	}
	if rank(b) > rank(a) {
		return interactions[rank(b)]
	}
	return interactions[rank(a)]
}

// InterruptKind says what an [Interrupt] waits for.
type InterruptKind string

// The interrupt kinds.
const (
	// InterruptQuestion: a tool asked a person a question (it returned a
	// [*tool.InterruptError]). The answer's Text becomes the call's result.
	InterruptQuestion InterruptKind = "question"
	// InterruptApproval: the tool policy wants a person to approve the call
	// first ([policy.Approve]). Approved, the call runs with its original
	// input; denied, its result is the denial.
	InterruptApproval InterruptKind = "approval"
)

// Interrupt is a tool call that waits for a person.
type Interrupt struct {
	// ID is the call's tool use ID; answers name it.
	ID string `json:"id"`
	// Call is the call's index in its turn, and Name its tool's name.
	Call int           `json:"call"`
	Name string        `json:"name"`
	Kind InterruptKind `json:"kind"`
	// Prompt is the question, or for an approval why it is needed (the
	// policy's reason).
	Prompt string `json:"prompt,omitempty"`
	// Choices are the answers a question accepts; empty means any text.
	Choices []string `json:"choices,omitempty"`
	// Decision is the policy's decision of an approval.
	Decision *policy.Decision `json:"decision,omitempty"`
	// Input is an approval's call input, byte for byte as the model wrote
	// it: approved, the call runs with it. (The reply holds it too, but a
	// message's JSON does not keep a call's input bytes.)
	Input string `json:"input,omitempty"`
}

func (i Interrupt) clone() Interrupt {
	i.Choices = slices.Clone(i.Choices)
	if i.Decision != nil {
		d := *i.Decision
		i.Decision = &d
	}
	return i
}

// ErrInvalidAnswer means an [Answer] does not fit its interrupt. It matches
// [ErrInvalidInput] too.
var ErrInvalidAnswer = fmt.Errorf("%w: invalid answer", ErrInvalidInput)

// Answer is a person's answer to an [Interrupt].
type Answer struct {
	// ID is the interrupt's ID.
	ID string `json:"id"`
	// Text answers a question: one of its Choices if it has any.
	Text string `json:"text,omitempty"`
	// Approved, Reason and By answer an approval. A denial's result says
	// "denied by <By>: <Reason>".
	Approved bool   `json:"approved,omitempty"`
	Reason   string `json:"reason,omitempty"`
	By       string `json:"by,omitempty"`
}

// Check reports whether a answers i: it names i, answers a question with
// text (one of its choices, if it has any) and an approval without text. An
// answer that does not fits [ErrInvalidAnswer].
func (i Interrupt) Check(a Answer) error {
	switch {
	case a.ID != i.ID:
		return fmt.Errorf("%w: answer %q is not for interrupt %q", ErrInvalidAnswer, a.ID, i.ID)
	case i.Kind == InterruptQuestion && a.Text == "":
		return fmt.Errorf("%w: interrupt %q asks a question: answer it with text", ErrInvalidAnswer, i.ID)
	case i.Kind == InterruptQuestion && len(i.Choices) > 0 && !slices.Contains(i.Choices, a.Text):
		return fmt.Errorf("%w: interrupt %q takes one of %q, not %q", ErrInvalidAnswer, i.ID, i.Choices, a.Text)
	case i.Kind == InterruptQuestion && (a.Approved || a.Reason != ""):
		return fmt.Errorf("%w: interrupt %q asks a question, not for an approval", ErrInvalidAnswer, i.ID)
	case i.Kind == InterruptApproval && a.Text != "":
		return fmt.Errorf("%w: interrupt %q asks for an approval: answer it with approved and a reason, not text", ErrInvalidAnswer, i.ID)
	case i.Kind != InterruptQuestion && i.Kind != InterruptApproval:
		return fmt.Errorf("%w: interrupt %q has unknown kind %q", ErrInvalidAnswer, i.ID, i.Kind)
	}
	return nil
}

// result is the call's result for answer a, for a question or a denied
// approval (an approved call runs instead).
func (i Interrupt) result(a Answer) llm.ToolResult {
	if i.Kind == InterruptQuestion {
		return llm.ToolResult{ToolUseID: i.ID, Content: a.Text}
	}
	by, reason := a.By, a.Reason
	if by == "" {
		by = "a person"
	}
	if reason == "" {
		reason = "no reason given"
	}
	return llm.ToolResult{ToolUseID: i.ID, Content: "denied by " + by + ": " + reason, IsError: true}
}

// Pending is a turn that waits for people: the model's reply, the results of
// the calls that finished, and the interrupts of the others. A run that
// suspends returns it ([EventSuspended], [Result.Pending]); a later run
// finishes it with the answers ([Input.Pending], [Input.Answers]). It is not
// part of the transcript, which stays valid without it, and it marshals to
// JSON to be kept until the answers come.
type Pending struct {
	// Turn is the turn so far: its number, times, usage and stop reason as
	// the suspended run recorded them, Messages holding the reply alone, and
	// a record per call (an interrupted call's runs until it interrupted).
	Turn TurnRecord `json:"turn"`
	// Done is a user message of the results of the calls that finished, in
	// the order of the calls; it has no content if none did.
	Done llm.Message `json:"done"`
	// Interrupts are the calls that wait, in the order of the calls.
	Interrupts []Interrupt `json:"interrupts"`
}

// Reply returns the model's reply whose calls wait, or the zero message.
func (p Pending) Reply() llm.Message {
	if len(p.Turn.Messages) == 0 {
		return llm.Message{}
	}
	return p.Turn.Messages[0].Message
}

// Clone returns a deep copy of p.
func (p Pending) Clone() Pending {
	out := Pending{Turn: p.Turn.clone(), Done: p.Done.Clone()}
	for _, i := range p.Interrupts {
		out.Interrupts = append(out.Interrupts, i.clone())
	}
	return out
}

// Interrupt returns the interrupt with the ID id.
func (p Pending) Interrupt(id string) (Interrupt, bool) {
	for _, i := range p.Interrupts {
		if i.ID == id {
			return i.clone(), true
		}
	}
	return Interrupt{}, false
}

// check reports whether p can be resumed after t: its reply is a valid
// next message calling tools, Done answers some of those calls in order, the
// interrupts are exactly the others, and each call has a record.
func (p Pending) check(t Transcript) error {
	if len(p.Turn.Messages) != 1 {
		return invalidInput("the pending turn has %d messages, want its reply alone", len(p.Turn.Messages))
	}
	reply := p.Reply()
	if err := replyProblem(reply); err != nil {
		return invalidInput("the pending turn's reply: %v", err)
	}
	uses := toolUses(reply)
	if len(uses) == 0 {
		return invalidInput("the pending turn's reply calls no tools")
	}
	if len(p.Turn.Tools) != len(uses) {
		return invalidInput("the pending turn has %d tool records for %d calls", len(p.Turn.Tools), len(uses))
	}
	if p.Done.Role != llm.RoleUser && (p.Done.Role != "" || len(p.Done.Content) > 0) {
		return invalidInput("the pending turn's results have role %q", p.Done.Role)
	}
	done := map[string]bool{}
	next := 0 // the results are in the order of the calls
	for _, b := range p.Done.Content {
		r, ok := b.(llm.ToolResult)
		for ok && next < len(uses) && uses[next].ID != r.ToolUseID {
			next++
		}
		if !ok || next == len(uses) {
			return invalidInput("the pending turn's results are not results of its calls, in order")
		}
		done[r.ToolUseID] = true
		next++
	}
	waiting := map[string]bool{}
	for _, i := range p.Interrupts {
		c := i.Call
		if c < 0 || c >= len(uses) || uses[c].ID != i.ID || uses[c].Name != i.Name || done[i.ID] || waiting[i.ID] {
			return invalidInput("interrupt %q is not a waiting call of the pending turn", i.ID)
		}
		if i.Kind != InterruptQuestion && i.Kind != InterruptApproval {
			return invalidInput("interrupt %q has unknown kind %q", i.ID, i.Kind)
		}
		if i.Kind == InterruptApproval && !sameJSON([]byte(i.Input), uses[c].Input) {
			return invalidInput("interrupt %q holds an input other than its call's", i.ID)
		}
		waiting[i.ID] = true
	}
	if len(done)+len(waiting) != len(uses) || len(waiting) == 0 {
		return invalidInput("the pending turn has %d calls, %d results and %d interrupts", len(uses), len(done), len(waiting))
	}
	if !slices.IsSortedFunc(p.Interrupts, func(a, b Interrupt) int { return a.Call - b.Call }) {
		return invalidInput("the pending turn's interrupts are not in the order of the calls")
	}
	// The reply must be what may come next, whatever the answers.
	full := append(t.Clone(), reply, p.results(nil))
	return full.Validate()
}

// sameJSON reports whether a and b are the same JSON value, or both empty.
func sameJSON(a, b []byte) bool {
	if len(a) == 0 || len(b) == 0 {
		return len(a) == len(b)
	}
	var va, vb any
	if json.Unmarshal(a, &va) != nil || json.Unmarshal(b, &vb) != nil {
		return false
	}
	return reflect.DeepEqual(va, vb)
}

// results is the user message of every call's result: Done's, and for each
// interrupt its result in got, or a placeholder.
func (p Pending) results(got map[int]llm.ToolResult) llm.Message {
	done := map[string]llm.ToolResult{}
	for _, b := range p.Done.Content {
		if r, ok := b.(llm.ToolResult); ok {
			done[r.ToolUseID] = r
		}
	}
	var blocks []llm.Block
	for i, u := range toolUses(p.Reply()) {
		r, ok := done[u.ID]
		if !ok {
			r, ok = got[i]
		}
		if !ok {
			r = llm.ToolResult{ToolUseID: u.ID, Content: "(waiting)"}
		}
		blocks = append(blocks, r)
	}
	return llm.User(blocks...)
}

// checkAnswers reports whether answers answer every interrupt of p once, and
// returns them by call.
func (p Pending) checkAnswers(answers []Answer) (map[int]Answer, error) {
	byCall := map[int]Answer{}
	for _, a := range answers {
		i, ok := p.Interrupt(a.ID)
		if !ok {
			return nil, fmt.Errorf("%w: answer %q is for no interrupt of the pending turn", ErrInvalidAnswer, a.ID)
		}
		if _, dup := byCall[i.Call]; dup {
			return nil, fmt.Errorf("%w: interrupt %q is answered twice", ErrInvalidAnswer, a.ID)
		}
		if err := i.Check(a); err != nil {
			return nil, err
		}
		byCall[i.Call] = a
	}
	for _, i := range p.Interrupts {
		if _, ok := byCall[i.Call]; !ok {
			return nil, fmt.Errorf("%w: interrupt %q has no answer", ErrInvalidAnswer, i.ID)
		}
	}
	return byCall, nil
}

// ErrSuspended means a run suspended: some tool calls wait for people
// ([EventSuspended]). [Collect] returns it as a [*SuspendedError].
var ErrSuspended = errors.New("run suspended: tool calls wait for a person")

// SuspendedError is what [Collect] returns for a run that suspended, with the
// partial result. It matches [ErrSuspended].
type SuspendedError struct {
	Pending Pending
}

func (e *SuspendedError) Error() string {
	ids := make([]string, len(e.Pending.Interrupts))
	for i, in := range e.Pending.Interrupts {
		ids[i] = in.ID
	}
	return fmt.Sprintf("harness: %v: %q", ErrSuspended, ids)
}

func (e *SuspendedError) Unwrap() error { return ErrSuspended }
