package harness_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/policy"
)

// The invariants of a run. They hold for every run, whatever the model said,
// whichever tools failed and wherever the consumer stopped, so rig.drive checks
// them after every run in this package, not just in the test that is about
// them. A new behavior then cannot break one of them unnoticed in a scenario
// that no test happens to look at.
//
//	events:      a run is well-formed; every call that began is finished before its turn is
//	requests:    the model is only ever sent valid conversations, each extending the last,
//	             and nothing it was sent changes afterwards
//	transcript:  valid, saved and loaded without loss, equal to input + what events announced
//	record:      adds up (usage, turn count, times that only move forward)
//	budgets:     never exceeded by more than the one turn that is allowed to overshoot

// limits are the budgets of a rig, for tests that set them. Zero means none.
type limits struct{ maxTurns, maxTokens int }

func (r *rig) check(tb testing.TB, o observation, from int) {
	tb.Helper()
	checkEvents(tb, o.events, o.complete && o.err == nil, o.in.Pending != nil)
	sent, snap := r.rec.requests(from)

	if errors.Is(o.err, harness.ErrInvalidInput) {
		if len(snap) != 0 || len(o.events) != 0 {
			tb.Errorf("invalid input: %d model calls and %d events, want none", len(snap), len(o.events))
		}
		return
	}
	checkRequests(tb, o.in, sent, snap)
	if !o.complete {
		return // the consumer stopped: there is no result to check
	}

	res := o.res
	if err := res.Transcript.Validate(); err != nil {
		tb.Errorf("the transcript is not valid: %v", err)
	}
	checkRoundTrip(tb, res.Transcript)
	if n := len(o.in.Transcript); len(res.Transcript) < n {
		tb.Errorf("the transcript has %d messages, fewer than the %d it started with", len(res.Transcript), n)
	} else {
		diff(tb, "the input transcript (it is only ever appended to)", []llm.Message(o.in.Transcript), []llm.Message(res.Transcript[:n]))
	}
	diff(tb, "messages announced by events", []llm.Message(res.Transcript[len(o.in.Transcript):]), announcedMessages(o.events))

	if res.Turns != len(snap) {
		tb.Errorf("Turns = %d, the model was called %d times", res.Turns, len(snap))
	}
	if o.in.Pending != nil && len(res.Record.Turns) > 0 && res.Record.Turns[0].Turn == 0 {
		checkFinished(tb, o)
	}
	switch {
	case o.suspended:
		checkSuspended(tb, o, snap)
	case o.err == nil:
		checkSuccess(tb, o, snap)
	}
	checkRecord(tb, o)
	if lim := r.lim; lim.maxTurns > 0 && res.Turns > lim.maxTurns {
		tb.Errorf("%d turns, the limit is %d", res.Turns, lim.maxTurns)
	}
	if lim := r.lim; (lim.maxTurns > 0 || lim.maxTokens > 0) && errors.Is(o.err, harness.ErrBudgetExceeded) &&
		res.Turns != lim.maxTurns && (lim.maxTokens == 0 || tokens(res.Usage) < lim.maxTokens) {
		tb.Errorf("ErrBudgetExceeded after %d turns and %d tokens, with limits %+v", res.Turns, tokens(res.Usage), lim)
	}
}

func tokens(u llm.Usage) int {
	return u.InputTokens + u.CacheReadTokens + u.CacheWriteTokens + u.OutputTokens
}

// checkRequests checks what the model was sent: only valid conversations, the
// first one the input, every one extending the one before, none changed after
// it was sent.
func checkRequests(tb testing.TB, in harness.Input, sent, snap []llm.Request) {
	tb.Helper()
	prev := []llm.Message(in.Transcript)
	for i, req := range snap {
		msgs := req.Messages
		if err := harness.Transcript(msgs).Validate(); err != nil {
			tb.Errorf("request %d is not a valid conversation: %v", i, err)
		}
		if len(msgs) < len(prev) {
			tb.Errorf("request %d has %d messages, fewer than the %d before it", i, len(msgs), len(prev))
		} else {
			diff(tb, "earlier messages in request "+string(rune('0'+i%10)), prev, msgs[:len(prev)])
		}
		if i == 0 && in.Message.Role != "" {
			if n := len(msgs); n == 0 {
				tb.Error("the first request is empty")
			} else {
				diff(tb, "the new message at the end of the first request", in.Message, msgs[n-1])
			}
		}
		prev = msgs
		if d := cmpRequests(sent[i], req); d != "" {
			tb.Errorf("request %d was changed after it was sent (-sent +now):\n%s", i, d)
		}
	}
}

