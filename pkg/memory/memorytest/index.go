package memorytest

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"math"
	"math/rand/v2"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/memory"
	"github.com/bsamanvith/agentkit/pkg/memory/internal/keyword"
)

// NewIndex makes an empty index with cfg for one test.
type NewIndex func(t *testing.T, cfg memory.IndexConfig) memory.Index

// RunIndex runs the conformance suite of memory.Index against the indexes
// newIndex makes. Every search a test makes goes through [CheckSearch], so
// the invariants of a result list (at most K, ordered, no duplicates, the
// filter kept, each item what Get returns, hybrid scores the fusion of the
// other two modes) hold everywhere. The keyword tests and the
// [KeywordRelevance] set run when the index has ModeKeyword.
//
// The suite runs three times: as newIndex makes indexes (keyword only,
// without an embedder), and in "Semantic" and "Hybrid", where it puts a
// [FakeEmbedder] and the modes [semantic] and [hybrid, keyword, semantic] in
// the config; there the semantic tests, [SemanticRelevance] and
// [HybridRelevance] run too, and a backend must honor
// memory.IndexConfig.Embedder and Modes.
func RunIndex(t *testing.T, newIndex NewIndex) {
	t.Helper()
	runIndex(t, newIndex)
	for _, v := range []struct {
		name  string
		modes []memory.Mode
	}{
		{"Semantic", []memory.Mode{memory.ModeSemantic}},
		{"Hybrid", []memory.Mode{memory.ModeHybrid, memory.ModeKeyword, memory.ModeSemantic}},
	} {
		t.Run(v.name, func(t *testing.T) {
			t.Parallel()
			runIndex(t, WithEmbedder(newIndex, v.modes))
		})
	}
}

// WithEmbedder returns a NewIndex that makes indexes of newIndex with modes
// and, unless the test gives its own, a new FakeEmbedder (DefaultFakeModel,
// DefaultFakeDim) each. It fails the test when an index does not have
// exactly those modes.
func WithEmbedder(newIndex NewIndex, modes []memory.Mode) NewIndex {
	return func(t *testing.T, cfg memory.IndexConfig) memory.Index {
		t.Helper()
		if cfg.Embedder == nil {
			cfg.Embedder = NewFakeEmbedder(DefaultFakeModel, DefaultFakeDim)
		}
		if cfg.Modes == nil {
			cfg.Modes = modes
		}
		x := newIndex(t, cfg)
		if got := x.Modes(); !slices.Equal(got, cfg.Modes) {
			t.Fatalf("an index made with modes %v has modes %v: the backend ignores IndexConfig.Modes", cfg.Modes, got)
		}
		return x
	}
}

func runIndex(t *testing.T, newIndex NewIndex) {
	t.Helper()
	t.Run("PutGetDelete", func(t *testing.T) { t.Parallel(); testIndexBasics(t, newIndex) })
	t.Run("InvalidInput", func(t *testing.T) { t.Parallel(); testIndexInvalid(t, newIndex) })
	t.Run("Limits", func(t *testing.T) { t.Parallel(); testIndexLimits(t, newIndex) })
	t.Run("Isolation", func(t *testing.T) { t.Parallel(); testIndexIsolation(t, newIndex) })
	t.Run("UpsertReplaces", func(t *testing.T) { t.Parallel(); testIndexUpsert(t, newIndex) })
	t.Run("DeleteRemovesFromResults", func(t *testing.T) { t.Parallel(); testIndexDelete(t, newIndex) })
	t.Run("KHonored", func(t *testing.T) { t.Parallel(); testIndexK(t, newIndex) })
	t.Run("StableTies", func(t *testing.T) { t.Parallel(); testIndexTies(t, newIndex) })
	t.Run("FiltersExact", func(t *testing.T) { t.Parallel(); testIndexFilters(t, newIndex) })
	t.Run("UnsupportedMode", func(t *testing.T) { t.Parallel(); testIndexModes(t, newIndex) })
	t.Run("Canceled", func(t *testing.T) { t.Parallel(); testIndexCanceled(t, newIndex) })
	t.Run("Concurrent", func(t *testing.T) { t.Parallel(); testIndexConcurrent(t, newIndex) })
	modes := newIndex(t, memory.IndexConfig{}).Modes()
	if slices.Contains(modes, memory.ModeKeyword) {
		t.Run("KeywordSyntax", func(t *testing.T) { t.Parallel(); testKeywordSyntax(t, newIndex) })
		t.Run("KeywordRelevance", func(t *testing.T) {
			t.Parallel()
			RunRelevance(t, newIndex(t, memory.IndexConfig{}), KeywordRelevance())
		})
	}
	if slices.Contains(modes, memory.ModeSemantic) {
		t.Run("SemanticScores", func(t *testing.T) { t.Parallel(); testSemanticScores(t, newIndex) })
		t.Run("EmbedderCalls", func(t *testing.T) { t.Parallel(); testEmbedderCalls(t, newIndex) })
		t.Run("EmbedderFails", func(t *testing.T) { t.Parallel(); testEmbedderFails(t, newIndex) })
		t.Run("EmbedderMismatch", func(t *testing.T) { t.Parallel(); testEmbedderLies(t, newIndex) })
		t.Run("SemanticUpsert", func(t *testing.T) { t.Parallel(); testSemanticUpsert(t, newIndex) })
		t.Run("SemanticRelevance", func(t *testing.T) {
			t.Parallel()
			RunRelevance(t, newIndex(t, memory.IndexConfig{}), SemanticRelevance())
		})
	}
	if slices.Contains(modes, memory.ModeHybrid) {
		t.Run("HybridRelevance", func(t *testing.T) {
			t.Parallel()
			RunRelevance(t, newIndex(t, memory.IndexConfig{}), HybridRelevance())
		})
	}
	t.Run("Simulation", func(t *testing.T) { t.Parallel(); testIndexSimulation(t, newIndex) })
}

