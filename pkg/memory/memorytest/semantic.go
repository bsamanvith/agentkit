package memorytest

import (
	"context"
	"errors"
	"math"
	"slices"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/memory"
)

func (r *irig) semantic(text string, k int) []memory.Result {
	r.t.Helper()
	return r.search(memory.Query{Text: text, K: k, Mode: memory.ModeSemantic})
}

// testSemanticScores: scores are cosines: a text scores 1 against itself,
// and the same text three times over scores the same (vectors are
// normalized, so length does not count); ties then go newest first, then by
// ID.
func testSemanticScores(t *testing.T, newIndex NewIndex) {
	r := newIRig(t, newIndex, memory.IndexConfig{})
	r.put(memory.Item{ID: "once", Text: "aisle seat"})
	r.put(memory.Item{ID: "thrice", Text: "aisle seat aisle seat aisle seat"})
	r.put(memory.Item{ID: "other", Text: "Peanuts give him a severe reaction."})
	r.put(memory.Item{ID: "none", Text: "!!!"}) // no word: a zero vector, cosine 0
	res := r.semantic("aisle seat", 10)
	if got := IDs(res); !slices.Equal(got, []string{"once", "thrice", "none", "other"}) && !slices.Equal(got, []string{"once", "thrice", "other", "none"}) {
		t.Fatalf("semantic results %v, want once, thrice, then the others", got)
	}
	for _, x := range res[:2] {
		if math.Abs(x.Score-1) > 1e-5 {
			t.Errorf("%q scores %v against its own text, want 1: cosine of normalized vectors", x.Item.ID, x.Score)
		}
	}
	for _, x := range res[2:] {
		if math.Abs(x.Score) > 0.3 {
			t.Errorf("unrelated %q scores %v", x.Item.ID, x.Score)
		}
	}
	if res[0].Score != res[1].Score {
		t.Errorf("a text repeated scores %v, once %v: vectors must be normalized", res[1].Score, res[0].Score)
	}
}

// testEmbedderCalls: Put embeds the item's text as a document, a semantic
// search the query as a query; Get, Delete, keyword searches and refused
// puts call nothing.
func testEmbedderCalls(t *testing.T, newIndex NewIndex) {
	e := NewFakeEmbedder(DefaultFakeModel, DefaultFakeDim)
	r := newIRig(t, newIndex, memory.IndexConfig{Embedder: e, MaxItems: 1})
	r.put(memory.Item{ID: "a", Text: "nut allergy"})
	if err := r.x.Put(r.ctx, memory.Item{ID: "b", Text: "over the limit"}); !errors.Is(err, memory.ErrLimit) {
		t.Fatalf("second item = %v, want ErrLimit", err)
	}
	if err := r.x.Put(r.ctx, memory.Item{ID: "c", Text: ""}); !errors.Is(err, memory.ErrInvalid) {
		t.Fatalf("empty text = %v", err)
	}
	if _, err := r.x.Get(r.ctx, "a"); err != nil {
		t.Fatal(err)
	}
	r.semantic("peanuts", 3)
	if r.keyword() {
		r.kwIDs("nut", 3)
	}
	if err := r.x.Delete(r.ctx, "a"); err != nil {
		t.Fatal(err)
	}
	want := []EmbedCall{
		{Texts: []string{"nut allergy"}, Purpose: memory.PurposeDocument},
		{Texts: []string{"peanuts"}, Purpose: memory.PurposeQuery},
	}
	if got := e.Calls(); !slices.EqualFunc(got, want, func(a, b EmbedCall) bool {
		return a.Purpose == b.Purpose && slices.Equal(a.Texts, b.Texts)
	}) {
		t.Errorf("embedder calls %+v, want %+v", got, want)
	}
}

var errEmbedderDown = errors.New("memorytest: the embedder is down")

// testEmbedderFails: an embedder's error fails Put (nothing stored) and
// semantic searches (no results); keyword searches go on.
func testEmbedderFails(t *testing.T, newIndex NewIndex) {
	e := NewFakeEmbedder(DefaultFakeModel, DefaultFakeDim)
	r := newIRig(t, newIndex, memory.IndexConfig{Embedder: e})
	r.put(memory.Item{ID: "a", Text: "nut allergy"})
	e.SetErr(errEmbedderDown)
	if err := r.x.Put(r.ctx, memory.Item{ID: "b", Text: "pet dog"}); !errors.Is(err, errEmbedderDown) {
		t.Errorf("Put with the embedder down = %v, want its error", err)
	}
	if err := r.x.Put(r.ctx, memory.Item{ID: "a", Text: "replaced"}); !errors.Is(err, errEmbedderDown) {
		t.Errorf("replacing Put with the embedder down = %v, want its error", err)
	}
	if _, err := r.x.Get(r.ctx, "b"); !errors.Is(err, memory.ErrItemNotFound) {
		t.Errorf("Get of an item whose Put failed = %v, want ErrItemNotFound", err)
	}
	if got, _ := r.x.Get(r.ctx, "a"); got.Text != "nut allergy" {
		t.Errorf("a failed Put replaced the item: %+v", got)
	}
	for _, m := range r.x.Modes() {
		res, err := r.x.Search(r.ctx, memory.Query{Text: "allergy", Mode: m})
		if m == memory.ModeKeyword {
			if err != nil || len(res) != 1 {
				t.Errorf("keyword search with the embedder down = %v, %v", res, err)
			}
			continue
		}
		if !errors.Is(err, errEmbedderDown) || res != nil {
			t.Errorf("%s search with the embedder down = %v, %v; want its error and no results", m, res, err)
		}
	}
	e.SetErr(nil)
	if ids := IDs(r.semantic("allergy", 3)); !slices.Equal(ids, []string{"a"}) {
		t.Errorf("after the embedder is back: %v", ids)
	}
}

