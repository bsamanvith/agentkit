package inmem

import (
	"context"
	"sync"

	"github.com/bsamanvith/agentkit/pkg/memory"
	"github.com/bsamanvith/agentkit/pkg/memory/internal/keyword"
)

var _ memory.Index = (*Index)(nil)

// Index is the reference memory.Index: items in a map, searched by keyword
// (BM25) with a scan of the scope. It is for tests, demos and agents with up
// to some tens of thousands of items per scope, lost when the process ends.
type Index struct {
	cfg    memory.IndexConfig
	mu     sync.RWMutex
	scopes map[memory.Owner]map[string]keyword.Doc // id -> item and its words
}

// NewIndex returns an empty index, or memory.ErrInvalid for a bad cfg.
func NewIndex(cfg memory.IndexConfig) (*Index, error) {
	cfg, err := cfg.Resolve()
	if err != nil {
		return nil, err
	}
	return &Index{cfg: cfg, scopes: map[memory.Owner]map[string]keyword.Doc{}}, nil
}

// Modes implements memory.Index: keyword only.
func (x *Index) Modes() []memory.Mode { return []memory.Mode{memory.ModeKeyword} }

func (x *Index) owner(ctx context.Context) (memory.Owner, error) {
	if err := ctx.Err(); err != nil {
		return memory.Owner{}, err
	}
	return memory.OwnerOf(ctx, x.cfg.Scope)
}

// Put implements memory.Index.
func (x *Index) Put(ctx context.Context, it memory.Item) error {
	o, err := x.owner(ctx)
	if err != nil {
		return err
	}
	it, err = memory.CheckItem(it, x.cfg)
	if err != nil {
		return err
	}
	doc := keyword.Doc{Item: it, Tokens: memory.Tokenize(it.Text)}
	x.mu.Lock()
	defer x.mu.Unlock()
	scope := x.scopes[o]
	if _, ok := scope[it.ID]; !ok && len(scope) >= x.cfg.MaxItems {
		return &memory.LimitError{Limit: "items", Max: int64(x.cfg.MaxItems), Want: int64(len(scope) + 1)}
	}
	if scope == nil {
		scope = map[string]keyword.Doc{}
		x.scopes[o] = scope
	}
	scope[it.ID] = doc
	return nil
}

// Search implements memory.Index.
func (x *Index) Search(ctx context.Context, q memory.Query) ([]memory.Result, error) {
	o, err := x.owner(ctx)
	if err != nil {
		return nil, err
	}
	q, err = memory.ResolveQuery(q, x.Modes())
	if err != nil {
		return nil, err
	}
	kq, err := memory.ParseKeywordQuery(q.Text)
	if err != nil {
		return nil, err
	}
	parts := keyword.Parts(kq)
	x.mu.RLock()
	defer x.mu.RUnlock()
	scope := x.scopes[o]
	st := keyword.Stats{Docs: len(scope), DF: make([]int, len(parts))}
	docs := make([]keyword.Doc, 0, len(scope))
	for _, d := range scope {
		st.Tokens += len(d.Tokens)
		for i, p := range parts {
			if keyword.Count(d.Tokens, p) > 0 {
				st.DF[i]++
			}
		}
		docs = append(docs, d)
	}
	return keyword.Rank(kq, docs, st, q.Filter, q.K), nil
}

// Get implements memory.Index.
func (x *Index) Get(ctx context.Context, id string) (memory.Item, error) {
	o, err := x.owner(ctx)
	if err != nil {
		return memory.Item{}, err
	}
	if err := memory.CheckID(id); err != nil {
		return memory.Item{}, err
	}
	x.mu.RLock()
	defer x.mu.RUnlock()
	d, ok := x.scopes[o][id]
	if !ok {
		return memory.Item{}, memory.ErrItemNotFound
	}
	return d.Item.Clone(), nil
}

// Delete implements memory.Index.
func (x *Index) Delete(ctx context.Context, id string) error {
	o, err := x.owner(ctx)
	if err != nil {
		return err
	}
	if err := memory.CheckID(id); err != nil {
		return err
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	scope := x.scopes[o]
	if _, ok := scope[id]; !ok {
		return memory.ErrItemNotFound
	}
	delete(scope, id)
	if len(scope) == 0 {
		delete(x.scopes, o)
	}
	return nil
}
