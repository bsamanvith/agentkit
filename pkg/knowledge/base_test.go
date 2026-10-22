package knowledge_test

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/bsamanvith/agentkit/pkg/knowledge"
	"github.com/bsamanvith/agentkit/pkg/memory"
	"github.com/bsamanvith/agentkit/pkg/memory/memorytest"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

const handbook = "testdata/handbook"

func loadHandbook(t *testing.T) []knowledge.Document {
	t.Helper()
	docs, err := knowledge.Load(os.DirFS(handbook))
	if err != nil {
		t.Fatal(err)
	}
	return docs
}

func newBase(t *testing.T, c knowledge.Config) *knowledge.Base {
	t.Helper()
	b, err := knowledge.New(t.Context(), loadHandbook(t), c)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestLoad(t *testing.T) {
	t.Parallel()
	docs := loadHandbook(t)
	var got []string
	for _, d := range docs {
		got = append(got, d.Source+": "+d.Title)
	}
	// Hidden folders, other extensions and empty files are left out; a
	// document without a level-one heading is named after its file.
	want := []string{
		"returns.md: Returns and refunds",
		"travel.txt: travel",
		"troubleshooting/error-codes.md: E-bike display error codes",
		"warranty.md: Warranty",
	}
	if !slices.Equal(got, want) {
		t.Errorf("loaded %q, want %q", got, want)
	}
}

func TestLoadRefuses(t *testing.T) {
	t.Parallel()
	for name, fsys := range map[string]fstest.MapFS{
		"not UTF-8": {"a.md": {Data: []byte("caf\xe9")}},
		"too big":   {"big.txt": {Data: []byte(strings.Repeat("x", knowledge.MaxDocumentBytes+1))}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := knowledge.Load(fsys); !errors.Is(err, knowledge.ErrInvalid) {
				t.Errorf("Load: %v, want ErrInvalid", err)
			}
		})
	}
}