func cmpRequests(sent, snap llm.Request) string {
	// sent shares memory with the harness; snap is the copy taken when it was sent.
	return diffString(snap, sent)
}

// checkRoundTrip checks a transcript can be saved and loaded: it marshals, the
// result decodes, and encoding again gives the same bytes. Opaque blocks must
// come back byte for byte.
func checkRoundTrip(tb testing.TB, t harness.Transcript) {
	tb.Helper()
	first := mustMarshal(tb, t)
	var back harness.Transcript
	if err := json.Unmarshal(first, &back); err != nil {
		tb.Errorf("a saved transcript does not load: %v", err)
		return
	}
	if second := mustMarshal(tb, back); !bytes.Equal(first, second) {
		tb.Errorf("a transcript changed by a save and load:\n%s\n%s", first, second)
	}
	for i, m := range t {
		for j, b := range m.Content {
			if o, ok := b.(llm.Opaque); ok {
				got, isOpaque := back[i].Content[j].(llm.Opaque)
				if !isOpaque || !bytes.Equal(got.Data, o.Data) {
					tb.Errorf("opaque block %d.%d changed by a save and load", i, j)
				}
			}
		}
	}
}

// checkSuccess checks what only a finished run promises.
func checkSuccess(tb testing.TB, o observation, snap []llm.Request) {
	tb.Helper()
	res := o.res
	n := len(res.Transcript)
	if res.Final.Role != llm.RoleAssistant || n == 0 {
		tb.Errorf("Final is %+v", res.Final)
		return
	}
	diff(tb, "Final is the last message", res.Transcript[n-1], res.Final)
	checkUsage(tb, o, snap, nil)
	if string(o.finalJSON) != string(mustMarshal(tb, res.Transcript)) && o.finalJSON != nil {
		tb.Error("the transcript in EventRunDone changed while the consumer held it")
	}
}

// checkUsage checks the run's usage is the sum of the turns whose model call
// it made: its own (not a turn 0 it finished for an earlier run), and the
// pending turn it suspended in, if any.
func checkUsage(tb testing.TB, o observation, snap []llm.Request, pending *harness.Pending) {
	tb.Helper()
	res := o.res
	var sum llm.Usage
	turns := res.Record.Turns
	if len(turns) > 0 && turns[0].Turn == 0 {
		turns = turns[1:]
	}
	// The pending turn's model call is this run's unless the run suspended
	// again on the turn it resumed, before finishing it.
	resuspended := o.in.Pending != nil && (len(res.Record.Turns) == 0 || res.Record.Turns[0].Turn != 0)
	if pending != nil && !resuspended {
		turns = append(slices.Clone(turns), pending.Turn)
	}
	for _, tr := range turns {
		sum = sum.Add(tr.Usage)
	}
	diff(tb, "total usage is the sum of the turns", sum, res.Usage)
	if len(turns) != res.Turns || res.Turns != len(snap) {
		tb.Errorf("%d turns with a model call, Turns = %d, %d model calls", len(turns), res.Turns, len(snap))
	}
}