// CheckSearch runs q on x in ctx and checks the invariants of its results:
// at most K (DefaultK for 0), ordered best first (score, then the newest,
// then ID), each ID once, each in q's mode (the index's default when q has
// none), each matching q's filter, and each item exactly what Get returns.
// It returns the results and the error. Each result's Found is its mode
// (in a hybrid search, keyword, semantic or both); semantic scores are
// cosines, within [-1, 1], with no highlights. A hybrid search on an index
// that also has the other two modes is compared with the fusion of their
// searches (see memory.ModeHybrid), worked out here.
func CheckSearch(ctx context.Context, t testing.TB, x memory.Index, q memory.Query) ([]memory.Result, error) {
	t.Helper()
	return checkSearch(ctx, t, x, q, true)
}

// checkSearch is CheckSearch; fuse false skips the comparison of a hybrid
// search with the other modes' (whose items may change meanwhile in a test
// of concurrent writes).
func checkSearch(ctx context.Context, t testing.TB, x memory.Index, q memory.Query, fuse bool) ([]memory.Result, error) {
	t.Helper()
	res, err := x.Search(ctx, q)
	if err != nil {
		if res != nil {
			t.Errorf("Search(%+v) failed with %v and returned results %v", q, err, res)
		}
		return nil, err
	}
	k := q.K
	if k == 0 {
		k = memory.DefaultK
	}
	mode := q.Mode
	if mode == "" {
		mode = x.Modes()[0]
	}
	if len(res) > k {
		t.Errorf("Search(%+v): %d results, K is %d", q, len(res), k)
	}
	seen := map[string]bool{}
	for i, r := range res {
		if seen[r.Item.ID] {
			t.Errorf("Search(%+v): %q twice", q, r.Item.ID)
		}
		seen[r.Item.ID] = true
		if i > 0 && keyword.Compare(res[i-1], r) >= 0 {
			t.Errorf("Search(%+v): result %d (%q, %v) not after %q (%v)", q, i, r.Item.ID, r.Score, res[i-1].Item.ID, res[i-1].Score)
		}
		if r.Mode != mode {
			t.Errorf("Search(%+v): result %q has mode %q, want %q", q, r.Item.ID, r.Mode, mode)
		}
		if !memory.MatchesFilter(r.Item.Metadata, q.Filter) {
			t.Errorf("Search(%+v): result %q has metadata %v, outside the filter", q, r.Item.ID, r.Item.Metadata)
		}
		got, err := x.Get(ctx, r.Item.ID)
		if err != nil || !SameItem(got, r.Item) {
			t.Errorf("Search(%+v): result %+v, but Get = %+v, %v", q, r.Item, got, err)
		}
		checkFound(t, q, mode, r)
	}
	modes := x.Modes()
	if fuse && mode == memory.ModeHybrid && slices.Contains(modes, memory.ModeKeyword) && slices.Contains(modes, memory.ModeSemantic) {
		checkFusion(ctx, t, x, q, k, res)
	}
	return res, nil
}

func checkFound(t testing.TB, q memory.Query, mode memory.Mode, r memory.Result) {
	t.Helper()
	switch mode {
	case memory.ModeHybrid:
		if !slices.Equal(r.Found, []memory.Mode{memory.ModeKeyword}) && !slices.Equal(r.Found, []memory.Mode{memory.ModeSemantic}) &&
			!slices.Equal(r.Found, []memory.Mode{memory.ModeKeyword, memory.ModeSemantic}) {
			t.Errorf("Search(%+v): hybrid result %q found by %v, want keyword, semantic or both in that order", q, r.Item.ID, r.Found)
		}
	default:
		if !slices.Equal(r.Found, []memory.Mode{mode}) {
			t.Errorf("Search(%+v): result %q found by %v, want [%s]", q, r.Item.ID, r.Found, mode)
		}
	}
	if mode == memory.ModeSemantic {
		if r.Score < -1-1e-5 || r.Score > 1+1e-5 || math.IsNaN(r.Score) {
			t.Errorf("Search(%+v): semantic result %q has score %v, outside [-1, 1]: are the vectors normalized?", q, r.Item.ID, r.Score)
		}
		if r.Highlights != nil {
			t.Errorf("Search(%+v): semantic result %q has highlights %v", q, r.Item.ID, r.Highlights)
		}
	}
}

// checkFusion compares hybrid results with the reciprocal rank fusion
// (k = 60) of the top MaxK of a keyword and a semantic search, worked out
// here: ranks from 1, tied scores sharing the rank of the first.
func checkFusion(ctx context.Context, t testing.TB, x memory.Index, q memory.Query, k int, got []memory.Result) {
	t.Helper()
	var lists [2][]memory.Result
	if text := memory.HybridKeywordText(q.Text); text != "" {
		kw, err := checkSearch(ctx, t, x, memory.Query{Text: text, K: memory.MaxK, Filter: q.Filter, Mode: memory.ModeKeyword}, false)
		if err != nil {
			t.Errorf("hybrid Search(%+v): the keyword search of %q failed: %v", q, text, err)
			return
		}
		lists[0] = kw
	}
	sem, err := checkSearch(ctx, t, x, memory.Query{Text: q.Text, K: memory.MaxK, Filter: q.Filter, Mode: memory.ModeSemantic}, false)
	if err != nil {
		t.Errorf("hybrid Search(%+v): the semantic search failed: %v", q, err)
		return
	}
	lists[1] = sem
	want := fuseModel(lists[0], lists[1], k)
	ok := len(got) == len(want)
	for i := 0; ok && i < len(got); i++ {
		g, w := got[i], want[i]
		ok = g.Item.ID == w.Item.ID && math.Abs(g.Score-w.Score) < 1e-12 &&
			slices.Equal(g.Found, w.Found) && slices.Equal(g.Highlights, w.Highlights)
	}
	if !ok {
		t.Errorf("hybrid Search(%+v):\ngot  %v\nwant %v (the fusion of keyword %v and semantic %v)",
			q, describeFused(got), describeFused(want), describe(lists[0]), describe(lists[1]))
	}
}

