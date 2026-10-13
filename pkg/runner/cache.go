package runner

import (
	"container/list"
	"context"
	"sync"

	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

// view is a conversation's context as of one position of its log.
type view struct {
	seq int64
	tr  harness.Transcript
	// lastRun is the RunID of the entry at seq, "" for an empty log.
	lastRun string
	// flush is the FlushEvery of the last prompt entry's record.
	flush int
}

func (v view) clone() view {
	v.tr = v.tr.Clone()
	return v
}

func (v *view) apply(e store.Entry) {
	if e.Kind == store.KindCompaction {
		v.tr = nil
	}
	for _, m := range e.Messages {
		v.tr = append(v.tr, m.Clone())
	}
	v.seq = e.Seq
	v.lastRun = e.RunID
	if e.Kind == store.KindPrompt {
		v.flush = 0
		if rec, err := DecodeRecord(e); err == nil && rec.Prompt != nil {
			v.flush = rec.Prompt.FlushEvery
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