// lyingEmbedder wraps the fake and breaks what it returns.
type lyingEmbedder struct {
	*FakeEmbedder
	lie func(vs [][]float32) [][]float32
}

func (e lyingEmbedder) Embed(ctx context.Context, texts []string, p memory.Purpose) ([][]float32, error) {
	vs, err := e.FakeEmbedder.Embed(ctx, texts, p)
	if err != nil {
		return nil, err
	}
	return e.lie(vs), nil
}

// testEmbedderLies: a vector of another dimension than the embedder's Model
// is ErrEmbedderMismatch; a wrong count or a value that is not finite is an
// error too; nothing is stored.
func testEmbedderLies(t *testing.T, newIndex NewIndex) {
	for name, c := range map[string]struct {
		lie      func([][]float32) [][]float32
		mismatch bool
	}{
		"short": {func(vs [][]float32) [][]float32 { return [][]float32{vs[0][:DefaultFakeDim-1]} }, true},
		"long":  {func(vs [][]float32) [][]float32 { return [][]float32{append(vs[0], 1)} }, true},
		"none":  {func([][]float32) [][]float32 { return nil }, false},
		"two":   {func(vs [][]float32) [][]float32 { return append(vs, vs[0]) }, false},
		"nan":   {func(vs [][]float32) [][]float32 { vs[0][3] = float32(math.NaN()); return vs }, false},
		"inf":   {func(vs [][]float32) [][]float32 { vs[0][0] = float32(math.Inf(1)); return vs }, false},
	} {
		t.Run(name, func(t *testing.T) {
			e := lyingEmbedder{NewFakeEmbedder(DefaultFakeModel, DefaultFakeDim), c.lie}
			r := newIRig(t, newIndex, memory.IndexConfig{Embedder: e})
			err := r.x.Put(r.ctx, memory.Item{ID: "a", Text: "nut allergy"})
			if err == nil || c.mismatch != errors.Is(err, memory.ErrEmbedderMismatch) {
				t.Errorf("Put = %v, want an error (ErrEmbedderMismatch: %v)", err, c.mismatch)
			}
			if _, err := r.x.Get(r.ctx, "a"); !errors.Is(err, memory.ErrItemNotFound) {
				t.Errorf("Get after a refused vector = %v", err)
			}
			res, err := r.x.Search(r.ctx, memory.Query{Text: "nut", Mode: memory.ModeSemantic})
			if err == nil || res != nil || c.mismatch != errors.Is(err, memory.ErrEmbedderMismatch) {
				t.Errorf("Search = %v, %v; want an error (ErrEmbedderMismatch: %v)", res, err, c.mismatch)
			}
		})
	}
}

// testSemanticUpsert: a replaced item is found by its new text's meaning,
// not its old one's.
func testSemanticUpsert(t *testing.T, newIndex NewIndex) {
	r := newIRig(t, newIndex, memory.IndexConfig{})
	r.put(memory.Item{ID: "n", Text: "Peanuts give him a severe reaction."})
	r.put(memory.Item{ID: "other", Text: "Their puppy is a golden retriever."})
	r.put(memory.Item{ID: "n", Text: "It rained all week in Lisbon."})
	res := r.semantic("It rained all week in Lisbon.", 2)
	if len(res) == 0 || res[0].Item.ID != "n" || math.Abs(res[0].Score-1) > 1e-5 {
		t.Errorf("the new text: %v", describe(res))
	}
	res = r.semantic("Peanuts give him a severe reaction.", 2)
	for _, x := range res {
		if x.Item.ID == "n" && x.Score > 0.3 {
			t.Errorf("the old text still scores %v", x.Score)
		}
	}
}

// Reopen opens the same index (the same file or database) with cfg each
// time it is called in one test; the index must be a memory.Reembedder.
type Reopen func(t *testing.T, cfg memory.IndexConfig) memory.Index