// fuseModel is reciprocal rank fusion with k = 60, written apart from the
// backends'.
func fuseModel(kw, sem []memory.Result, k int) []memory.Result {
	var out []memory.Result
	at := map[string]int{}
	for li, list := range [][]memory.Result{kw, sem} {
		mode := []memory.Mode{memory.ModeKeyword, memory.ModeSemantic}[li]
		for i, r := range list {
			rank := i + 1
			for rank > 1 && list[rank-2].Score == r.Score {
				rank--
			}
			j, ok := at[r.Item.ID]
			if !ok {
				j = len(out)
				at[r.Item.ID] = j
				out = append(out, memory.Result{Item: r.Item, Mode: memory.ModeHybrid})
			}
			if mode == memory.ModeKeyword {
				out[j].Highlights = r.Highlights
			}
			out[j].Score += 1.0 / (60 + float64(rank))
			out[j].Found = append(out[j].Found, mode)
		}
	}
	slices.SortStableFunc(out, keyword.Compare)
	return out[:min(k, len(out))]
}

func describeFused(res []memory.Result) string {
	s := make([]string, len(res))
	for i, r := range res {
		s[i] = fmt.Sprintf("%s:%.6f%v", r.Item.ID, r.Score, r.Found)
	}
	return "[" + strings.Join(s, " ") + "]"
}

// SameItem reports whether a and b are the same item (nil and empty
// metadata alike, times equal to the nanosecond).
func SameItem(a, b memory.Item) bool {
	return a.ID == b.ID && a.Text == b.Text && a.Time.Equal(b.Time) &&
		maps.Equal(a.Metadata, b.Metadata)
}

// IDs returns the IDs of res, in order.
func IDs(res []memory.Result) []string {
	out := make([]string, len(res))
	for i, r := range res {
		out[i] = r.Item.ID
	}
	return out
}

type irig struct {
	t   *testing.T
	x   memory.Index
	ctx context.Context
}

func newIRig(t *testing.T, newIndex NewIndex, cfg memory.IndexConfig) *irig {
	return &irig{t: t, x: newIndex(t, cfg), ctx: Ctx(t, "t1", "u1")}
}

func (r *irig) put(it memory.Item) {
	r.t.Helper()
	if err := r.x.Put(r.ctx, it); err != nil {
		r.t.Fatalf("Put(%q): %v", it.ID, err)
	}
}

func (r *irig) search(q memory.Query) []memory.Result {
	r.t.Helper()
	res, err := CheckSearch(r.ctx, r.t, r.x, q)
	if err != nil {
		r.t.Fatalf("Search(%+v): %v", q, err)
	}
	return res
}

// kwIDs is ids in ModeKeyword.
func (r *irig) kwIDs(text string, k int) []string {
	r.t.Helper()
	return IDs(r.search(memory.Query{Text: text, K: k, Mode: memory.ModeKeyword}))
}

func (r *irig) ids(text string, k int) []string {
	r.t.Helper()
	return IDs(r.search(memory.Query{Text: text, K: k}))
}

func (r *irig) keyword() bool { return slices.Contains(r.x.Modes(), memory.ModeKeyword) }

func testIndexBasics(t *testing.T, newIndex NewIndex) {
	r := newIRig(t, newIndex, memory.IndexConfig{})
	at := time.Date(2026, 3, 4, 5, 6, 7, 8, time.FixedZone("x", 3600))
	in := memory.Item{ID: "note-1", Text: "The deploy key rotates monthly.", Metadata: map[string]string{"kind": "fact"}, Time: at}
	r.put(in)
	got, err := r.x.Get(r.ctx, "note-1")
	if err != nil {
		t.Fatal(err)
	}
	if !SameItem(got, in) || got.Time.Location() != time.UTC {
		t.Errorf("Get = %+v, want %+v in UTC", got, in)
	}
	got.Metadata["kind"] = "changed" // the caller's copy
	in.Metadata["kind"] = "changed too"
	if again, _ := r.x.Get(r.ctx, "note-1"); again.Metadata["kind"] != "fact" {
		t.Errorf("metadata changed through a copy: %v", again.Metadata)
	}
	r.put(memory.Item{ID: "note-2", Text: "no time, no metadata"})
	if got, err := r.x.Get(r.ctx, "note-2"); err != nil || got.Metadata != nil || !got.Time.IsZero() {
		t.Errorf("Get(note-2) = %+v, %v; want nil metadata and a zero time", got, err)
	}
	if _, err := r.x.Get(r.ctx, "nope"); !errors.Is(err, memory.ErrItemNotFound) {
		t.Errorf("Get(missing) = %v, want ErrItemNotFound", err)
	}
	if err := r.x.Delete(r.ctx, "nope"); !errors.Is(err, memory.ErrItemNotFound) {
		t.Errorf("Delete(missing) = %v, want ErrItemNotFound", err)
	}
	if err := r.x.Delete(r.ctx, "note-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.x.Get(r.ctx, "note-1"); !errors.Is(err, memory.ErrItemNotFound) {
		t.Errorf("Get after Delete = %v, want ErrItemNotFound", err)
	}
	if len(r.x.Modes()) == 0 {
		t.Error("Modes is empty")
	}
}

