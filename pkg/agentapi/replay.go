package agentapi

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/runner"
	"github.com/bsamanvith/agentkit/pkg/store"
)

// Replaying a run from the store: the events its stream had, rebuilt from the
// entries it wrote (its prompt, and one entry per turn), so that a run whose
// events this server does not hold (it ran in another process, before a
// restart, or its events were let go) still shows them all.
//
// A replayed stream says what the run did, not how it streamed: a turn's text
// comes as one text_delta per text block, and each tool call is followed at
// once by its result. Turns not stored yet (see runner's FlushEvery) are not
// in it, nor are retrying events.

// replayed is a run rebuilt from its entries.
type replayed struct {
	events []Event // run_start and the turns' events
	turns  int
	usage  llm.Usage
	final  *llm.Message // the last turn's reply; nil if the run has no turn
	// rewind, when set, is the event an execution resuming the run sends
	// first: the log has turns, or a turn's beginning, that the run did not
	// keep (see [TypeRewind]). turns, usage and final are the kept turns'.
	rewind *Event
	// interrupts is set when the run's last stored event is an interrupt:
	// a chat run that waits for input.
	interrupts bool
}

// runEntries returns the entries the run wrote, in order. They are read from
// the run's creation on, and the reading stops at the first entry of another
// run after the run's own.
func (s *Server) runEntries(ctx context.Context, run store.Run) ([]store.Entry, error) {
	var out []store.Entry
	p := store.Page{Order: store.Ascending, FromTime: run.CreatedAt, Limit: store.MaxPageLimit}
	for {
		page, err := s.store.History(ctx, run.ConversationID, p)
		if err != nil {
			return nil, fmt.Errorf("reading the log of conversation %q: %w", run.ConversationID, err)
		}
		for _, e := range page.Entries {
			switch {
			case e.RunID == run.ID:
				out = append(out, e)
			case len(out) > 0:
				return out, nil
			}
		}
		if page.Next == "" {
			return out, nil
		}
		p = store.Page{Order: store.Ascending, Cursor: page.Next, Limit: store.MaxPageLimit}
	}
}

// replay rebuilds the run's events from its entries.
func replay(run store.Run, entries []store.Entry) (replayed, error) {
	var r replayed
	at := run.CreatedAt
	add := func(e Event) {
		e.V, e.RunID, e.Seq = WireVersion, run.ID, len(r.events)
		if e.At.Before(at) {
			e.At = at
		}
		at = e.At
		r.events = append(r.events, e)
	}
	start := Event{Type: TypeRunStart, ConversationID: run.ConversationID, At: run.CreatedAt}
	if len(entries) > 0 && entries[0].Kind == store.KindPrompt {
		rec, err := runner.DecodeRecord(entries[0])
		if err != nil {
			return r, err
		}
		p := TimedMessage{At: entries[0].CreatedAt, Message: entries[0].Messages[0]}
		if rec.Prompt != nil {
			p.At = rec.Prompt.At
		}
		start.Prompt, start.At = &p, p.At
	}
	add(start)
	for _, e := range entries {
		if e.Kind != store.KindTurn {
			continue
		}
		if rec, err := runner.DecodeRecord(e); err == nil && rec.Canceled != nil {
			continue // the marker of a run canceled before it answered: no event announced it
		}
		tr, err := runner.HarnessTurn(e)
		if err != nil {
			return r, fmt.Errorf("entry %d of conversation %q: %w", e.Seq, run.ConversationID, err)
		}
		r.turns++
		tr.Turn = r.turns
		for _, ev := range turnEvents(tr) {
			add(ev)
		}
		r.usage = r.usage.Add(tr.Usage)
		reply := tr.Messages[0].Message
		r.final = &reply
	}
	return r, nil
}

