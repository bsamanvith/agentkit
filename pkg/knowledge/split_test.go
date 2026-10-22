package knowledge_test

import (
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/bsamanvith/agentkit/pkg/knowledge"
)

// part is what a test compares of a passage.
type part struct{ section, text string }

func parts(ps []knowledge.Passage) []part {
	out := make([]part, len(ps))
	for i, p := range ps {
		out[i] = part{p.Section, p.Text}
	}
	return out
}

func TestSplit(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("word ", 50) // 250 bytes
	tests := []struct {
		name string
		text string
		max  int
		want []part
	}{
		{"no headings", "One line.\n\nAnother.\n", 0, []part{{"", "One line.\n\nAnother."}}},
		{
			"sections and their paths",
			"Intro.\n# Guide\nAbout.\n## Returns\nWithin 45 days.\n### Fees\n12%.\n## Shipping\nFree.\n",
			0,
			[]part{
				{"", "Intro."},
				{"Guide", "# Guide\nAbout."},
				{"Guide > Returns", "## Returns\nWithin 45 days."},
				{"Guide > Returns > Fees", "### Fees\n12%."},
				{"Guide > Shipping", "## Shipping\nFree."},
			},
		},
		{
			"a heading with no text joins the next section",
			"# Guide\n\n## Returns\nWithin 45 days.\n",
			0,
			[]part{{"Guide > Returns", "# Guide\n\n## Returns\nWithin 45 days."}},
		},
		{
			"a skipped level",
			"# A\nx\n### C\ny\n",
			0,
			[]part{{"A", "# A\nx"}, {"A > C", "### C\ny"}},
		},
		{
			"a heading in a code fence is text",
			"# A\n```sh\n# not a heading\n```\n~~~\n## nor this\n~~~~\n## B\nz\n",
			0,
			[]part{{"A", "# A\n```sh\n# not a heading\n```\n~~~\n## nor this\n~~~~"}, {"A > B", "## B\nz"}},
		},
		{
			"not headings: a hashtag, seven #s, four spaces in",
			"#tag\n####### seven\n    # code\n",
			0,
			[]part{{"", "#tag\n####### seven\n    # code"}},
		},
		{
			"closing #s and indent",
			"  ## Returns ##\nok\n",
			0,
			[]part{{"Returns", "## Returns ##\nok"}},
		},
		{
			"a long section is cut at paragraph breaks",
			"# A\n" + long + "\n\n" + long + "\n\n" + long + "\n",
			300,
			[]part{
				{"A", "# A\n" + strings.TrimSpace(long)},
				{"A", strings.TrimSpace(long)},
				{"A", strings.TrimSpace(long)},
			},
		},
		{
			"a paragraph break wins over a later line break",
			strings.Repeat("p", 99) + "\n\n" + strings.Repeat("line\n", 50),
			200,
			[]part{
				{"", strings.Repeat("p", 99)},
				{"", strings.TrimSpace(strings.Repeat("line\n", 40))},
				{"", strings.TrimSpace(strings.Repeat("line\n", 10))},
			},
		},
		{
			"a break in the first quarter is not used",
			"ab\n\n" + strings.Repeat("x", 300),
			200,
			[]part{{"", "ab\n\n" + strings.Repeat("x", 196)}, {"", strings.Repeat("x", 104)}},
		},
		{
			"a paragraph with no break is cut at spaces",
			strings.Repeat("abcd ", 100),
			200,
			[]part{
				{"", strings.TrimSpace(strings.Repeat("abcd ", 40))},
				{"", strings.TrimSpace(strings.Repeat("abcd ", 40))},
				{"", strings.TrimSpace(strings.Repeat("abcd ", 20))},
			},
		},
		{"only white space", " \n\t\n", 0, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := parts(knowledge.Split(knowledge.Document{Source: "a.md", Title: "A", Text: tt.text}, tt.max))
			if len(got) != len(tt.want) {
				t.Fatalf("got %d passages %q, want %d %q", len(got), got, len(tt.want), tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("passage %d = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestSplitNumbersAndLabelsPassages(t *testing.T) {
	t.Parallel()
	doc := knowledge.Document{Source: "x/y.md", Title: "Y", Text: "# Y\na\n## B\nb\n## C\nc\n"}
	for i, p := range knowledge.Split(doc, 0) {
		if p.Part != i || p.Source != "x/y.md" || p.Title != "Y" {
			t.Errorf("passage %d: part %d, source %q, title %q", i, p.Part, p.Source, p.Title)
		}
	}
}

func TestSplitCutsAWordAtACharacterBoundary(t *testing.T) {
	t.Parallel()
	text := strings.Repeat("é", 300) // 600 bytes, no break
	ps := knowledge.Split(knowledge.Document{Source: "a.md", Text: text}, knowledge.MinChunkBytes)
	if len(ps) != 3 {
		t.Fatalf("got %d passages, want 3", len(ps))
	}
	for _, p := range ps {
		if !utf8.ValidString(p.Text) || len(p.Text) != 200 {
			t.Errorf("passage of %d bytes, valid %v; want 200 valid", len(p.Text), utf8.ValidString(p.Text))
		}
	}
}

// checkSplit checks Split's invariants: every passage fits, is not empty,
// is valid UTF-8 when the text is, is numbered in order, and together the
// passages are the text with only white space lost.
func checkSplit(t *testing.T, text string, maxBytes int) {
	t.Helper()
	ps := knowledge.Split(knowledge.Document{Source: "f.md", Text: text}, maxBytes)
	limit := max(maxBytes, knowledge.MinChunkBytes)
	var all strings.Builder
	for i, p := range ps {
		switch {
		case p.Part != i:
			t.Fatalf("passage %d has part %d", i, p.Part)
		case len(p.Text) > limit:
			t.Fatalf("passage %d has %d bytes, more than %d", i, len(p.Text), limit)
		case strings.TrimSpace(p.Text) == "":
			t.Fatalf("passage %d is empty", i)
		case utf8.ValidString(text) && !utf8.ValidString(p.Text):
			t.Fatalf("passage %d is not UTF-8", i)
		}
		all.WriteString(p.Text)
	}
	if got, want := noSpace(all.String()), noSpace(text); got != want {
		t.Fatalf("passages lost text:\n got %q\nwant %q", got, want)
	}
}

func noSpace(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, s)
}

func TestSplitInvariantsOnTheHandbook(t *testing.T) {
	t.Parallel()
	for _, d := range loadHandbook(t) {
		for _, n := range []int{0, knowledge.MinChunkBytes, 500, knowledge.MaxChunkBytes} {
			checkSplit(t, d.Text, n)
		}
	}
}

func FuzzSplit(f *testing.F) {
	f.Add("# A\nx\n## B\n```\n# c\n```\ny\n", 200)
	f.Add(strings.Repeat("a b\n\n", 200), 250)
	f.Add(strings.Repeat("ü", 500), 201)
	f.Add("\xff\xfe# x\n"+strings.Repeat("z", 900), 300)
	f.Add("   # \n#\n# #\n~~~\n```\n", 0)
	f.Fuzz(func(t *testing.T, text string, maxBytes int) {
		if maxBytes > 4*knowledge.MaxChunkBytes || maxBytes < -1 {
			maxBytes = 0
		}
		checkSplit(t, text, maxBytes)
	})
}