func testIndexInvalid(t *testing.T, newIndex NewIndex) {
	r := newIRig(t, newIndex, memory.IndexConfig{})
	long := strings.Repeat("k", memory.MaxMetadataKeyBytes+1)
	many := map[string]string{}
	for i := range memory.MaxMetadataPairs + 1 {
		many[fmt.Sprint("k", i)] = "v"
	}
	for _, it := range []memory.Item{
		{ID: "", Text: "x"},
		{ID: strings.Repeat("i", memory.MaxIDBytes+1), Text: "x"},
		{ID: "a\nb", Text: "x"},
		{ID: "bad-utf8\xff", Text: "x"},
		{ID: "a", Text: ""},
		{ID: "a", Text: "\xff\xfe"},
		{ID: "a", Text: "x", Metadata: map[string]string{"": "v"}},
		{ID: "a", Text: "x", Metadata: map[string]string{long: "v"}},
		{ID: "a", Text: "x", Metadata: map[string]string{"k": strings.Repeat("v", memory.MaxMetadataValueBytes+1)}},
		{ID: "a", Text: "x", Metadata: many},
		{ID: "a", Text: "x", Time: time.Date(1500, 1, 1, 0, 0, 0, 0, time.UTC)},
	} {
		if err := r.x.Put(r.ctx, it); !errors.Is(err, memory.ErrInvalid) {
			t.Errorf("Put(%.40q) = %v, want ErrInvalid", fmt.Sprintf("%+v", it), err)
		}
	}
	for _, q := range []memory.Query{
		{Text: "x", K: -1},
		{Text: "x", K: memory.MaxK + 1},
		{Text: strings.Repeat("x ", memory.MaxQueryBytes)},
		{Text: "\xff"},
		{Text: "x", Mode: "fuzzy"},
		{Text: "x", Filter: map[string]string{"": "v"}},
	} {
		if _, err := CheckSearch(r.ctx, t, r.x, q); !errors.Is(err, memory.ErrInvalid) {
			t.Errorf("Search(%.60q) = %v, want ErrInvalid", fmt.Sprintf("%+v", q), err)
		}
	}
	for _, id := range []string{"", "a\x00"} {
		if _, err := r.x.Get(r.ctx, id); !errors.Is(err, memory.ErrInvalid) {
			t.Errorf("Get(%q) = %v, want ErrInvalid", id, err)
		}
		if err := r.x.Delete(r.ctx, id); !errors.Is(err, memory.ErrInvalid) {
			t.Errorf("Delete(%q) = %v, want ErrInvalid", id, err)
		}
	}
	if r.keyword() {
		var tooMany strings.Builder
		for i := range memory.MaxQueryParts + 1 {
			fmt.Fprintf(&tooMany, "w%d ", i)
		}
		for _, text := range []string{"", "  ", `"" !!`, tooMany.String()} {
			if _, err := CheckSearch(r.ctx, t, r.x, memory.Query{Text: text, Mode: memory.ModeKeyword}); !errors.Is(err, memory.ErrInvalid) {
				t.Errorf("keyword Search(%.40q) = %v, want ErrInvalid", text, err)
			}
		}
	}
}

func testIndexLimits(t *testing.T, newIndex NewIndex) {
	r := newIRig(t, newIndex, memory.IndexConfig{MaxItems: 2, MaxTextBytes: 10})
	r.put(memory.Item{ID: "a", Text: "one"})
	r.put(memory.Item{ID: "b", Text: "two"})
	var le *memory.LimitError
	if err := r.x.Put(r.ctx, memory.Item{ID: "c", Text: "three"}); !errors.Is(err, memory.ErrLimit) || !errors.As(err, &le) || le.Limit != "items" {
		t.Errorf("third item: %v, want an items LimitError", err)
	}
	r.put(memory.Item{ID: "b", Text: "two again"}) // a replacement adds nothing
	if err := r.x.Put(r.ctx, memory.Item{ID: "a", Text: "eleven byte"}); !errors.Is(err, memory.ErrLimit) {
		t.Errorf("text over MaxTextBytes: %v, want ErrLimit", err)
	}
	if got, _ := r.x.Get(r.ctx, "a"); got.Text != "one" {
		t.Errorf("a failed Put changed the item: %+v", got)
	}
	// Another scope has its own count.
	other := Ctx(t, "t2", "u1")
	if err := r.x.Put(other, memory.Item{ID: "c", Text: "three"}); err != nil {
		t.Errorf("another tenant's first item: %v", err)
	}
	if err := r.x.Delete(r.ctx, "a"); err != nil {
		t.Fatal(err)
	}
	r.put(memory.Item{ID: "c", Text: "three"})
}