// turnEvents are a stored turn's events: its text, its tool calls each with
// its result, and turn_done.
func turnEvents(tr harness.TurnRecord) []Event {
	var out []Event
	reply := tr.Messages[0].Message
	var results map[string]llm.ToolResult
	if len(tr.Messages) > 1 {
		results = map[string]llm.ToolResult{}
		for _, b := range tr.Messages[1].Message.Content {
			if res, ok := b.(llm.ToolResult); ok {
				results[res.ToolUseID] = res
			}
		}
	}
	call := 0
	for i, b := range reply.Content {
		switch b := b.(type) {
		case llm.Text:
			out = append(out, Event{Type: TypeTextDelta, Turn: tr.Turn, At: tr.FirstEventAt, Block: i, Text: b.Text})
		case llm.ToolUse:
			started, took := tr.DoneAt, time.Duration(0)
			if j := slices.IndexFunc(tr.Tools, func(t harness.ToolRecord) bool { return t.ToolUseID == b.ID }); j >= 0 {
				started, took = tr.Tools[j].StartedAt, tr.Tools[j].Duration
			}
			out = append(out, Event{Type: TypeToolCall, Turn: tr.Turn, At: started, Call: call, ToolUseID: b.ID, Name: b.Name, Input: b.Input})
			if res, ok := results[b.ID]; ok {
				out = append(out, Event{
					Type: TypeToolResult, Turn: tr.Turn, At: started.Add(took), Call: call, ToolUseID: b.ID,
					Content: res.Content, IsError: res.IsError, DurationMS: ms(took),
				})
			}
			call++
		}
	}
	rec := turnRecord(tr)
	return append(out, Event{Type: TypeTurnDone, Turn: tr.Turn, At: tr.DoneAt, Record: &rec})
}

// ending is the last event of a replayed run, from its status: nil for a run
// that is going on (its lease is held). now is the time of a run that has
// not finished.
func ending(run store.Run, r replayed, now time.Time) *Event {
	at := run.FinishedAt
	if at.IsZero() {
		at = now
	}
	usage := r.usage
	e := &Event{V: WireVersion, Type: TypeRunError, RunID: run.ID, At: at, Usage: &usage, Turns: r.turns}
	switch run.Status {
	case store.StatusCompleted:
		e.Type = TypeRunDone
		if r.final != nil {
			final := *r.final
			e.Answer, e.Final = final.Text(), &final
		}
	case store.StatusFailed:
		e.Error = &Error{Code: CodeRunFailed, Message: run.Error, Retryable: run.Retryable}
	case store.StatusCanceled:
		e.Error = &Error{Code: CodeCancelled, Message: "the run was canceled"}
	case store.StatusRejected:
		e.Error = &Error{Code: CodeRejected, Message: run.Error}
	case store.StatusSuspended:
		e.Error = suspendedError(run, r.interrupts)
	default:
		if held(run, now) {
			return nil
		}
		e.Error = interruptedError(run.ID)
	}
	return e
}

// suspendedError is the error of a suspended run: a flow run waits for
// decisions, a chat run with interrupts for input.
func suspendedError(run store.Run, interrupts bool) *Error {
	if interrupts {
		return &Error{Code: CodeSuspended, Message: "the run waits for input (POST /v1/runs/" + run.ID + "/input) to " + strings.Join(run.Pending, ", ")}
	}
	return &Error{Code: CodeSuspended, Message: "the run waits for a decision at " + strings.Join(run.Pending, ", ")}
}

func interruptedError(runID string) *Error {
	return &Error{Code: CodeRunInterrupted, Message: "the run stopped before it finished; resume it (POST /v1/runs/" + runID + "/resume) or send a new message"}
}

// held reports whether a process holds the run's lease at now.
func held(run store.Run, now time.Time) bool {
	return run.Lease.Owner != "" && now.Before(run.Lease.Expires)
}

// conversationMessages reads a conversation's messages, each with when it
// was written: the times the runner keeps in its records, else the entry's.
// Compactions are left out: their messages stand in for earlier ones, for
// the model.
func (s *Server) conversationMessages(ctx context.Context, id string) ([]TimedMessage, error) {
	out := []TimedMessage{}
	p := store.Page{Order: store.Ascending, Limit: store.MaxPageLimit}
	for {
		page, err := s.store.History(ctx, id, p)
		if err != nil {
			return nil, err
		}
		for _, e := range page.Entries {
			switch e.Kind {
			case store.KindCompaction:
				continue // it stands in for messages already listed, for the model
			case store.KindInterrupt:
				continue // a pending turn: its turn entry lists its messages once it is done
			}
			times := make([]time.Time, len(e.Messages))
			for i := range times {
				times[i] = e.CreatedAt
			}
			rec, err := runner.DecodeRecord(e)
			if err != nil {
				return nil, fmt.Errorf("entry %d: %w", e.Seq, err)
			}
			switch {
			case rec.Prompt != nil:
				times[0] = rec.Prompt.At
			case rec.Turn != nil && len(rec.Turn.MessageTimes) == len(times):
				copy(times, rec.Turn.MessageTimes)
			case rec.Canceled != nil:
				times[0] = rec.Canceled.At
			}
			for i, m := range e.Messages {
				out = append(out, TimedMessage{At: times[i], Message: m})
			}
		}
		if page.Next == "" {
			return out, nil
		}
		p = store.Page{Order: store.Ascending, Cursor: page.Next, Limit: store.MaxPageLimit}
	}
}
