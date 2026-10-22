package inmem

import (
	"context"
	"slices"
	"sync"

	"github.com/bsamanvith/agentkit/pkg/memory"
	"github.com/bsamanvith/agentkit/pkg/memory/internal/keyword"
	"github.com/bsamanvith/agentkit/pkg/memory/internal/semantic"
)

var _ memory.Index = (*Index)(nil)

// Index is the reference memory.Index: items in a map, searched with a scan
// of the scope, by keyword (BM25) and, with an embedder
// (memory.IndexConfig.Embedder), by cosine similarity and by both fused. It
// is for tests, demos and agents with up to some tens of thousands of items
// per scope, lost when the process ends.
type Index struct {
	cfg    memory.IndexConfig
	mu     sync.RWMutex
	scopes map[memory.Owner]map[string]entry // by item ID
}

// entry is an item, its words and its vector (zero without an embedder).
type entry struct {
	doc keyword.Doc
	vec semantic.Vector
}

// NewIndex returns an empty index, or memory.ErrInvalid for a bad cfg.
func NewIndex(cfg memory.IndexConfig) (*Index, error) {
	cfg, err := cfg.Resolve()
	if err != nil {
		return nil, err
	}
	return &Index{cfg: cfg, scopes: map[memory.Owner]map[string]entry{}}, nil
}

// Modes implements memory.Index: the configured modes.
func (x *Index) Modes() []memory.Mode { return slices.Clone(x.cfg.Modes) }

func (x *Index) owner(ctx context.Context) (memory.Owner, error) {
	if err := ctx.Err(); err != nil {
		return memory.Owner{}, err
	}
	return memory.OwnerOf(ctx, x.cfg.Scope)
}

func (x *Index) full(o memory.Owner, id string) error {
	scope := x.scopes[o]
	if _, ok := scope[id]; !ok && len(scope) >= x.cfg.MaxItems {
		return &memory.LimitError{Limit: "items", Max: int64(x.cfg.MaxItems), Want: int64(len(scope) + 1)}
	}
	return nil
}

// Put implements memory.Index. With an embedder, the text is embedded
// first; an embedder's failure stores nothing.
func (x *Index) Put(ctx context.Context, it memory.Item) error {
	o, err := x.owner(ctx)
	if err != nil {
		return err
	}
	it, err = memory.CheckItem(it, x.cfg)
	if err != nil {
		return err
	}
	e := entry{doc: keyword.Doc{Item: it, Tokens: memory.Tokenize(it.Text)}}
	if x.cfg.Embedder != nil {
		// No embedding (a paid call) for an item that cannot be stored.
		x.mu.RLock()
		err := x.full(o, it.ID)
		x.mu.RUnlock()
		if err != nil {
			return err
		}
		vs, err := semantic.Embed(ctx, x.cfg.Embedder, []string{it.Text}, memory.PurposeDocument)
		if err != nil {
			return err
		}
		e.vec = vs[0]
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	if err := x.full(o, it.ID); err != nil {
		return err
	}
	scope := x.scopes[o]
	if scope == nil {
		scope = map[string]entry{}
		x.scopes[o] = scope
	}
	scope[it.ID] = e
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
	var kq *memory.KeywordQuery
	switch q.Mode {
	case memory.ModeKeyword:
		p, err := memory.ParseKeywordQuery(q.Text)
		if err != nil {
			return nil, err
		}
		kq = &p
	case memory.ModeHybrid:
		if text := memory.HybridKeywordText(q.Text); text != "" {
			p, err := memory.ParseKeywordQuery(text)
			if err != nil {
				return nil, err
			}
			kq = &p
		}
	}
	var qv semantic.Vector
	if q.Mode != memory.ModeKeyword {
		vs, err := semantic.Embed(ctx, x.cfg.Embedder, []string{q.Text}, memory.PurposeQuery)
		if err != nil {
			return nil, err
		}
		qv = vs[0]
	}
	x.mu.RLock()
	defer x.mu.RUnlock()
	scope := x.scopes[o]
	switch q.Mode {
	case memory.ModeKeyword:
		return keywordRank(scope, *kq, q.Filter, q.K), nil
	case memory.ModeSemantic:
		return semanticRank(scope, qv, q.Filter, q.K)
	}
	sem, err := semanticRank(scope, qv, q.Filter, memory.HybridDepth)
	if err != nil {
		return nil, err
	}
	var kw []memory.Result
	if kq != nil {
		kw = keywordRank(scope, *kq, q.Filter, memory.HybridDepth)
	}
	return semantic.Fuse(kw, sem, q.K), nil
}

func keywordRank(scope map[string]entry, kq memory.KeywordQuery, filter map[string]string, k int) []memory.Result {
	parts := keyword.Parts(kq)
	st := keyword.Stats{Docs: len(scope), DF: make([]int, len(parts))}
	docs := make([]keyword.Doc, 0, len(scope))
	for _, e := range scope {
		d := e.doc
		st.Tokens += len(d.Tokens)
		for i, p := range parts {
			if keyword.Count(d.Tokens, p) > 0 {
				st.DF[i]++
			}
		}
		docs = append(docs, d)
	}
	return keyword.Rank(kq, docs, st, filter, k)
}

func semanticRank(scope map[string]entry, qv semantic.Vector, filter map[string]string, k int) ([]memory.Result, error) {
	docs := make([]semantic.Doc, 0, len(scope))
	for _, e := range scope {
		docs = append(docs, semantic.Doc{Item: e.doc.Item, Vec: e.vec})
	}
	return semantic.Rank(qv, docs, filter, k)
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
	e, ok := x.scopes[o][id]
	if !ok {
		return memory.Item{}, memory.ErrItemNotFound
	}
	return e.doc.Item.Clone(), nil
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