// testIndexIsolation: tenants, and users under ScopeUser, never see each
// other's items: not by Get, Delete, Search or the same ID, and another
// scope's items do not change a search's scores.
func testIndexIsolation(t *testing.T, newIndex NewIndex) {
	for _, scope := range []memory.Scope{memory.ScopeTenant, memory.ScopeUser} {
		t.Run(string(scope), func(t *testing.T) {
			t.Parallel()
			x := newIndex(t, memory.IndexConfig{Scope: scope})
			a := Ctx(t, "t1", "u1")
			var b context.Context
			if scope == memory.ScopeUser {
				b = Ctx(t, "t1", "u2")
			} else {
				b = Ctx(t, "t2", "u1")
			}
			q := memory.Query{Text: "deploy"}
			if !slices.Contains(x.Modes(), memory.ModeKeyword) {
				q.Text = "how do we deploy"
			}
			put := func(ctx context.Context, id, text string) {
				t.Helper()
				if err := x.Put(ctx, memory.Item{ID: id, Text: text}); err != nil {
					t.Fatal(err)
				}
			}
			put(a, "x1", "deploy with the blue pipeline")
			put(a, "x2", "lunch order for friday")
			before, err := CheckSearch(a, t, x, q)
			if err != nil {
				t.Fatal(err)
			}
			put(b, "x1", "deploy deploy deploy, the other one's secret")
			put(b, "y1", "deploy notes of the other scope")
			put(b, "y2", "and more deploy")
			after, err := CheckSearch(a, t, x, q)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, after) {
				t.Errorf("another scope's items changed a search:\nbefore %+v\nafter  %+v", before, after)
			}
			if got, err := x.Get(a, "x1"); err != nil || got.Text != "deploy with the blue pipeline" {
				t.Errorf("Get(x1) in a = %+v, %v", got, err)
			}
			if _, err := x.Get(a, "y1"); !errors.Is(err, memory.ErrItemNotFound) {
				t.Errorf("Get of the other scope's item = %v, want ErrItemNotFound", err)
			}
			if err := x.Delete(a, "y1"); !errors.Is(err, memory.ErrItemNotFound) {
				t.Errorf("Delete of the other scope's item = %v, want ErrItemNotFound", err)
			}
			if err := x.Delete(b, "x1"); err != nil {
				t.Fatal(err)
			}
			if got, err := x.Get(a, "x1"); err != nil || got.Text != "deploy with the blue pipeline" {
				t.Errorf("deleting b's x1 changed a's: %+v, %v", got, err)
			}
			resB, err := CheckSearch(b, t, x, q)
			if err != nil {
				t.Fatal(err)
			}
			for _, r := range resB {
				if strings.HasPrefix(r.Item.ID, "x") {
					t.Errorf("b's search found a's item %+v", r.Item)
				}
			}
			if len(resB) == 0 {
				t.Errorf("b's search found none of its items")
			}

			// No tenant, and no user under ScopeUser.
			none := context.Background()
			if err := x.Put(none, memory.Item{ID: "z", Text: "deploy"}); !errors.Is(err, memory.ErrNoTenant) {
				t.Errorf("Put with no tenant = %v", err)
			}
			if _, err := x.Search(none, q); !errors.Is(err, memory.ErrNoTenant) {
				t.Errorf("Search with no tenant = %v", err)
			}
			if _, err := x.Get(none, "x1"); !errors.Is(err, memory.ErrNoTenant) {
				t.Errorf("Get with no tenant = %v", err)
			}
			if err := x.Delete(none, "x1"); !errors.Is(err, memory.ErrNoTenant) {
				t.Errorf("Delete with no tenant = %v", err)
			}
			noUser := Ctx(t, "t1", "")
			_, err = x.Search(noUser, q)
			if scope == memory.ScopeUser && !errors.Is(err, memory.ErrNoUser) {
				t.Errorf("Search with no user = %v, want ErrNoUser", err)
			}
			if scope == memory.ScopeTenant && err != nil {
				t.Errorf("Search with no user, tenant scope = %v", err)
			}
		})
	}
}

func testIndexUpsert(t *testing.T, newIndex NewIndex) {
	r := newIRig(t, newIndex, memory.IndexConfig{})
	r.put(memory.Item{ID: "n", Text: "old words about kafka", Metadata: map[string]string{"kind": "old", "keep": "x"}})
	r.put(memory.Item{ID: "other", Text: "unrelated text"})
	r.put(memory.Item{ID: "n", Text: "new words about redis", Metadata: map[string]string{"kind": "new"}})
	got, err := r.x.Get(r.ctx, "n")
	if err != nil {
		t.Fatal(err)
	}
	if want := (memory.Item{ID: "n", Text: "new words about redis", Metadata: map[string]string{"kind": "new"}}); !SameItem(got, want) {
		t.Errorf("Get after upsert = %+v, want %+v (metadata replaced, not merged)", got, want)
	}
	if !r.keyword() {
		return
	}
	if ids := r.kwIDs("kafka", 5); len(ids) != 0 {
		t.Errorf("old text still found: %v", ids)
	}
	if ids := r.kwIDs("redis", 5); !slices.Equal(ids, []string{"n"}) {
		t.Errorf("new text: %v, want [n]", ids)
	}
	res := r.search(memory.Query{Text: "words", Filter: map[string]string{"kind": "old"}, Mode: memory.ModeKeyword})
	if len(res) != 0 {
		t.Errorf("old metadata still filters: %v", IDs(res))
	}
}

