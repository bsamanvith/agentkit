package agui

import (
	"encoding/json"
	"slices"
	"strconv"
	"strings"

	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
	"github.com/bsamanvith/agentkit/pkg/llm"
)

// IDs the server gives what it streams. Each starts with the ID of the run
// (an agent API run) and a slash, so that a later request whose messages
// carry one is known to continue that run's conversation (see thread).
//
//	{run}/t{turn}        the assistant message of a turn
//	{run}/r/{toolUseID}  the tool message of a call's result
//	{run}/i/{toolUseID}  the interrupt of a call that waits for people
//
// A run that drops a turn and does it again (a retry, a rewind) gives the
// messages from then on a ".{n}" suffix, so that no ID names two messages.
func messageID(runID string, turn, gen int) string {
	return runID + "/t" + strconv.Itoa(turn) + genSuffix(gen)
}

func resultID(runID, toolUseID string, gen int) string {
	return runID + "/r/" + toolUseID + genSuffix(gen)
}

func interruptID(runID, toolUseID string) string { return runID + "/i/" + toolUseID }

func genSuffix(gen int) string {
	if gen == 0 {
		return ""
	}
	return "." + strconv.Itoa(gen)
}

// runOfMessage returns the run in a message ID the server gave.
func runOfMessage(id string) (string, bool) {
	run, rest, ok := strings.Cut(id, "/")
	if !ok || !isID(run) || (!strings.HasPrefix(rest, "t") && !strings.HasPrefix(rest, "r/")) {
		return "", false
	}
	return run, true
}

// parseInterruptID splits an interrupt ID the server gave into its run and
// tool call.
func parseInterruptID(id string) (runID, toolUseID string, ok bool) {
	run, rest, ok := strings.Cut(id, "/i/")
	if !ok || !isID(run) || rest == "" || strings.Contains(run, "/") {
		return "", "", false
	}
	return run, rest, true
}

// isID reports whether s can name a conversation or a run of the agent
// API: not "", "." or "..", which the API's router would clean into another
// path (GET /v1/conversations/. is the list of conversations).
func isID(s string) bool { return s != "" && s != "." && s != ".." }

// translator turns an agent API run's events into AG-UI events. It keeps
// the messages it streamed, as the client's reducer builds them from the
// events, for a MESSAGES_SNAPSHOT when a turn it streamed is dropped.
type translator struct {
	em    *emitter
	runID string
	model wire.Model

	gen   int               // bumped by each retry and rewind
	done  int               // the last turn whose turn_done was seen
	turns map[int]*turnText // the turns of this generation
	open  *turnText         // the turn whose text message is open
	log   []logged          // the messages streamed so far
	ui    []wire.Event      // the ui events of a call, not sent yet
	asked []Interrupt       // the interrupts seen
	dirty bool              // a streamed message was dropped

	// Set when the run goes on after answers (see resume): the answered
	// calls, which the client already has, and the last turn begun before.
	answered  map[string]bool
	resumedAt int
}

// turnText is a turn's assistant message: its ID and the text streamed.
type turnText struct {
	turn int
	id   string
	text strings.Builder
	msg  int // its index in log, or -1
}

// logged is a streamed message and the turn it belongs to.
type logged struct {
	turn int
	msg  Message
	text string // an assistant message's text so far
}

func newTranslator(em *emitter, runID string, model wire.Model) *translator {
	return &translator{em: em, runID: runID, model: model, turns: map[int]*turnText{}}
}

func (t *translator) turn(n int) *turnText {
	tt := t.turns[n]
	if tt == nil {
		tt = &turnText{turn: n, id: messageID(t.runID, n, t.gen), msg: -1}
		t.turns[n] = tt
	}
	return tt
}

