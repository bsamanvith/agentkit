package runner

import (
	"context"
	"fmt"
	"net/url"

	"github.com/bsamanvith/agentkit/pkg/agentstep"
	"github.com/bsamanvith/agentkit/pkg/flow"
	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/store"
)

// StepConversation is the conversation that keeps the transcript of the
// agent step at path of flow run runID. The run ID is escaped, so no two
// steps share one, and the "step/" prefix keeps them apart from chat
// conversations as long as those never start with it.
func StepConversation(runID, path string) string {
	return "step/" + url.PathEscape(runID) + "/" + path
}

// Saver is an [agentstep.TranscriptSaver] over a conversation store: each
// step's transcript is the log of its [StepConversation], the prompt first
// and then one entry per [Saver.Append], so an append is atomic. Like every
// conversation, the transcripts are kept when the run ends.
type Saver struct {
	cs store.ConversationStore
}

var _ agentstep.TranscriptSaver = (*Saver)(nil)

// NewSaver returns a saver over cs.
func NewSaver(cs store.ConversationStore) *Saver { return &Saver{cs: cs} }

// Load implements [agentstep.TranscriptSaver]: the step's context, which is
// the whole transcript until it is compacted.
func (s *Saver) Load(ctx context.Context, runID, path string) (harness.Transcript, error) {
	cv, err := s.cs.Context(ctx, StepConversation(runID, path))
	if err != nil {
		return nil, err
	}
	var v view
	for _, e := range cv.Entries {
		v.apply(e)
	}
	return v.tr, nil
}

// Append implements [agentstep.TranscriptSaver]. Appending no messages does
// nothing. An entry at the position it read being taken by another writer is
// [store.ErrConflict].
func (s *Saver) Append(ctx context.Context, runID, path string, msgs []llm.Message) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(msgs) == 0 {
		return nil
	}
	conv := StepConversation(runID, path)
	cv, err := s.cs.Context(ctx, conv)
	if err != nil {
		return err
	}
	kind := store.KindTurn
	if cv.Seq == 0 {
		kind = store.KindPrompt
	}
	return s.cs.Append(ctx, conv, store.Entry{Seq: cv.Seq + 1, Kind: kind, Messages: msgs, RunID: runID})
}

// StepOutput is what an [AgentStep] writes to the flow state: the answer, and
// where its transcript is, so that a checkpoint stays small however many
// turns the agent took.
type StepOutput struct {
	Answer string `json:"answer"`
	// ConversationID is the step's [StepConversation], and Seq the last
	// entry of the transcript the answer ends.
	ConversationID string `json:"conversation_id"`
	Seq            int64  `json:"seq"`
}

// Transcript reads the step's transcript back from the store: every entry of
// its conversation up to out.Seq, in order.
func (s *Saver) Transcript(ctx context.Context, out StepOutput) (harness.Transcript, error) {
	var tr harness.Transcript
	p := store.Page{FromSeq: 1, Order: store.Ascending, Limit: store.MaxPageLimit}
	for {
		page, err := s.cs.History(ctx, out.ConversationID, p)
		if err != nil {
			return nil, err
		}
		for _, e := range page.Entries {
			if e.Seq > out.Seq {
				return tr, nil
			}
			tr = append(tr, e.Messages...)
		}
		if page.Next == "" {
			return tr, nil
		}
		p = store.Page{Cursor: page.Next, Order: store.Ascending, Limit: store.MaxPageLimit}
	}
}

// AgentStep is an [agentstep.Step] whose output, under its output key, is a
// [StepOutput] instead of an [agentstep.Output]: the transcript stays in the
// store, saved through a [Saver], and the flow state holds a reference to it.
// Read the transcript with [Saver.Transcript].
type AgentStep struct {
	inner *agentstep.Step
	saver *Saver
	keys  agentstep.Keys
}

// NewAgentStep returns an agent step called name, as [agentstep.New] with
// saver, whose output is a [StepOutput].
func NewAgentStep(name string, h *harness.Harness, saver *Saver, keys agentstep.Keys, opts ...agentstep.Option) (*AgentStep, error) {
	if saver == nil {
		return nil, fmt.Errorf("%w: nil saver", agentstep.ErrInvalidConfig)
	}
	inner, err := agentstep.New(name, h, saver, keys, opts...)
	if err != nil {
		return nil, err
	}
	return &AgentStep{inner: inner, saver: saver, keys: keys}, nil
}

// Name implements [flow.Namer].
func (s *AgentStep) Name() string { return s.inner.Name() }

// Run implements [flow.Step]: it runs the agent step, then replaces its
// output with the reference before the flow checkpoints the state.
func (s *AgentStep) Run(ctx context.Context, st *flow.State) error {
	if err := s.inner.Run(ctx, st); err != nil {
		return err
	}
	runID, path, ok := flow.StepPath(ctx)
	if !ok {
		return agentstep.ErrNoRun
	}
	out, err := flow.Get[agentstep.Output](st, s.keys.Output)
	if err != nil {
		return err
	}
	conv := StepConversation(runID, path)
	cv, err := s.saver.cs.Context(ctx, conv)
	if err != nil {
		return err
	}
	return flow.Set(st, s.keys.Output, StepOutput{Answer: out.Answer, ConversationID: conv, Seq: cv.Seq})
}