func TestLoadTitleSkipsCode(t *testing.T) {
	t.Parallel()
	docs, err := knowledge.Load(fstest.MapFS{
		"x/notes.MD": {Data: []byte("```\n# not the title\n```\n## Sub\n# Real title\n")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 1 || docs[0].Source != "x/notes.MD" || docs[0].Title != "Real title" {
		t.Errorf("loaded %+v, want x/notes.MD titled %q", docs, "Real title")
	}
}

func TestNewRefuses(t *testing.T) {
	t.Parallel()
	doc := knowledge.Document{Source: "a.md", Text: "text"}
	for name, tt := range map[string]struct {
		docs []knowledge.Document
		c    knowledge.Config
		want error
	}{
		"no documents":    {nil, knowledge.Config{}, knowledge.ErrEmpty},
		"only white":      {[]knowledge.Document{{Source: "a.md", Text: "\n \n"}}, knowledge.Config{}, knowledge.ErrEmpty},
		"two of a source": {[]knowledge.Document{doc, doc}, knowledge.Config{}, knowledge.ErrInvalid},
		"no source":       {[]knowledge.Document{{Text: "x"}}, knowledge.Config{}, knowledge.ErrInvalid},
		"not UTF-8":       {[]knowledge.Document{{Source: "a.md", Text: "\xff"}}, knowledge.Config{}, knowledge.ErrInvalid},
		"small chunks":    {[]knowledge.Document{doc}, knowledge.Config{ChunkBytes: knowledge.MinChunkBytes - 1}, knowledge.ErrInvalid},
		"large chunks":    {[]knowledge.Document{doc}, knowledge.Config{ChunkBytes: knowledge.MaxChunkBytes + 1}, knowledge.ErrInvalid},
		"semantic, no embedder": {
			[]knowledge.Document{doc}, knowledge.Config{Modes: []memory.Mode{memory.ModeSemantic}}, memory.ErrInvalid,
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := knowledge.New(t.Context(), tt.docs, tt.c); !errors.Is(err, tt.want) {
				t.Errorf("New: %v, want %v", err, tt.want)
			}
		})
	}
}

// relevance are questions a support agent gets, and the document and
// section whose passage must come first.
var relevance = []struct{ query, source, section string }{
	{"How long is the return window?", "returns.md", "Returns and refunds > Return window"},
	{"restocking fee opened e-bike", "returns.md", "Returns and refunds > Restocking fee"},
	{"What does error E07 mean?", "troubleshooting/error-codes.md", "E-bike display error codes > E07: torque sensor fault"},
	{"battery overheating", "troubleshooting/error-codes.md", "E-bike display error codes > E12: battery overheating"},
	{"where is the serial number for a warranty claim", "warranty.md", "Warranty > Making a claim"},
	{`"NW-RMA"`, "returns.md", "Returns and refunds > Starting a return"},
}

func TestSearchByKeyword(t *testing.T) {
	t.Parallel()
	b := newBase(t, knowledge.Config{})
	if got := b.Modes(); !slices.Equal(got, []memory.Mode{memory.ModeKeyword}) {
		t.Errorf("modes %v, want keyword only", got)
	}
	for _, r := range relevance {
		hits, err := b.Search(t.Context(), r.query, 3)
		if err != nil {
			t.Fatalf("%q: %v", r.query, err)
		}
		if len(hits) == 0 || hits[0].Source != r.source || hits[0].Section != r.section {
			t.Errorf("%q: first hit %+v, want %s, %q", r.query, first(hits), r.source, r.section)
		}
		for i := 1; i < len(hits); i++ {
			if hits[i].Score > hits[i-1].Score {
				t.Errorf("%q: hits out of order: %v after %v", r.query, hits[i].Score, hits[i-1].Score)
			}
		}
	}
}

func first(hits []knowledge.Hit) any {
	if len(hits) == 0 {
		return "none"
	}
	return hits[0].Passage
}

// TestSearchFindsTheSectionsWords: a passage is found by the words of the
// document's title and the headings it is under, though a long section's
// later passages do not have them in their text.
func TestSearchFindsTheSectionsWords(t *testing.T) {
	t.Parallel()
	body := strings.Repeat("They are black and white.\n\n", 20) // 540 bytes: two passages of 300
	b, err := knowledge.New(t.Context(), []knowledge.Document{
		{Source: "a.md", Title: "Zebras", Text: "## Stripes\n" + body},
		{Source: "b.md", Title: "Horses", Text: "Horses run.\n"},
	}, knowledge.Config{ChunkBytes: 300})
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"stripes", "zebras"} {
		hits, err := b.Search(t.Context(), q, 5)
		if err != nil {
			t.Fatal(err)
		}
		if len(hits) != 2 || hits[0].Source != "a.md" || hits[1].Source != "a.md" || hits[1].Part == hits[0].Part {
			t.Errorf("%q: hits %+v, want both passages of a.md", q, hits)
		}
	}
}

func TestSearchRefusesABadQuery(t *testing.T) {
	t.Parallel()
	b := newBase(t, knowledge.Config{})
	for _, q := range []string{"", "?!", strings.Repeat("a ", memory.MaxQueryBytes)} {
		if _, err := b.Search(t.Context(), q, 0); !errors.Is(err, memory.ErrInvalid) {
			t.Errorf("Search(%.20q): %v, want memory.ErrInvalid", q, err)
		}
	}
	if _, err := b.Search(t.Context(), "battery", memory.MaxK+1); !errors.Is(err, memory.ErrInvalid) {
		t.Errorf("k past MaxK: %v, want memory.ErrInvalid", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := b.Search(ctx, "battery", 0); !errors.Is(err, context.Canceled) {
		t.Errorf("canceled: %v, want context.Canceled", err)
	}
}

// TestSearchIsTheSameForEveryCaller: the base is the agent's, so every
// tenant and user, and a context with none, finds the same passages.
func TestSearchIsTheSameForEveryCaller(t *testing.T) {
	t.Parallel()
	b := newBase(t, knowledge.Config{})
	want, err := b.Search(t.Context(), "battery claim", 5)
	if err != nil || len(want) == 0 {
		t.Fatalf("no tenant: %v, %d hits", err, len(want))
	}
	for _, ctx := range []context.Context{memorytest.Ctx(t, "acme", ""), memorytest.Ctx(t, "globex", "u1")} {
		got, err := b.Search(ctx, "battery claim", 5)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(got, want) {
			t.Errorf("tenant %v: %+v, want %+v", tenancy.MustFrom(ctx).ID(), got, want)
		}
	}
}

func TestSemanticSearch(t *testing.T) {
	t.Parallel()
	e := memorytest.NewFakeEmbedder(memorytest.DefaultFakeModel, memorytest.DefaultFakeDim)
	b := newBase(t, knowledge.Config{Embedder: e})
	if got := b.Modes(); !slices.Equal(got, []memory.Mode{memory.ModeHybrid, memory.ModeKeyword, memory.ModeSemantic}) {
		t.Errorf("modes %v, want hybrid, keyword, semantic", got)
	}
	// Every passage was embedded as a document, once.
	var docs int
	for _, c := range e.Calls() {
		if c.Purpose != memory.PurposeDocument {
			t.Errorf("an indexing call with purpose %q", c.Purpose)
		}
		docs += len(c.Texts)
	}
	if docs != b.Passages() {
		t.Errorf("embedded %d texts, want the %d passages", docs, b.Passages())
	}
	// No word of the query is in travel.txt; its meaning is.
	hits, err := b.Search(t.Context(), "plane flights", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Source != "travel.txt" {
		t.Errorf("hits %+v, want travel.txt", hits)
	}
	if calls := e.Calls(); calls[len(calls)-1].Purpose != memory.PurposeQuery {
		t.Errorf("the query was embedded as %q", calls[len(calls)-1].Purpose)
	}
}

func TestNewFailsWhenTheEmbedderDoes(t *testing.T) {
	t.Parallel()
	e := memorytest.NewFakeEmbedder(memorytest.DefaultFakeModel, memorytest.DefaultFakeDim)
	boom := errors.New("embedding API down")
	e.SetErr(boom)
	if _, err := knowledge.New(t.Context(), loadHandbook(t), knowledge.Config{Embedder: e}); !errors.Is(err, boom) {
		t.Errorf("New: %v, want the embedder's error", err)
	}
}

func TestDocuments(t *testing.T) {
	t.Parallel()
	b := newBase(t, knowledge.Config{})
	docs := b.Documents()
	if len(docs) != 4 {
		t.Fatalf("%d documents, want 4", len(docs))
	}
	docs[0].Text = "changed" // a copy
	d, ok := b.Document("returns.md")
	if !ok || !strings.HasPrefix(d.Text, "# Returns and refunds") {
		t.Errorf("Document(returns.md) = %.40q, %v", d.Text, ok)
	}
	if _, ok := b.Document("missing.md"); ok {
		t.Error("Document(missing.md) found one")
	}
}