func testIndexDelete(t *testing.T, newIndex NewIndex) {
	r := newIRig(t, newIndex, memory.IndexConfig{})
	r.put(memory.Item{ID: "a", Text: "rotate the signing key"})
	r.put(memory.Item{ID: "b", Text: "the signing ceremony"})
	q := "signing"
	if !r.keyword() {
		q = "how to rotate keys"
	}
	if ids := r.ids(q, 5); !slices.Contains(ids, "a") {
		t.Fatalf("before delete: %v", ids)
	}
	if err := r.x.Delete(r.ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if ids := r.ids(q, 5); slices.Contains(ids, "a") {
		t.Errorf("deleted item still found: %v", ids)
	}
	r.put(memory.Item{ID: "a", Text: "nothing in common"})
	if r.keyword() {
		if ids := r.kwIDs("rotate", 5); len(ids) != 0 {
			t.Errorf("a re-added item is found by its old text: %v", ids)
		}
	}
}

func testIndexK(t *testing.T, newIndex NewIndex) {
	r := newIRig(t, newIndex, memory.IndexConfig{})
	for i := range 12 {
		r.put(memory.Item{ID: fmt.Sprintf("i%02d", i), Text: fmt.Sprintf("incident report %d %s", i, strings.Repeat("filler ", i))})
	}
	for _, k := range []int{1, 3, 7} {
		if n := len(r.search(memory.Query{Text: "incident", K: k})); n != k {
			t.Errorf("K=%d gave %d results", k, n)
		}
	}
	if n := len(r.search(memory.Query{Text: "incident"})); n != memory.DefaultK {
		t.Errorf("K=0 gave %d results, want DefaultK %d", n, memory.DefaultK)
	}
	if n := len(r.search(memory.Query{Text: "incident", K: memory.MaxK})); n != 12 {
		t.Errorf("K=MaxK gave %d results, want all 12", n)
	}
	// The top k are the top of a longer list.
	top3, top7 := r.ids("incident", 3), r.ids("incident", 7)
	if !slices.Equal(top3, top7[:3]) {
		t.Errorf("top 3 %v is not the start of top 7 %v", top3, top7)
	}
}

// testIndexTies: items that score alike come newest first, then by ID,
// whatever order they were put in.
func testIndexTies(t *testing.T, newIndex NewIndex) {
	day := func(d int) time.Time { return time.Date(2026, 1, d, 0, 0, 0, 0, time.UTC) }
	items := []memory.Item{
		{ID: "c", Text: "same words here", Time: day(1)},
		{ID: "a", Text: "same words here", Time: day(1)},
		{ID: "d", Text: "same words here", Time: day(3)},
		{ID: "b", Text: "same words here"},
		{ID: "e", Text: "same words here", Time: day(2)},
	}
	want := []string{"d", "e", "a", "c", "b"}
	q := "same words"
	x := newIndex(t, memory.IndexConfig{})
	if !slices.Contains(x.Modes(), memory.ModeKeyword) {
		q = "the same words"
	}
	for i, order := range [][]int{{0, 1, 2, 3, 4}, {4, 3, 2, 1, 0}, {2, 0, 4, 1, 3}} {
		ctx := Ctx(t, fmt.Sprint("ties", i), "")
		for _, j := range order {
			if err := x.Put(ctx, items[j]); err != nil {
				t.Fatal(err)
			}
		}
		for range 3 {
			res, err := CheckSearch(ctx, t, x, memory.Query{Text: q, K: 10})
			if err != nil {
				t.Fatal(err)
			}
			if got := IDs(res); !slices.Equal(got, want) {
				t.Errorf("put in order %v: %v, want %v", order, got, want)
			}
		}
	}
}

func testIndexFilters(t *testing.T, newIndex NewIndex) {
	r := newIRig(t, newIndex, memory.IndexConfig{})
	r.put(memory.Item{ID: "1", Text: "billing question", Metadata: map[string]string{"kind": "note", "lang": "en"}})
	r.put(memory.Item{ID: "2", Text: "billing question", Metadata: map[string]string{"kind": "Note", "lang": "en"}})
	r.put(memory.Item{ID: "3", Text: "billing question", Metadata: map[string]string{"kind": "notes"}})
	r.put(memory.Item{ID: "4", Text: "billing question", Metadata: map[string]string{"kind": "note "}})
	r.put(memory.Item{ID: "5", Text: "billing question"})
	r.put(memory.Item{ID: "6", Text: "billing question", Metadata: map[string]string{"kind": "note", "lang": "de"}})
	r.put(memory.Item{ID: "7", Text: "billing question", Metadata: map[string]string{"kind": ""}})
	q := "billing"
	if !r.keyword() {
		q = "a question about billing"
	}
	for _, c := range []struct {
		filter map[string]string
		want   []string
	}{
		{map[string]string{"kind": "note"}, []string{"1", "6"}},
		{map[string]string{"kind": "note", "lang": "en"}, []string{"1"}},
		{map[string]string{"kind": ""}, []string{"7"}},
		{map[string]string{"lang": "fr"}, nil},
		{map[string]string{"Kind": "note"}, nil},
		{nil, []string{"1", "2", "3", "4", "5", "6", "7"}},
	} {
		res := r.search(memory.Query{Text: q, K: 10, Filter: c.filter})
		got := slices.Sorted(slices.Values(IDs(res)))
		if !slices.Equal(got, c.want) {
			t.Errorf("filter %v: %v, want %v", c.filter, got, c.want)
		}
	}
}

// testIndexModes: a mode the index lacks is ErrModeUnsupported with no
// results, never another mode's results.
func testIndexModes(t *testing.T, newIndex NewIndex) {
	r := newIRig(t, newIndex, memory.IndexConfig{})
	r.put(memory.Item{ID: "a", Text: "anything at all"})
	modes := r.x.Modes()
	for _, m := range []memory.Mode{memory.ModeKeyword, memory.ModeSemantic, memory.ModeHybrid} {
		res, err := r.x.Search(r.ctx, memory.Query{Text: "anything", Mode: m})
		if slices.Contains(modes, m) {
			if err != nil {
				t.Errorf("mode %q, which the index has: %v", m, err)
			}
			for _, x := range res {
				if x.Mode != m {
					t.Errorf("mode %q gave a result of mode %q", m, x.Mode)
				}
			}
			continue
		}
		if !errors.Is(err, memory.ErrModeUnsupported) || res != nil {
			t.Errorf("mode %q, which the index lacks: %v, %v; want ErrModeUnsupported and no results", m, res, err)
		}
	}
	res := r.search(memory.Query{Text: "anything"})
	for _, x := range res {
		if x.Mode != modes[0] {
			t.Errorf("default mode gave %q, want %q", x.Mode, modes[0])
		}
	}
}

func testIndexCanceled(t *testing.T, newIndex NewIndex) {
	x := newIndex(t, memory.IndexConfig{})
	ctx, cancel := context.WithCancel(Ctx(t, "t1", "u1"))
	cancel()
	if err := x.Put(ctx, memory.Item{ID: "a", Text: "x"}); !errors.Is(err, context.Canceled) {
		t.Errorf("Put = %v, want context.Canceled", err)
	}
	if _, err := x.Search(ctx, memory.Query{Text: "x"}); !errors.Is(err, context.Canceled) {
		t.Errorf("Search = %v, want context.Canceled", err)
	}
	if _, err := x.Get(ctx, "a"); !errors.Is(err, context.Canceled) {
		t.Errorf("Get = %v, want context.Canceled", err)
	}
	if err := x.Delete(ctx, "a"); !errors.Is(err, context.Canceled) {
		t.Errorf("Delete = %v, want context.Canceled", err)
	}
}

// testIndexConcurrent: writers in two tenants and readers at once; every
// item put is there at the end, and every search checks out.
func testIndexConcurrent(t *testing.T, newIndex NewIndex) {
	x := newIndex(t, memory.IndexConfig{})
	const workers, items = 4, 10
	var wg sync.WaitGroup
	for _, tenant := range []string{"c1", "c2"} {
		ctx := Ctx(t, tenant, "")
		for w := range workers {
			wg.Go(func() {
				for i := range items {
					id := fmt.Sprintf("w%d-%d", w, i)
					if err := x.Put(ctx, memory.Item{ID: id, Text: "shared words and " + id}); err != nil {
						t.Error(err)
						return
					}
					if _, err := checkSearch(ctx, t, x, memory.Query{Text: "shared words", K: 3}, false); err != nil {
						t.Error(err)
						return
					}
				}
			})
		}
	}
	wg.Wait()
	for _, tenant := range []string{"c1", "c2"} {
		ctx := Ctx(t, tenant, "")
		for w := range workers {
			for i := range items {
				if _, err := x.Get(ctx, fmt.Sprintf("w%d-%d", w, i)); err != nil {
					t.Errorf("%s: %v", tenant, err)
				}
			}
		}
	}
}

// testKeywordSyntax: case, punctuation, quoted phrases (required, in order),
// and terms that only boost next to a phrase.
func testKeywordSyntax(t *testing.T, newIndex NewIndex) {
	r := newIRig(t, newIndex, memory.IndexConfig{})
	r.put(memory.Item{ID: "phrase", Text: "We use blue-green deployment for the API."})
	r.put(memory.Item{ID: "reversed", Text: "Green and blue: the deployment colours."})
	r.put(memory.Item{ID: "apos", Text: "Don't deploy on Fridays!"})
	r.put(memory.Item{ID: "both", Text: "Blue green deployment, with a canary."})
	r.put(memory.Item{ID: "none", Text: "Nothing to see."})
	for _, c := range []struct {
		q    string
		want []string // exactly these, in this order
	}{
		{`"green deployment"`, []string{"both", "phrase"}},
		{`"GREEN Deployment" canary`, []string{"both", "phrase"}},
		{`"deployment green"`, nil},
		{`"don't deploy"`, []string{"apos"}},
		{`dont`, nil},
		{`fridays`, []string{"apos"}},
		{`"canary"`, []string{"both"}},
		{`zebra`, nil},
		{`"blue green" "with a canary"`, []string{"both"}},
		{`green "deployment for`, []string{"phrase"}}, // a quote left open runs to the end
	} {
		got := r.kwIDs(c.q, 10)
		if !slices.Equal(got, c.want) {
			t.Errorf("%s: %v, want %v", c.q, got, c.want)
		}
	}
	res := r.search(memory.Query{Text: `"green deployment" canary zebra`, K: 10, Mode: memory.ModeKeyword})
	if len(res) == 0 || !slices.Equal(res[0].Highlights, []string{"green deployment", "canary"}) {
		t.Errorf("highlights = %+v, want [green deployment canary] on the first", res)
	}
}

// testIndexSimulation runs random puts, deletes and searches in three scopes
// and compares every search with a model of the scope: BM25 for keyword,
// cosine over the fake embedder's vectors for semantic, and (in CheckSearch)
// the fusion of both for hybrid.
func testIndexSimulation(t *testing.T, newIndex NewIndex) {
	semantic := slices.Contains(newIndex(t, memory.IndexConfig{}).Modes(), memory.ModeSemantic)
	for seed := range uint64(4) {
		t.Run(fmt.Sprint("seed", seed+1), func(t *testing.T) {
			t.Parallel()
			cfg := memory.IndexConfig{Scope: memory.ScopeUser, MaxItems: 5}
			var e memory.Embedder
			if semantic {
				e = NewFakeEmbedder(DefaultFakeModel, DefaultFakeDim)
				cfg.Embedder = e
			}
			SimulateIndex(t, newIndex(t, cfg), e, 5, seed+1, 120)
		})
	}
}

var (
	simIDs    = []string{"a", "b", "c", "d", "e", "f"}
	simTexts  = []string{"red fox", "red red fox jumps", "the lazy dog", "fox and dog", "a red dog", "quick brown fox", "dog", "puppy retriever"}
	simQuery  = []string{"fox", "red", `"red fox"`, "dog fox", `"lazy dog" red`, "cat", `"fox red"`, "the", "pet"}
	simLabels = []map[string]string{nil, {"k": "a"}, {"k": "b"}, {"k": "a", "j": "1"}}
)

type simOwner struct{ tenant, user string }

// SimulateKeyword is SimulateIndex on a keyword index (no embedder).
func SimulateKeyword(t testing.TB, x memory.Index, maxItems int, seed uint64, n int) {
	t.Helper()
	SimulateIndex(t, x, nil, maxItems, seed, n)
}

// SimulateIndex runs n random operations chosen by seed on x, an empty ScopeUser
// index holding at most maxItems per scope, in three scopes, and checks
// after each one that Get matches a model of every scope, and that each
// search, in a mode of x chosen at random, returns what the model ranks:
// BM25 in ModeKeyword; in ModeSemantic, the top K by the cosine of e's
// vectors (e is x's embedder; nil for none), scores within 1e-5; hybrid
// searches are checked by CheckSearch.
func SimulateIndex(t testing.TB, x memory.Index, e memory.Embedder, maxItems int, seed uint64, n int) {
	t.Helper()
	rng := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	owners := []simOwner{{"s1", "u1"}, {"s1", "u2"}, {"s2", "u1"}}
	model := map[simOwner]map[string]memory.Item{}
	for _, o := range owners {
		model[o] = map[string]memory.Item{}
	}
	modes := x.Modes()
	for step := range n {
		o := owners[rng.IntN(len(owners))]
		ctx := Ctx(t, o.tenant, o.user)
		id := simIDs[rng.IntN(len(simIDs))]
		m := model[o]
		var what string
		switch op := rng.IntN(10); {
		case op < 4:
			it := memory.Item{
				ID: id, Text: simTexts[rng.IntN(len(simTexts))], Metadata: simLabels[rng.IntN(len(simLabels))],
				Time: time.Date(2026, 1, 1+rng.IntN(3), 0, 0, 0, 0, time.UTC),
			}
			what = fmt.Sprintf("put %s %q", id, it.Text)
			err := x.Put(ctx, it)
			_, exists := m[id]
			switch {
			case !exists && len(m) >= maxItems:
				if !errors.Is(err, memory.ErrLimit) {
					t.Errorf("seed %d step %d: %s past the limit: %v", seed, step, what, err)
				}
			case err != nil:
				t.Errorf("seed %d step %d: %s: %v", seed, step, what, err)
			default:
				m[id] = it.Clone()
			}
		case op < 6:
			what = "delete " + id
			err := x.Delete(ctx, id)
			if _, ok := m[id]; ok != (err == nil) || (!ok && !errors.Is(err, memory.ErrItemNotFound)) {
				t.Errorf("seed %d step %d: %s: %v (in the model: %v)", seed, step, what, err, ok)
			}
			delete(m, id)
		default:
			q := memory.Query{
				Text: simQuery[rng.IntN(len(simQuery))], K: 1 + rng.IntN(4), Filter: simLabels[rng.IntN(len(simLabels))],
				Mode: modes[rng.IntN(len(modes))],
			}
			what = fmt.Sprintf("search %+v", q)
			got, err := CheckSearch(ctx, t, x, q)
			if errors.Is(err, memory.ErrInvalid) && q.Mode == memory.ModeKeyword && memory.HybridKeywordText(q.Text) == "" {
				break // a keyword query with no word
			}
			if err != nil {
				t.Errorf("seed %d step %d: %s: %v", seed, step, what, err)
				break
			}
			switch q.Mode {
			case memory.ModeKeyword:
				if want := modelSearch(m, q); !sameResults(got, want) {
					t.Errorf("seed %d step %d: %s:\ngot  %v\nwant %v", seed, step, what, describe(got), describe(want))
				}
			case memory.ModeSemantic:
				if msg := checkSemanticModel(t, e, m, q, got); msg != "" {
					t.Errorf("seed %d step %d: %s: %s", seed, step, what, msg)
				}
			}
		}
		for _, o := range owners {
			for _, id := range simIDs {
				got, err := x.Get(Ctx(t, o.tenant, o.user), id)
				want, ok := model[o][id]
				if ok != (err == nil) || (ok && !SameItem(got, want)) {
					t.Errorf("seed %d step %d (after %s): %v Get(%s) = %+v, %v; the model has %+v, %v", seed, step, what, o, id, got, err, want, ok)
				}
			}
		}
		if t.Failed() {
			t.FailNow()
		}
	}
}

// checkSemanticModel returns what is wrong with got, a semantic search of
// q over items, or "": each result's score is the cosine of e's vectors of
// its text and q's (within 1e-5), and the results are the top K of the
// items in the filter (ties within 1e-5 either way).
func checkSemanticModel(t testing.TB, e memory.Embedder, items map[string]memory.Item, q memory.Query, got []memory.Result) string {
	t.Helper()
	cos := func(a, b string) float64 {
		vs, err := e.Embed(t.Context(), []string{a, b}, memory.PurposeDocument)
		if err != nil {
			t.Fatal(err)
		}
		var dot, na, nb float64
		for i := range vs[0] {
			x, y := float64(vs[0][i]), float64(vs[1][i])
			dot, na, nb = dot+x*y, na+x*x, nb+y*y
		}
		if na == 0 || nb == 0 {
			return 0
		}
		return dot / math.Sqrt(na*nb)
	}
	var scores []float64
	want := map[string]float64{}
	for id, it := range items {
		if memory.MatchesFilter(it.Metadata, q.Filter) {
			want[id] = cos(q.Text, it.Text)
			scores = append(scores, want[id])
		}
	}
	if len(got) != min(q.K, len(scores)) {
		return fmt.Sprintf("%d results, want %d", len(got), min(q.K, len(scores)))
	}
	slices.Sort(scores)
	slices.Reverse(scores)
	for _, r := range got {
		w, ok := want[r.Item.ID]
		switch {
		case !ok:
			return fmt.Sprintf("result %q is not an item of the filter", r.Item.ID)
		case math.Abs(r.Score-w) > 1e-5:
			return fmt.Sprintf("result %q scores %v, its cosine is %v", r.Item.ID, r.Score, w)
		case w < scores[len(got)-1]-1e-5:
			return fmt.Sprintf("result %q (cosine %v) is not in the top %d %v", r.Item.ID, w, len(got), scores)
		}
	}
	return ""
}

// modelSearch is BM25 over the items of one scope.
func modelSearch(items map[string]memory.Item, q memory.Query) []memory.Result {
	kq, err := memory.ParseKeywordQuery(q.Text)
	if err != nil {
		return nil
	}
	parts := keyword.Parts(kq)
	st := keyword.Stats{Docs: len(items), DF: make([]int, len(parts))}
	var docs []keyword.Doc
	for _, it := range items {
		d := keyword.Doc{Item: it, Tokens: memory.Tokenize(it.Text)}
		st.Tokens += len(d.Tokens)
		for i, p := range parts {
			if keyword.Count(d.Tokens, p) > 0 {
				st.DF[i]++
			}
		}
		docs = append(docs, d)
	}
	return keyword.Rank(kq, docs, st, q.Filter, q.K)
}

func sameResults(a, b []memory.Result) bool {
	return slices.EqualFunc(a, b, func(x, y memory.Result) bool {
		return SameItem(x.Item, y.Item) && x.Score == y.Score && x.Mode == y.Mode && slices.Equal(x.Highlights, y.Highlights)
	})
}

func describe(res []memory.Result) string {
	s := make([]string, len(res))
	for i, r := range res {
		s[i] = fmt.Sprintf("%s:%.4f", r.Item.ID, r.Score)
	}
	return "[" + strings.Join(s, " ") + "]"
}
