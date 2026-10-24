package runner

import (
	"container/list"
	"context"
	"slices"
	"sync"

	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/policy"
	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

// view is a conversation's context as of one position of its log.
type view struct {
	seq int64
	tr  harness.Transcript
	// lastRun is the RunID of the entry at seq, "" for an empty log.
	lastRun string
	// flush is the FlushEvery of the last prompt entry's record, and
	// policy its ToolPolicy (as a later compaction carries it); policyErr
	// says that record could not be read, so the run's policy is not known.
	flush       int
	policy      *policy.Policy
	interaction harness.Interaction
	tools       *harness.ToolChoice
	policyErr   error
	// pending is the turn the log's last entry, an interrupt, says waits
	// for people; nil when the last entry is of another kind.
	pending *pendingTurn
	// last is the usage of the last turn's request since the latest
	// compaction, zero if there is none.
	last llm.Usage
	// steered is the last steering command a steer entry of run steerRun
	// delivered ([SteerRecord.Commands]), 0 if none since the context
	// was read.
	steerRun string
	steered  int64
}

// delivered reports whether the steering command seq of run was delivered
// already: a steer entry of the run names it, or a later one.
func (v view) delivered(run string, seq int64) bool {
	return v.steerRun == run && seq <= v.steered
}

// steerInto adds steering messages to the end of a transcript: into its last
// message when that is the user's (the tool results the model has not seen
// yet, or a message it has not answered), so that roles still alternate.
func steerInto(tr harness.Transcript, msgs []llm.Message) harness.Transcript {
	for _, m := range msgs {
		n := len(tr)
		if n == 0 || tr[n-1].Role != llm.RoleUser {
			tr = append(tr, m.Clone())
			continue
		}
		last := tr[n-1]
		blocks := make([]llm.Block, 0, len(last.Content)+len(m.Content))
		blocks = append(blocks, last.Content...)
		blocks = append(blocks, m.Clone().Content...)
		tr[n-1] = llm.Message{Role: last.Role, Content: blocks}
	}
	return tr
}

// pendingTurn is a run's turn that waits for people, as its latest interrupt
// entry holds it: the turn and the answers so far, or why the entry could
// not be read.
type pendingTurn struct {
	run     string
	seq     int64
	turn    harness.Pending
	answers []harness.Answer
	err     error
}

// answered returns the answer to the interrupt id, if there is one.
func (p *pendingTurn) answered(id string) (harness.Answer, bool) {
	for _, a := range p.answers {
		if a.ID == id {
			return a, true
		}
	}
	return harness.Answer{}, false
}

// complete reports whether every interrupt has its answer.
func (p *pendingTurn) complete() bool {
	for _, in := range p.turn.Interrupts {
		if _, ok := p.answered(in.ID); !ok {
			return false
		}
	}
	return true
}

func (v view) clone() view {
	v.tr = v.tr.Clone()
	if v.pending != nil {
		p := *v.pending
		p.turn = p.turn.Clone()
		p.answers = slices.Clone(p.answers)
		v.pending = &p
	}
	return v
}

func (v *view) apply(e store.Entry) {
	if e.Kind == store.KindCompaction {
		v.tr, v.last = nil, llm.Usage{}
	}
	v.seq = e.Seq
	v.lastRun = e.RunID
	v.pending = nil
	if e.Kind == store.KindInterrupt {
		// Not part of the context: the turn that finishes it is.
		turn, answers, err := PendingTurn(e)
		v.pending = &pendingTurn{run: e.RunID, seq: e.Seq, turn: turn, answers: answers, err: err}
		return
	}
	if e.Kind == store.KindSteer {
		v.tr = steerInto(v.tr, e.Messages)
		if rec, err := DecodeRecord(e); err == nil && rec.Steer != nil {
			if e.RunID != v.steerRun {
				v.steerRun, v.steered = e.RunID, 0
			}
			for _, seq := range rec.Steer.Commands {
				v.steered = max(v.steered, seq)
			}
		}
		return
	}
	for _, m := range e.Messages {
		v.tr = append(v.tr, m.Clone())
	}
	switch e.Kind {
	case store.KindPrompt:
		v.flush, v.policy, v.interaction, v.tools, v.policyErr = 0, nil, "", nil, nil
		rec, err := DecodeRecord(e)
		if err != nil {
			v.policyErr = err
		} else if rec.Prompt != nil {
			v.flush, v.policy, v.interaction, v.tools = rec.Prompt.FlushEvery, rec.Prompt.ToolPolicy, rec.Prompt.Interaction, rec.Prompt.Tools
		}
	case store.KindCompaction:
		// A cold read starts here, without the prompt: the compaction
		// carries its policy.
		rec, err := DecodeRecord(e)
		if err != nil {
			v.policyErr = err
		} else if rec.Compaction != nil {
			v.policy, v.interaction, v.tools = rec.Compaction.ToolPolicy, rec.Compaction.Interaction, rec.Compaction.Tools
		}
	case store.KindTurn:
		if rec, err := DecodeRecord(e); err == nil && rec.Turn != nil {
			v.last = rec.Turn.Usage
		}
	}
}

// endsOnUser reports whether the context's last message is the user's.
func (v view) endsOnUser() bool {
	return len(v.tr) > 0 && v.tr[len(v.tr)-1].Role == llm.RoleUser
}

type cacheKey struct{ tenant, conv string }

// cache keeps decoded contexts per conversation, keyed by the log position
// they were built from. The log only grows, so an entry is never invalidated:
// it is brought up to date by reading the log after its position, and
// appended to by the runner's own writes. It holds at most max conversations,
// dropping the least recently used.
type cache struct {
	mu  sync.Mutex
	max int
	ll  *list.List // of *cacheItem, most recent first
	m   map[cacheKey]*list.Element
}

type cacheItem struct {
	k cacheKey
	v view
}

func newCache(max int) *cache {
	return &cache{max: max, ll: list.New(), m: map[cacheKey]*list.Element{}}
}

func keyOf(ctx context.Context, conv string) (cacheKey, error) {
	t, err := tenancy.Require(ctx)
	if err != nil {
		return cacheKey{}, err
	}
	return cacheKey{t.ID(), conv}, nil
}

// load returns the conversation's context as of now: the cached one brought
// up to date, or a cold read on a miss. The result is the caller's.
func (c *cache) load(ctx context.Context, cs store.ConversationStore, conv string) (view, error) {
	k, err := keyOf(ctx, conv)
	if err != nil {
		return view{}, err
	}
	v, ok := c.get(k)
	if !ok {
		cv, err := cs.Context(ctx, conv)
		if err != nil {
			return view{}, err
		}
		for _, e := range cv.Entries {
			v.apply(e)
		}
		v.seq = cv.Seq
	} else {
		p := store.Page{FromSeq: v.seq + 1, Order: store.Ascending, Limit: store.MaxPageLimit}
		for {
			page, err := cs.History(ctx, conv, p)
			if err != nil {
				return view{}, err
			}
			for _, e := range page.Entries {
				if e.Seq == v.seq+1 {
					v.apply(e)
				}
			}
			if page.Next == "" {
				break
			}
			p = store.Page{Cursor: page.Next, Order: store.Ascending, Limit: store.MaxPageLimit}
		}
	}
	c.put(k, v)
	return v, nil
}

// appended records that e was stored, if the cached context is the one it
// extends.
func (c *cache) appended(ctx context.Context, conv string, e store.Entry) {
	k, err := keyOf(ctx, conv)
	if err != nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.m[k]; ok {
		it := el.Value.(*cacheItem)
		if it.v.seq == e.Seq-1 {
			it.v.apply(e)
		}
	}
}

func (c *cache) get(k cacheKey) (view, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.m[k]
	if !ok {
		return view{}, false
	}
	c.ll.MoveToFront(el)
	return el.Value.(*cacheItem).v.clone(), true
}

// put keeps v unless the cache already holds a newer context.
func (c *cache) put(k cacheKey, v view) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.m[k]; ok {
		it := el.Value.(*cacheItem)
		if v.seq > it.v.seq {
			it.v = v.clone()
		}
		c.ll.MoveToFront(el)
		return
	}
	c.m[k] = c.ll.PushFront(&cacheItem{k: k, v: v.clone()})
	for c.ll.Len() > c.max {
		last := c.ll.Back()
		c.ll.Remove(last)
		delete(c.m, last.Value.(*cacheItem).k)
	}
}