// checkSuspended checks what a run that suspended promises: a pending turn
// whose reply follows the transcript, whose calls are each finished or
// waiting (as the interrupt events said), and that survives a save and load.
func checkSuspended(tb testing.TB, o observation, snap []llm.Request) {
	tb.Helper()
	p := o.res.Pending
	if p == nil {
		tb.Error("a suspended run has no pending turn")
		return
	}
	if o.res.Final.Role != "" {
		tb.Errorf("a suspended run has a final message %+v", o.res.Final)
	}
	checkUsage(tb, o, snap, p)
	var waiting []string
	for _, ev := range o.events {
		switch ev.Kind {
		case harness.EventInterrupt:
			waiting = append(waiting, ev.Interrupt.ID)
		case harness.EventTurnDone:
			waiting = nil
		}
	}
	var ids []string
	for _, in := range p.Interrupts {
		ids = append(ids, in.ID)
	}
	slices.Sort(waiting)
	diff(tb, "the pending turn's interrupts and the interrupt events", waiting, slices.Sorted(slices.Values(ids)))
	// The reply follows the transcript, and with every call answered the
	// conversation is valid.
	reply := p.Reply()
	var results []llm.Block
	done := map[string]llm.ToolResult{}
	for _, b := range p.Done.Content {
		r := b.(llm.ToolResult)
		done[r.ToolUseID] = r
	}
	for _, b := range reply.Content {
		if u, ok := b.(llm.ToolUse); ok {
			r, ok := done[u.ID]
			if !ok {
				r = llm.ToolResult{ToolUseID: u.ID, Content: "answer"}
				if !slices.Contains(ids, u.ID) {
					tb.Errorf("call %q of the pending turn neither finished nor waits", u.ID)
				}
			}
			results = append(results, r)
		}
	}
	full := append(o.res.Transcript.Clone(), reply, llm.User(results...))
	if err := full.Validate(); err != nil {
		tb.Errorf("the pending turn does not follow the transcript: %v", err)
	}
	var back harness.Pending
	if err := json.Unmarshal(mustMarshal(tb, p), &back); err != nil {
		tb.Errorf("a saved pending turn does not load: %v", err)
	} else if !bytes.Equal(mustMarshal(tb, back), mustMarshal(tb, p)) {
		tb.Errorf("a pending turn changed by a save and load")
	}
}

// checkFinished checks the turn 0 a run finished for an earlier run: the
// pending reply, then a result for every call, the finished ones unchanged.
func checkFinished(tb testing.TB, o observation) {
	tb.Helper()
	p, tr := o.in.Pending, o.res.Record.Turns[0]
	if len(tr.Messages) != 2 {
		tb.Errorf("turn 0 has %d messages, want the reply and its results", len(tr.Messages))
		return
	}
	diff(tb, "turn 0's reply", p.Reply(), tr.Messages[0].Message)
	got := map[string]llm.ToolResult{}
	for _, b := range tr.Messages[1].Message.Content {
		r := b.(llm.ToolResult)
		got[r.ToolUseID] = r
	}
	for _, b := range p.Done.Content {
		r := b.(llm.ToolResult)
		diff(tb, "a result the suspended run had", r, got[r.ToolUseID])
	}
	diff(tb, "turn 0's usage", p.Turn.Usage, tr.Usage)
}

// checkRecord checks the record agrees with the events and the transcript, and
// that times only move forward.
func checkRecord(tb testing.TB, o observation) {
	tb.Helper()
	rec := o.res.Record
	var fromEvents []harness.TurnRecord
	for _, ev := range o.events {
		if ev.Kind == harness.EventTurnDone {
			fromEvents = append(fromEvents, *ev.TurnRecord)
		}
	}
	diff(tb, "turn records in the result and in events", fromEvents, rec.Turns)
	if rec.StartedAt.IsZero() || rec.FinishedAt.Before(rec.StartedAt) {
		tb.Errorf("run times %v .. %v", rec.StartedAt, rec.FinishedAt)
	}
	last := rec.StartedAt
	if rec.Prompt != nil {
		diff(tb, "Prompt", o.in.Message, rec.Prompt.Message)
	}
	for _, tr := range rec.Turns {
		if tr.Turn == 0 {
			// Finished for an earlier run, which made its model call.
			if n := len(tr.Messages); n != 2 || tr.Messages[1].At.Before(last) {
				tb.Errorf("turn 0: %d messages, results stamped before the run started", n)
			}
			continue
		}
		for _, tm := range []time.Time{tr.RequestedAt, tr.FirstEventAt, tr.DoneAt} {
			if tm.Before(last) {
				tb.Errorf("turn %d: time %v is before %v", tr.Turn, tm, last)
			}
			last = tm
		}
		for _, m := range tr.Messages {
			if m.At.Before(tr.DoneAt) {
				tb.Errorf("turn %d: a message stamped %v, before the reply was done at %v", tr.Turn, m.At, tr.DoneAt)
			}
		}
		calls := 0
		if len(tr.Messages) > 0 {
			for _, b := range tr.Messages[0].Message.Content {
				if _, ok := b.(llm.ToolUse); ok {
					calls++
				}
			}
		}
		if len(tr.Tools) != calls {
			tb.Errorf("turn %d: %d tool records for %d calls", tr.Turn, len(tr.Tools), calls)
		}
		for _, tool := range tr.Tools {
			if tool.Duration < 0 || tool.StartedAt.Before(tr.DoneAt) {
				tb.Errorf("turn %d: tool record %+v starts before the reply was done", tr.Turn, tool)
			}
		}
	}
	if rec.FinishedAt.Before(last) {
		tb.Errorf("the run finished at %v, before its last turn event at %v", rec.FinishedAt, last)
	}
}