// RunEmbedderChange checks a persistent index across a change of embedder:
// written with model A, then opened with B (another name) or C (A's name,
// another dimension), semantic and hybrid searches are ErrEmbedderMismatch
// while keyword searches, Get and Put go on; an item put without an
// embedder loses its vector; Reembed then embeds exactly the stale items of
// every scope, after which searches work, and another Reembed does nothing.
func RunEmbedderChange(t *testing.T, reopen Reopen) {
	t.Helper()
	a := NewFakeEmbedder("model-a", DefaultFakeDim)
	b := NewFakeEmbedder("model-b", DefaultFakeDim)
	c := NewFakeEmbedder("model-a", DefaultFakeDim/2)
	s1, s2 := Ctx(t, "t1", ""), Ctx(t, "t2", "")
	put := func(x memory.Index, ctx context.Context, id, text string) {
		t.Helper()
		if err := x.Put(ctx, memory.Item{ID: id, Text: text}); err != nil {
			t.Fatalf("Put(%s): %v", id, err)
		}
	}
	x := reopen(t, memory.IndexConfig{Embedder: a})
	put(x, s1, "a", "nut allergy")
	put(x, s1, "b", "pet dog breed")
	put(x, s2, "c", "portugal weather")
	mismatch := func(x memory.Index, ctx context.Context, why string) {
		t.Helper()
		for _, m := range []memory.Mode{memory.ModeSemantic, memory.ModeHybrid} {
			res, err := x.Search(ctx, memory.Query{Text: "peanuts", Mode: m})
			if !errors.Is(err, memory.ErrEmbedderMismatch) || res != nil {
				t.Errorf("%s: %s search = %v, %v; want ErrEmbedderMismatch", why, m, res, err)
			}
		}
		if res, err := x.Search(ctx, memory.Query{Text: "allergy", Mode: memory.ModeKeyword}); err != nil || len(res) != 1 {
			t.Errorf("%s: keyword search = %v, %v; want a's item", why, res, err)
		}
	}
	mismatch(reopen(t, memory.IndexConfig{Embedder: b}), s1, "opened with another model")
	mismatch(reopen(t, memory.IndexConfig{Embedder: c}), s1, "opened with another dimension")

	x = reopen(t, memory.IndexConfig{Embedder: b})
	put(x, s1, "d", "salary") // written with B: the others stay A's
	mismatch(x, s1, "one item re-written")
	if it, err := x.Get(s1, "a"); err != nil || it.Text != "nut allergy" {
		t.Errorf("Get with another model = %+v, %v", it, err)
	}
	put(reopen(t, memory.IndexConfig{}), s1, "a", "nut allergy") // no embedder: no vector

	// An item rewritten without an embedder keeps no vector of its old text.
	s3 := Ctx(t, "t3", "")
	put(reopen(t, memory.IndexConfig{Embedder: a}), s3, "e", "vegetarian diet")
	put(reopen(t, memory.IndexConfig{}), s3, "e", "car vehicle")
	if res, err := reopen(t, memory.IndexConfig{Embedder: a}).Search(s3, memory.Query{Text: "vegetarian", Mode: memory.ModeSemantic}); !errors.Is(err, memory.ErrEmbedderMismatch) {
		t.Errorf("an item put without an embedder, searched with the model of its old vector = %v, %v; want ErrEmbedderMismatch", res, err)
	}

	x = reopen(t, memory.IndexConfig{Embedder: b})
	re, ok := x.(memory.Reembedder)
	if !ok {
		t.Fatalf("%T is not a memory.Reembedder", x)
	}
	before := len(b.Calls())
	n, err := re.Reembed(t.Context())
	if err != nil || n != 4 {
		t.Fatalf("Reembed = %d, %v; want 4 (a and e without a vector, b and c of model A)", n, err)
	}
	var texts []string
	for _, call := range b.Calls()[before:] {
		if call.Purpose != memory.PurposeDocument {
			t.Errorf("Reembed embedded as %q", call.Purpose)
		}
		texts = append(texts, call.Texts...)
	}
	slices.Sort(texts)
	if want := []string{"car vehicle", "nut allergy", "pet dog breed", "portugal weather"}; !slices.Equal(texts, want) {
		t.Errorf("Reembed embedded %v, want %v", texts, want)
	}
	for _, q := range []struct {
		ctx  context.Context
		text string
		top  string
	}{{s1, "peanuts", "a"}, {s1, "puppy", "b"}, {s1, "pay", "d"}, {s2, "rain", "c"}, {s3, "automobile", "e"}} {
		res, err := CheckSearch(q.ctx, t, x, memory.Query{Text: q.text, Mode: memory.ModeSemantic})
		if err != nil || len(res) == 0 || res[0].Item.ID != q.top {
			t.Errorf("after Reembed, %q: %v, %v; want %s first", q.text, IDs(res), err, q.top)
		}
	}
	if n, err := re.Reembed(t.Context()); err != nil || n != 0 {
		t.Errorf("a second Reembed = %d, %v; want 0", n, err)
	}
	mismatch(reopen(t, memory.IndexConfig{Embedder: a}), s1, "back to model A")
	if _, err := reopen(t, memory.IndexConfig{}).(memory.Reembedder).Reembed(t.Context()); !errors.Is(err, memory.ErrInvalid) {
		t.Errorf("Reembed with no embedder = %v, want ErrInvalid", err)
	}
}
