package keyword_test

import (
	"math"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/memory"
	"github.com/bsamanvith/agentkit/pkg/memory/internal/keyword"
)

// TestRankIsBM25: a score is BM25 with k1 1.2 and b 0.75, worked by hand.
func TestRankIsBM25(t *testing.T) {
	t.Parallel()
	q, err := memory.ParseKeywordQuery(`fox`)
	if err != nil {
		t.Fatal(err)
	}
	docs := []keyword.Doc{
		{Item: memory.Item{ID: "a"}, Tokens: []string{"fox", "fox", "den"}},
		{Item: memory.Item{ID: "b"}, Tokens: []string{"the", "dog"}},
	}
	// N 2, df 1, avgdl 2.5; doc a: f 2, dl 3.
	st := keyword.Stats{Docs: 2, Tokens: 5, DF: []int{1}}
	res := keyword.Rank(q, docs, st, nil, 5)
	idf := math.Log(1 + (2-1+0.5)/(1+0.5))
	tf := 2 * (1.2 + 1) / (2 + 1.2*(1-0.75+0.75*3/2.5))
	if len(res) != 1 || res[0].Item.ID != "a" || math.Abs(res[0].Score-idf*tf) > 1e-12 {
		t.Errorf("Rank = %+v, want a with %v", res, idf*tf)
	}
}

func TestCountOverlaps(t *testing.T) {
	t.Parallel()
	if n := keyword.Count([]string{"a", "a", "a"}, []string{"a", "a"}); n != 2 {
		t.Errorf("Count = %d, want 2", n)
	}
	if n := keyword.Count([]string{"a"}, nil); n != 0 {
		t.Errorf("Count of an empty part = %d", n)
	}
}
