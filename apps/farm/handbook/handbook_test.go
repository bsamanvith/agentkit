package handbook_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/bsamanvith/agentkit/apps/farm/agro"
	"github.com/bsamanvith/agentkit/pkg/memory/memorytest"

	"github.com/bsamanvith/agentkit/apps/farm/handbook"
	"github.com/bsamanvith/agentkit/pkg/knowledge"
)

func TestHandbook(t *testing.T) {
	t.Parallel()
	b, err := handbook.New(context.Background(), nil)
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
		"melia dubia pulpwood border":    "trees-and-borders.md",
		"farm pond lining silt trap":     "farm-layout.md",
		"berseem napier fodder":          "fodder-and-green-manure.md",
		"Godavari delta coastal region":  "agro-climatic-regions.md",
		"Also called Telugu":             handbook.LibraryDoc,
	} {
		hits, err := b.Search(context.Background(), q, 1)
		if err != nil || len(hits) == 0 || hits[0].Source != want {
			t.Errorf("%q: %+v %v, want %s", q, hits, err, want)
		}
	}
}

func TestCropLibraryChapter(t *testing.T) {
	t.Parallel()
	lib, err := agro.Default()
	if err != nil {
		t.Fatal(err)
	}
	text := handbook.Library(lib)
	for _, c := range lib.Crops() {
		if !strings.Contains(text, "### "+c.Name+"\n") || !strings.Contains(text, "Id in the app: "+c.ID+".") {
			t.Errorf("crop %s has no section", c.ID)
		}
	}
	if strings.Contains(text, "  ") {
		t.Error("double spaces in the library")
	}
	b, err := handbook.New(context.Background(), lib)
	if err != nil {
		t.Fatal(err)
	}
	var lib1 knowledge.Document
	for _, d := range b.Documents() {
		if d.Source == handbook.LibraryDoc {
			lib1 = d
		}
	}
	levels := map[int]int{}
	for _, s := range knowledge.Outline(lib1) {
		levels[s.Level]++
	}
	if levels[3] != len(lib.Crops()) {
		t.Errorf("library outline %v, want a section per crop (%d)", levels, len(lib.Crops()))
	}
}

func TestHybridSearch(t *testing.T) {
	t.Parallel()
	emb := memorytest.NewFakeEmbedder("fake", 32)
	b, err := handbook.New(context.Background(), nil, handbook.WithEmbedder(emb))
	if err != nil {
		t.Fatal(err)
	}
	if len(emb.Calls()) == 0 {
		t.Fatal("the passages were not embedded")
	}
	hits, err := b.Search(context.Background(), "where to dig a farm pond", 3)
	if err != nil || len(hits) == 0 {
		t.Fatalf("%v %v", hits, err)
	}
	if !slices.ContainsFunc(hits, func(h knowledge.Hit) bool { return h.Source == "farm-layout.md" }) {
		t.Errorf("hybrid search for a pond: %+v", hits)
	}
}