// announcedMessages rebuilds what a store would have saved from events: the
// prompt, then the messages of every finished turn.
func announcedMessages(events []harness.Event) []llm.Message {
	var out []llm.Message
	for _, ev := range events {
		switch ev.Kind {
		case harness.EventRunStart:
			if ev.Prompt != nil {
				out = append(out, ev.Prompt.Message)
			}
		case harness.EventTurnDone:
			for _, m := range ev.TurnRecord.Messages {
				out = append(out, m.Message)
			}
		}
	}
	return out
}

// checkEvents checks the grammar of a run's events. A run that was stopped
// halfway (not complete) is checked as far as it went. A run that resumed a
// pending turn (resumed) starts with that turn, as turn 0.
//
//	RunStart  [turn 0]  ( TextDelta* calls TurnDone )*  ( RunDone | TextDelta* calls Suspended )
//	calls: each call: start, [policy [flagged]], done or interrupt
func checkEvents(tb testing.TB, events []harness.Event, finished, resumed bool) {
	tb.Helper()
	if len(events) == 0 {
		return
	}
	if events[0].Kind != harness.EventRunStart {
		tb.Errorf("the first event is %v, want run_start", events[0].Kind)
	}
	turn := 1
	if resumed {
		turn = 0
	}
	started := map[int]bool{}
	done := map[int]bool{}
	waits := map[int]bool{}
	decided := map[int]policy.Action{} // by the tool policy: the action, then "flagged"
	text := map[[2]int]string{}        // (turn, block) -> streamed text
	toolsBegan := false
	for i, ev := range events[1:] {
		switch ev.Kind {
		case harness.EventRunStart:
			tb.Errorf("event %d: a second run_start", i+1)
		case harness.EventTextDelta:
			if toolsBegan || turn == 0 {
				tb.Errorf("event %d: text after the tools of turn %d began", i+1, turn)
			}
			if ev.Turn != turn {
				tb.Errorf("event %d: text for turn %d during turn %d", i+1, ev.Turn, turn)
			}
			text[[2]int{ev.Turn, ev.Block}] += ev.Text
		case harness.EventToolStart:
			toolsBegan = true
			if ev.Turn != turn || started[ev.Call] || done[ev.Call] {
				tb.Errorf("event %d: tool_start for turn %d call %d (turn %d, started %v, done %v)",
					i+1, ev.Turn, ev.Call, turn, started[ev.Call], done[ev.Call])
			}
			started[ev.Call] = true
		case harness.EventToolPolicy:
			if ev.Turn != turn || turn == 0 || !started[ev.Call] || done[ev.Call] || decided[ev.Call] != "" {
				tb.Errorf("event %d: tool_policy for turn %d call %d out of place", i+1, ev.Turn, ev.Call)
			}
			if ev.Decision.Action == policy.Allow || !slices.Contains(policy.Actions(), ev.Decision.Action) ||
				ev.Decision.Reason == "" || ev.Decision.Rule == "" || ev.ToolUse.ID == "" || ev.ToolUse.Input != nil {
				tb.Errorf("event %d: tool_policy %+v", i+1, ev)
			}
			decided[ev.Call] = ev.Decision.Action
		case harness.EventToolFlagged:
			if ev.Turn != turn || done[ev.Call] || decided[ev.Call] != policy.Flag || ev.Decision.Action != policy.Flag {
				tb.Errorf("event %d: tool_flagged for turn %d call %d without its tool_policy", i+1, ev.Turn, ev.Call)
			}
			decided[ev.Call] = "flagged"
		case harness.EventToolDone, harness.EventInterrupt:
			// (What a refused call's result says is for the tests: an
			// AfterTool hook may replace it.)
			if decided[ev.Call] == policy.Flag {
				tb.Errorf("event %d: a flagged call ran without tool_flagged", i+1)
			}
			if ev.Turn != turn || !started[ev.Call] || done[ev.Call] {
				tb.Errorf("event %d: %v for turn %d call %d without a matching start (turn %d, started %v, done %v)",
					i+1, ev.Kind, ev.Turn, ev.Call, turn, started[ev.Call], done[ev.Call])
			}
			done[ev.Call] = true
			if ev.Kind == harness.EventInterrupt {
				waits[ev.Call] = true
				in := ev.Interrupt
				switch {
				case in == nil || in.ID != ev.ToolUse.ID || in.Call != ev.Call || in.Name != ev.ToolUse.Name || ev.ToolUse.Input != nil:
					tb.Errorf("event %d: interrupt %+v", i+1, ev)
				case in.Kind == harness.InterruptApproval && (decided[ev.Call] != policy.Approve || in.Decision == nil || in.Decision.Action != policy.Approve):
					tb.Errorf("event %d: an approval interrupt without the policy's approve: %+v", i+1, in)
				case in.Kind == harness.InterruptQuestion && (decided[ev.Call] == policy.Approve || decided[ev.Call] == policy.Deny):
					tb.Errorf("event %d: a question from a call the policy held or denied", i+1)
				case in.Kind != harness.InterruptApproval && in.Kind != harness.InterruptQuestion:
					tb.Errorf("event %d: interrupt kind %q", i+1, in.Kind)
				}
			}
		case harness.EventTurnDone:
			if ev.Turn != turn || ev.TurnRecord == nil || ev.TurnRecord.Turn != turn {
				tb.Errorf("event %d: turn_done for turn %d during turn %d", i+1, ev.Turn, turn)
				return
			}
			for c := range started {
				if !done[c] || waits[c] {
					tb.Errorf("event %d: turn %d ended with call %d unfinished", i+1, turn, c)
				}
			}
			if turn > 0 {
				checkStreamedText(tb, ev.TurnRecord, text)
			}
			turn++
			started, done, waits, toolsBegan = map[int]bool{}, map[int]bool{}, map[int]bool{}, false
			decided = map[int]policy.Action{}
		case harness.EventRunDone:
			if i+2 != len(events) {
				tb.Errorf("event %d: run_done is not the last event", i+1)
			}
		case harness.EventSuspended:
			if i+2 != len(events) {
				tb.Errorf("event %d: suspended is not the last event", i+1)
			}
			if len(waits) == 0 {
				tb.Errorf("event %d: suspended with no call waiting in turn %d", i+1, turn)
			}
			for c := range started {
				if !done[c] {
					tb.Errorf("event %d: suspended with call %d of turn %d unfinished", i+1, c, turn)
				}
			}
			if ev.Result == nil || ev.Result.Pending == nil {
				tb.Errorf("event %d: suspended without a pending turn", i+1)
			}
		default:
			tb.Errorf("event %d: unknown kind %v", i+1, ev.Kind)
		}
	}
	last := events[len(events)-1].Kind
	if finished && last != harness.EventRunDone && last != harness.EventSuspended {
		tb.Errorf("a successful run does not end with run_done or suspended but with %v", last)
	}
	if !finished && len(events) > 0 && (last == harness.EventRunDone || last == harness.EventSuspended) {
		tb.Errorf("%v in a run that did not succeed", last)
	}
}

// checkStreamedText checks the text deltas of a finished turn add up to the
// text blocks of the reply that was committed.
func checkStreamedText(tb testing.TB, tr *harness.TurnRecord, text map[[2]int]string) {
	tb.Helper()
	if len(tr.Messages) == 0 {
		return
	}
	want := map[[2]int]string{}
	for j, b := range tr.Messages[0].Message.Content {
		if t, ok := b.(llm.Text); ok && t.Text != "" {
			want[[2]int{tr.Turn, j}] = t.Text
		}
	}
	got := map[[2]int]string{}
	for k, v := range text {
		if k[0] == tr.Turn {
			got[k] = v
		}
	}
	diff(tb, "streamed text of turn "+string(rune('0'+tr.Turn%10)), want, got)
}