// assistant returns the index in log of turn tt's assistant message,
// adding it.
func (t *translator) assistant(tt *turnText) int {
	if tt.msg < 0 {
		t.log = append(t.log, logged{turn: tt.turn, msg: Message{ID: tt.id, Role: "assistant"}})
		tt.msg = len(t.log) - 1
	}
	return tt.msg
}

// handle translates one event. It reports whether the run ended.
func (t *translator) handle(e wire.Event) (bool, error) {
	if e.Type != wire.TypeUI {
		if err := t.flushUI(); err != nil {
			return false, err
		}
	}
	at := stamp(e.At, t.em.clk)
	switch e.Type {
	case wire.TypeTextDelta:
		if e.Text == "" {
			return false, nil
		}
		return false, t.text(t.turn(e.Turn), e.Text, at)
	case wire.TypeToolCall:
		if err := t.closeText(at); err != nil {
			return false, err
		}
		if t.answered[e.ToolUseID] {
			// The client has the call from the run that asked: the spec has
			// the result answer it, with no second start.
			return false, nil
		}
		tt := t.turn(e.Turn)
		args := string(e.Input)
		if args == "" || args == "null" {
			args = "{}"
		}
		for _, ev := range []Event{
			{Type: EventToolCallStart, Timestamp: at, ToolCallID: e.ToolUseID, ToolCallName: e.Name, ParentMessageID: tt.id},
			{Type: EventToolCallArgs, Timestamp: at, ToolCallID: e.ToolUseID, Delta: ptr(args)},
			{Type: EventToolCallEnd, Timestamp: at, ToolCallID: e.ToolUseID},
		} {
			if err := t.em.send(ev); err != nil {
				return false, err
			}
		}
		i := t.assistant(tt)
		t.log[i].msg.ToolCalls = append(t.log[i].msg.ToolCalls, ToolCall{ID: e.ToolUseID, Type: "function", Function: FunctionCall{Name: e.Name, Arguments: args}})
		return false, nil
	case wire.TypeToolResult:
		if err := t.closeText(at); err != nil {
			return false, err
		}
		id := resultID(t.runID, e.ToolUseID, t.gen)
		ev := Event{Type: EventToolCallResult, Timestamp: at, MessageID: id, ToolCallID: e.ToolUseID, Content: jsonString(e.Content), Role: "tool"}
		if e.IsError {
			ev.Metadata = map[string]any{MetadataKey: map[string]bool{"isError": true}}
		}
		t.log = append(t.log, logged{turn: e.Turn, msg: Message{ID: id, Role: "tool", Content: jsonString(e.Content), ToolCallID: e.ToolUseID}})
		return false, t.em.send(ev)
	case wire.TypeInterrupt:
		if e.Interrupt != nil {
			t.asked = append(t.asked, interruptOf(t.runID, e.ToolUseID, e.Name, *e.Interrupt))
		}
		return false, t.closeText(at)
	case wire.TypeUI:
		if err := t.closeText(at); err != nil {
			return false, err
		}
		t.ui = append(t.ui, e)
		return false, nil
	case wire.TypeTurnDone:
		if err := t.catchUp(e, at); err != nil {
			return false, err
		}
		t.done = max(t.done, e.Turn)
		return false, t.closeText(at)
	case wire.TypeRetrying:
		// The turn in progress is dropped and done again.
		return false, t.drop(t.done, at)
	case wire.TypeRewind:
		// Only the first Turns turns stand.
		t.done = min(t.done, e.Turns)
		return false, t.drop(e.Turns, at)
	case wire.TypeRunDone, wire.TypeRunError:
		return true, t.end(e, at)
	}
	return false, nil // run_start, tool_policy, tool_flagged and types to come
}

