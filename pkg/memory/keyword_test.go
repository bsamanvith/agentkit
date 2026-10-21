package memory_test

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/bsamanvith/agentkit/pkg/memory"
)

func TestTokenize(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"Hello, World!", []string{"hello", "world"}},
		{"don't stop", []string{"don't", "stop"}},
		{"dogs' bowls", []string{"dogs", "bowls"}},
		{"see example.com or e.g. this", []string{"see", "example.com", "or", "e.g", "this"}},
		{"pi is 3.14, not 3,14; 1,000 items", []string{"pi", "is", "3.14", "not", "3,14", "1,000", "items"}},
		{"end. Next", []string{"end", "next"}},
		{"a..b", []string{"a", "b"}},
		{"snake_case and kebab-case", []string{"snake_case", "and", "kebab", "case"}},
		{"Café ÉLAN naïve", []string{"café", "élan", "naïve"}},
		{"café", []string{"café"}}, // a combining mark stays in the word
		{"東京タワー", []string{"東", "京", "タワー"}},
		{"ひらがな", []string{"ひ", "ら", "が", "な"}},
		{"Привет мир", []string{"привет", "мир"}},
		{"emoji 🎉 party", []string{"emoji", "party"}},
		{"tabs\tand\nnewlines", []string{"tabs", "and", "newlines"}},
		{"bad \xff utf8", []string{"bad", "utf8"}},
		{"v1.2.3", []string{"v1.2.3"}},
		{"key:value", []string{"key:value"}},
		{"trailing.", []string{"trailing"}},
	} {
		if got := memory.Tokenize(c.in); !slices.Equal(got, c.want) {
			t.Errorf("Tokenize(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestParseKeywordQuery(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		in      string
		phrases [][]string
		terms   []string
	}{
		{"red fox", nil, []string{"red", "fox"}},
		{`"red fox" den`, [][]string{{"red", "fox"}}, []string{"den"}},
		{`“curly quotes” too`, [][]string{{"curly", "quotes"}}, []string{"too"}},
		{`"open quote runs on`, [][]string{{"open", "quote", "runs", "on"}}, nil},
		{`fox "fox" FOX`, [][]string{{"fox"}}, nil},
		{`"a b" "A, B" a`, [][]string{{"a", "b"}}, []string{"a"}},
		{`"" fox ""`, nil, []string{"fox"}},
	} {
		got, err := memory.ParseKeywordQuery(c.in)
		if err != nil {
			t.Errorf("ParseKeywordQuery(%q): %v", c.in, err)
			continue
		}
		if !slices.EqualFunc(got.Phrases, c.phrases, slices.Equal) || !slices.Equal(got.Terms, c.terms) {
			t.Errorf("ParseKeywordQuery(%q) = %q %q, want %q %q", c.in, got.Phrases, got.Terms, c.phrases, c.terms)
		}
	}
	for _, in := range []string{"", `""`, "!!! ...", `" "`} {
		if _, err := memory.ParseKeywordQuery(in); !errors.Is(err, memory.ErrInvalid) {
			t.Errorf("ParseKeywordQuery(%q) = %v, want ErrInvalid", in, err)
		}
	}
}

// FuzzQuery: the query syntax never panics, and what it parses holds: at
// most MaxQueryParts parts, no empty phrase, no word that Tokenize would
// split again, and no part twice.
func FuzzQuery(f *testing.F) {
	for _, s := range []string{
		`red fox`, `"red fox" den`, `"`, `"""`, `"a"b"c`, "“”", `" "`, "don't", "3.14", "東京", "\xff\"\xfe",
		strings.Repeat(`"x `, 40), "a'", "'a", "a''b", "a.:b", "́́", "_", "1,", ",1",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		for _, w := range memory.Tokenize(s) {
			if w == "" || !utf8.ValidString(w) {
				t.Fatalf("Tokenize(%q) has word %q", s, w)
			}
			if again := memory.Tokenize(w); len(again) != 1 || again[0] != w {
				t.Fatalf("Tokenize(%q) has word %q, which tokenizes to %q", s, w, again)
			}
		}
		q, err := memory.ParseKeywordQuery(s)
		if err != nil {
			if !errors.Is(err, memory.ErrInvalid) {
				t.Fatalf("ParseKeywordQuery(%q): %v, not ErrInvalid", s, err)
			}
			return
		}
		parts := q.Parts()
		if len(parts) == 0 || len(parts) > memory.MaxQueryParts {
			t.Fatalf("ParseKeywordQuery(%q): %d parts", s, len(parts))
		}
		for _, p := range q.Phrases {
			if len(p) == 0 {
				t.Fatalf("ParseKeywordQuery(%q): an empty phrase", s)
			}
		}
		seen := map[string]bool{}
		for i, p := range parts {
			key := p
			if i >= len(q.Phrases) {
				key = "term:" + p
			}
			if seen[key] {
				t.Fatalf("ParseKeywordQuery(%q): %q twice", s, p)
			}
			seen[key] = true
		}
	})
}
