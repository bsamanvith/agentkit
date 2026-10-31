package handbook_test

import (
	"context"
	"testing"

	"github.com/bsamanvith/agentkit/apps/farm/handbook"
	"github.com/bsamanvith/agentkit/pkg/knowledge"
)

func TestHandbook(t *testing.T) {
	t.Parallel()
	b, err := handbook.New(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Documents()) < 5 {
		t.Fatalf("%d documents", len(b.Documents()))
	}
	// Every document is a tree: a title, chapters, sections.
	for _, d := range b.Documents() {
		levels := map[int]int{}
		for _, s := range knowledge.Outline(d) {
			levels[s.Level]++
		}
		if levels[1] != 1 || levels[2] < 2 {
			t.Errorf("%s: headings by level %v, want one title and chapters", d.Source, levels)
		}
	}
	for q, want := range map[string]string{
		"when to sow after monsoon rain": "seasons-and-sowing.md",
		"black soil cracks sticky":       "soils-of-india.md",
		"sorghum pigeon pea row ratio":   "mixed-farming.md",
		"dragon fruit Mexico":            "crops.md",
		"drip irrigation saves water":    "water.md",
	} {
		hits, err := b.Search(context.Background(), q, 1)
		if err != nil || len(hits) == 0 || hits[0].Source != want {
			t.Errorf("%q: %+v %v, want %s", q, hits, err, want)
		}
	}
}