// text streams a piece of turn tt's text, opening its message.
func (t *translator) text(tt *turnText, s string, at int64) error {
	if t.open != tt {
		if err := t.closeText(at); err != nil {
			return err
		}
		if err := t.em.send(Event{Type: EventTextMessageStart, Timestamp: at, MessageID: tt.id, Role: "assistant"}); err != nil {
			return err
		}
		t.open = tt
	}
	tt.text.WriteString(s)
	i := t.assistant(tt)
	t.log[i].text += s
	t.log[i].msg.Content = jsonString(t.log[i].text)
	return t.em.send(Event{Type: EventTextMessageContent, Timestamp: at, MessageID: tt.id, Delta: ptr(s)})
}

func (t *translator) closeText(at int64) error {
	if t.open == nil {
		return nil
	}
	id := t.open.id
	t.open = nil
	return t.em.send(Event{Type: EventTextMessageEnd, Timestamp: at, MessageID: id})
}

// catchUp streams the rest of a turn's text from its record, when its
// text_delta events were missed (the run was followed after they were
// sent, which only streams them live). A turn begun before the run went on
// after answers is the client's already.
func (t *translator) catchUp(e wire.Event, at int64) error {
	if e.Record == nil || e.Turn <= t.resumedAt {
		return nil
	}
	var reply string
	for _, m := range e.Record.Messages {
		if m.Message.Role == llm.RoleAssistant {
			reply = m.Message.Text()
			break
		}
	}
	tt := t.turn(e.Turn)
	sent := tt.text.String()
	if len(reply) <= len(sent) || !strings.HasPrefix(reply, sent) {
		return nil
	}
	return t.text(tt, reply[len(sent):], at)
}

// drop discards the messages of the turns after keep: their events are
// done again. The client is told with a MESSAGES_SNAPSHOT at the run's end.
func (t *translator) drop(keep int, at int64) error {
	if err := t.closeText(at); err != nil {
		return err
	}
	n := len(t.log)
	t.log = slices.DeleteFunc(t.log, func(l logged) bool { return l.turn > keep })
	if len(t.log) != n {
		t.dirty = true
	}
	t.ui = nil
	t.gen++
	t.turns = map[int]*turnText{}
	return nil
}

// flushUI sends the ui events of a call as an A2UI activity per surface:
// an ACTIVITY_SNAPSHOT whose content is the surface's messages of the call,
// in order (the shape of AG-UI's A2UI middleware).
func (t *translator) flushUI() error {
	if len(t.ui) == 0 {
		return nil
	}
	evs := t.ui
	t.ui = nil
	type group struct {
		call, surface string
		ops           []json.RawMessage
		at            int64
	}
	var groups []*group
	calls := map[string]int{}
	for _, e := range evs {
		i := slices.IndexFunc(groups, func(g *group) bool { return g.call == e.ToolUseID && g.surface == e.SurfaceID })
		if i < 0 {
			groups = append(groups, &group{call: e.ToolUseID, surface: e.SurfaceID, at: stamp(e.At, t.em.clk)})
			i = len(groups) - 1
			calls[e.ToolUseID]++
		}
		groups[i].ops = append(groups[i].ops, e.UI)
	}
	for _, g := range groups {
		id := "a2ui-surface-" + g.call
		if calls[g.call] > 1 {
			id = "a2ui-surface-" + g.surface + "-" + g.call
		}
		content, err := json.Marshal(map[string][]json.RawMessage{A2UIOperationsKey: g.ops})
		if err != nil {
			return err
		}
		if err := t.em.send(Event{Type: EventActivitySnapshot, Timestamp: g.at, MessageID: id, ActivityType: ActivityTypeA2UI, Content: content, Replace: ptr(true)}); err != nil {
			return err
		}
	}
	return nil
}

