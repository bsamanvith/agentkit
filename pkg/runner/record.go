package runner

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/policy"
	"github.com/bsamanvith/agentkit/pkg/store"
)

// Record is the JSON the runner keeps beside an entry's messages
// ([store.Entry.Record]): what is for people and bills, never for the model.
// Exactly one of its parts is set.
type Record struct {
	// Prompt is set on a prompt entry: when the user sent the message.
	Prompt *PromptRecord `json:"prompt,omitempty"`
	// Turn is set on a turn entry written for a harness turn.
	Turn *TurnRecord `json:"turn,omitempty"`
	// Canceled is set on the marker reply [Runner.Cancel] appends after a
	// run canceled before the model answered.
	Canceled *CanceledRecord `json:"canceled,omitempty"`
	// Compaction is set on a compaction entry ([Compactor]).
	Compaction *CompactionRecord `json:"compaction,omitempty"`
	// Interrupt is set on an interrupt entry ([store.KindInterrupt]): a turn
	// that waits for people.
	Interrupt *InterruptRecord `json:"interrupt,omitempty"`
}

// InterruptRecord is the record of an interrupt entry, whose one message is
// the reply whose calls wait: with it, the [harness.Pending] turn, and the
// answers given so far. The runner writes the entry when the run suspends,
// and again, whole, with each new answer: the latest one of the run counts.
type InterruptRecord struct {
	// Turn is the turn so far; its MessageTimes holds the reply's time.
	Turn *TurnRecord `json:"turn"`
	// Done is the user message of the results of the calls that finished.
	Done llm.Message `json:"done"`
	// Interrupts are the calls that wait.
	Interrupts []harness.Interrupt `json:"interrupts"`
	// Answers are those given so far, in the order they came.
	Answers []harness.Answer `json:"answers,omitempty"`
}

func interruptRecord(p harness.Pending, answers []harness.Answer) *InterruptRecord {
	return &InterruptRecord{Turn: turnRecord(&p.Turn), Done: p.Done, Interrupts: p.Interrupts, Answers: answers}
}

// PendingTurn reads an interrupt entry: the pending turn and the answers
// given so far.
func PendingTurn(e store.Entry) (harness.Pending, []harness.Answer, error) {
	r, err := DecodeRecord(e)
	if err != nil {
		return harness.Pending{}, nil, err
	}
	if e.Kind != store.KindInterrupt || r.Interrupt == nil || r.Interrupt.Turn == nil ||
		len(e.Messages) != 1 || len(r.Interrupt.Turn.MessageTimes) != 1 {
		return harness.Pending{}, nil, fmt.Errorf("entry %d is not an interrupt the runner wrote", e.Seq)
	}
	t, rec := r.Interrupt.Turn, r.Interrupt
	p := harness.Pending{
		Turn: harness.TurnRecord{
			Turn: t.Turn, RequestedAt: t.RequestedAt, FirstEventAt: t.FirstEventAt, DoneAt: t.DoneAt,
			Usage: t.Usage, Stop: t.Stop, Tools: t.Tools,
			Messages: []harness.TimedMessage{{Message: e.Messages[0], At: t.MessageTimes[0]}},
		},
		Done:       rec.Done,
		Interrupts: rec.Interrupts,
	}
	return p, rec.Answers, nil
}

// PromptRecord is the record of a prompt entry.
type PromptRecord struct {
	At time.Time `json:"at"`
	// FlushEvery is the run's [FlushEvery], kept so that a resume stores
	// its turns as the run started; 0 when the run took the runner's.
	FlushEvery int `json:"flush_every,omitempty"`
	// ToolPolicy is the run's [ToolPolicy], kept so that a resume narrows
	// the agent's policy as the run started; nil when the run set none.
	ToolPolicy *policy.Policy `json:"tool_policy,omitempty"`
	// Interaction is the run's [Interaction], kept the same way; empty when
	// the run set none.
	Interaction harness.Interaction `json:"interaction,omitempty"`
}

// CanceledRecord is the record of a cancel marker.
type CanceledRecord struct {
	At time.Time `json:"at"`
}

// TurnRecord is [harness.TurnRecord] without the messages, which are the
// entry's: MessageTimes holds when each was written.
type TurnRecord struct {
	Turn         int                  `json:"turn"`
	MessageTimes []time.Time          `json:"message_times"`
	RequestedAt  time.Time            `json:"requested_at"`
	FirstEventAt time.Time            `json:"first_event_at"`
	DoneAt       time.Time            `json:"done_at"`
	Usage        llm.Usage            `json:"usage"`
	Stop         llm.StopReason       `json:"stop"`
	Tools        []harness.ToolRecord `json:"tools,omitempty"`
}

func turnRecord(tr *harness.TurnRecord) *TurnRecord {
	out := &TurnRecord{
		Turn:        tr.Turn,
		RequestedAt: tr.RequestedAt, FirstEventAt: tr.FirstEventAt, DoneAt: tr.DoneAt,
		Usage: tr.Usage,
		Stop:  tr.Stop,
		Tools: append([]harness.ToolRecord(nil), tr.Tools...),
	}
	for _, m := range tr.Messages {
		out.MessageTimes = append(out.MessageTimes, m.At)
	}
	return out
}

// DecodeRecord reads the runner's record of an entry. An entry the runner did
// not write (an empty record) gives the zero Record.
func DecodeRecord(e store.Entry) (Record, error) {
	var r Record
	if len(e.Record) == 0 {
		return r, nil
	}
	if err := json.Unmarshal(e.Record, &r); err != nil {
		return Record{}, fmt.Errorf("entry %d: %w", e.Seq, err)
	}
	return r, nil
}

// HarnessTurn rebuilds the [harness.TurnRecord] of a turn entry: its messages
// with their times, and the turn's record. It is what [harness.EventTurnDone]
// carried when the entry was written.
func HarnessTurn(e store.Entry) (harness.TurnRecord, error) {
	r, err := DecodeRecord(e)
	if err != nil {
		return harness.TurnRecord{}, err
	}
	if e.Kind != store.KindTurn || r.Turn == nil || len(r.Turn.MessageTimes) != len(e.Messages) {
		return harness.TurnRecord{}, fmt.Errorf("entry %d is not a turn the runner wrote", e.Seq)
	}
	t := r.Turn
	out := harness.TurnRecord{
		Turn: t.Turn, RequestedAt: t.RequestedAt, FirstEventAt: t.FirstEventAt, DoneAt: t.DoneAt,
		Usage: t.Usage, Stop: t.Stop, Tools: t.Tools,
	}
	for i, m := range e.Messages {
		out.Messages = append(out.Messages, harness.TimedMessage{Message: m, At: t.MessageTimes[i]})
	}
	return out, nil
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		// Records hold only times, numbers and strings.
		panic(err)
	}
	return b
}
