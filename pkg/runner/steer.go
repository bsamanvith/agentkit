package runner

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/store"
)

// The types of the commands a chat run reads at its turn boundaries
// ([WithCommands]). Commands of other types (an API server's stop or
// cancel) are not the runner's: it reads no further than the first one, and
// leaves it and what follows it pending for its owner.
const (
	// CommandSteer carries a steering message ([SteerCommand]).
	CommandSteer = "steer"
	// CommandPause asks the run to pause at its next turn boundary
	// ([store.StatusPaused]).
	CommandPause = "pause"
	// CommandResume withdraws the pauses sent before it that the run has
	// not acted on yet. A paused run is continued by [Runner.Resume].
	CommandResume = "resume"
)

// steerData is the Data of a [CommandSteer] command.
type steerData struct {
	Message llm.Message `json:"message"`
}

// SteerCommand returns the command that steers a run with msg, a user
// message of text, under the sender's idempotency key. Send it to the run's
// command log ([store.CommandLog.SendCommand]): the run delivers it at its
// next turn boundary, after the tool results of the turn going on and before
// the next model call, with the other steering messages pending, in the
// order they were sent. A message that is not a user message of text, all
// of it non-empty, is [ErrInvalidInput].
func SteerCommand(key string, msg llm.Message) (store.Command, error) {
	if err := checkSteer(msg); err != nil {
		return store.Command{}, err
	}
	data, err := json.Marshal(steerData{Message: msg})
	if err != nil {
		return store.Command{}, fmt.Errorf("%w: %w", ErrInvalidInput, err)
	}
	return store.Command{Key: key, Type: CommandSteer, Data: data}, nil
}

// PauseCommand returns the command that pauses a run at its next turn
// boundary, under the sender's idempotency key.
func PauseCommand(key string) store.Command {
	return store.Command{Key: key, Type: CommandPause}
}

// ResumeCommand returns the command that withdraws the pauses sent before it,
// under the sender's idempotency key.
func ResumeCommand(key string) store.Command {
	return store.Command{Key: key, Type: CommandResume}
}

// checkSteer refuses a steering message that is not a user message of
// non-empty text.
func checkSteer(msg llm.Message) error {
	if msg.Role != llm.RoleUser || len(msg.Content) == 0 {
		return fmt.Errorf("%w: a steering message is a user message with content", ErrInvalidInput)
	}
	for i, b := range msg.Content {
		t, ok := b.(llm.Text)
		if !ok || t.Text == "" {
			return fmt.Errorf("%w: steering message block %d is not text", ErrInvalidInput, i)
		}
	}
	return nil
}

// boundary is what the commands pending at a turn boundary ask of a run.
type boundary struct {
	// steer are the steering messages not delivered yet, and seqs their
	// commands.
	steer []llm.Message
	seqs  []int64
	// pause says that the last of the pauses and resumes read was a pause.
	pause bool
	// through is the last command read, 0 for none: the run consumes the
	// commands up to it once it has acted on them.
	through int64
}

// any reports whether the run must stop its agent for b.
func (b boundary) any() bool { return len(b.steer) > 0 || b.pause }

// boundary reads the run's pending commands, if the runner has a command
// log: in order, up to the first that is not the runner's. Those that ask
// nothing of the run now (a resume, steering delivered already) are consumed
// at once when nothing else is to be done. A failed read is no boundary: the
// next one reads again.
func (x *exec) boundary(ctx context.Context, v *view) boundary {
	var b boundary
	if x.r.cmds == nil || !x.isChat || ctx.Err() != nil {
		return b
	}
	cmds, err := x.r.cmds.PendingCommands(ctx, x.run.ID, store.MaxPageLimit)
	if err != nil {
		return b
	}
	for _, c := range cmds {
		switch c.Type {
		case CommandSteer:
			if v.delivered(x.run.ID, c.Seq) {
				break // stored before a crash took its consumption
			}
			var d steerData
			if err := json.Unmarshal(c.Data, &d); err != nil || checkSteer(d.Message) != nil {
				break // not one SteerCommand made: dropped
			}
			b.steer = append(b.steer, d.Message)
			b.seqs = append(b.seqs, c.Seq)
		case CommandPause:
			b.pause = true
		case CommandResume:
			b.pause = false
		default:
			return b
		}
		b.through = c.Seq
	}
	if !b.any() && b.through > 0 {
		_ = x.r.cmds.ConsumeCommands(ctx, x.run.ID, x.owner, b.through) // read again if it failed
	}
	return b
}

// deliver acts on b at a turn boundary, the run's turns all stored: it
// stores the steering messages as a steer entry and tells the consumer
// ([EventSteered]), then consumes the commands read. It reports whether the
// run pauses now: a pause whose command could not be consumed is not acted
// on (the next boundary reads it again), so that a pause is never acted on
// twice.
func (x *exec) deliver(ctx context.Context, t *turns, b boundary) (paused bool, err error) {
	r := x.r
	if len(b.steer) > 0 {
		msgs := make([]llm.Message, len(b.steer))
		for i, m := range b.steer {
			msgs[i] = m.Clone()
		}
		seq, err := r.append(ctx, x.run.ConversationID, t.v, store.Entry{
			Kind:     store.KindSteer,
			Messages: msgs,
			RunID:    x.run.ID,
			Record:   mustJSON(Record{Steer: &SteerRecord{At: r.clk.Now(), Commands: b.seqs}}),
		})
		if err != nil {
			t.failed = true
			return false, err
		}
		out := make([]llm.Message, len(msgs))
		for i, m := range msgs {
			out[i] = m.Clone()
		}
		if !t.emit(Event{Kind: EventSteered, Seq: seq, Steer: out}) {
			return false, errStopped
		}
		t.emit(Event{Kind: EventSaved, Seq: seq})
	}
	if b.through == 0 {
		return false, nil
	}
	if err := r.cmds.ConsumeCommands(ctx, x.run.ID, x.owner, b.through); err != nil {
		return false, nil // the steering is stored; the run goes on
	}
	if b.pause && x.resumedSince(ctx) {
		return false, nil
	}
	return b.pause, nil
}

// resumedSince reports whether a resume was sent after the pause the run
// has just consumed, before the run is paused: one that a request sent
// while the run still showed running, which would otherwise wait unread
// with the run paused. The pauses and resumes pending are consumed, the
// last of them deciding, as at a boundary. A resume sent after this read
// finds the run paused (but for the one write that pauses it), and its
// sender continues it.
func (x *exec) resumedSince(ctx context.Context) bool {
	cmds, err := x.r.cmds.PendingCommands(ctx, x.run.ID, store.MaxPageLimit)
	if err != nil {
		return false
	}
	var through int64
	resumed := false
	for _, c := range cmds {
		if c.Type != CommandPause && c.Type != CommandResume {
			break // steering, or not the runner's: read at the next boundary
		}
		through, resumed = c.Seq, c.Type == CommandResume
	}
	if through == 0 || x.r.cmds.ConsumeCommands(ctx, x.run.ID, x.owner, through) != nil {
		return false
	}
	return resumed
}

// continuable reports whether a turn leaves the run where the model is
// called next, or where it has answered (a boundary either way), rather than
// failing (a refusal, the output limit).
func continuable(tr *harness.TurnRecord) bool {
	return tr.Turn == 0 || tr.Stop.Kind == llm.StopToolUse || tr.Stop.Kind == llm.StopEndTurn
}

// final reports whether a turn is the model's answer: it ended its turn.
func final(tr *harness.TurnRecord) bool {
	return tr.Turn != 0 && tr.Stop.Kind == llm.StopEndTurn
}