// end sends the run's last event: RUN_FINISHED for an answer, a run that
// waits for people (interrupt) or one canceled; RUN_ERROR for a failure.
func (t *translator) end(e wire.Event, at int64) error {
	if err := t.closeText(at); err != nil {
		return err
	}
	if t.dirty {
		if err := t.snapshot(at); err != nil {
			return err
		}
	}
	usage := t.usage(e.Usage)
	if e.Type == wire.TypeRunDone {
		var result json.RawMessage
		if e.Answer != "" {
			result = jsonString(e.Answer)
		}
		return t.em.finish(e.At, &Outcome{Type: OutcomeSuccess}, result, usage)
	}
	code, msg := wire.CodeInternal, "the run failed"
	if e.Error != nil {
		code, msg = e.Error.Code, e.Error.Message
	}
	switch code {
	case wire.CodeSuspended:
		if len(t.asked) > 0 {
			return t.em.finish(e.At, &Outcome{Type: OutcomeInterrupt, Interrupts: t.asked}, nil, usage)
		}
		// A flow run waits for a decision, which AG-UI has no way to give.
		code = CodeUnsupported
	case wire.CodeCancelled:
		return t.em.finish(e.At, &Outcome{Type: OutcomeCancelled}, nil, usage)
	}
	return t.em.send(Event{Type: EventRunError, Timestamp: at, Code: code, Message: msg, Usage: usage})
}

// snapshot sends the messages of the conversation as the client should
// have them: those it sent, then those this run streamed and kept.
func (t *translator) snapshot(at int64) error {
	msgs := slices.Clone(t.em.in.Messages)
	for _, l := range t.log {
		b, err := json.Marshal(l.msg)
		if err != nil {
			return err
		}
		msgs = append(msgs, b)
	}
	if msgs == nil {
		msgs = []json.RawMessage{}
	}
	b, err := json.Marshal(msgs)
	if err != nil {
		return err
	}
	return t.em.send(Event{Type: EventMessagesSnapshot, Timestamp: at, Messages: b})
}

func (t *translator) usage(u *llm.Usage) []TokenUsage {
	if u == nil {
		return nil
	}
	in := u.InputTokens + u.CacheReadTokens + u.CacheWriteTokens
	return []TokenUsage{{
		Provider: t.model.Provider, Model: t.model.ID,
		InputTokens: in, OutputTokens: u.OutputTokens, TotalTokens: in + u.OutputTokens,
		CachedInputTokens: u.CacheReadTokens, CacheWriteInputTokens: u.CacheWriteTokens,
	}}
}

// The response schemas of interrupts: what a resume's payload is.
var (
	approvalSchema = json.RawMessage(`{"type":"object","properties":{"approved":{"type":"boolean"},"reason":{"type":"string"}},"required":["approved"]}`)
)

// questionSchema is the payload of a question's answer: {"text": ...}, one
// of the choices if there are any.
func questionSchema(choices []string) json.RawMessage {
	text := map[string]any{"type": "string"}
	if len(choices) > 0 {
		text["enum"] = choices
	}
	b, _ := json.Marshal(map[string]any{"type": "object", "properties": map[string]any{"text": text}, "required": []string{"text"}})
	return b
}

// interruptOf is the AG-UI interrupt of a call that waits for people: an
// approval is a tool_call interrupt whose answer is {"approved": ...}; a
// question an input_required one whose answer is {"text": ...}.
func interruptOf(runID, toolUseID, tool string, in wire.Interrupt) Interrupt {
	meta := map[string]any{"kind": in.Kind, "tool": tool}
	out := Interrupt{ID: interruptID(runID, toolUseID), ToolCallID: toolUseID, Message: in.Prompt}
	if in.Kind == wire.InterruptApproval {
		out.Reason, out.ResponseSchema = ReasonToolCall, approvalSchema
		if out.Message == "" {
			out.Message = "Approve the call of " + tool + "?"
		}
		if in.Policy != "" {
			meta["policy"], meta["rule"] = in.Policy, in.Rule
		}
	} else {
		out.Reason, out.ResponseSchema = ReasonInputRequired, questionSchema(in.Choices)
		if len(in.Choices) > 0 {
			meta["choices"] = in.Choices
		}
	}
	out.Metadata = map[string]any{MetadataKey: meta}
	return out
}
