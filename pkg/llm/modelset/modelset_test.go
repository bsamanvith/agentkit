package modelset_test

import (
	"errors"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/llm/modelset"
)

func known(p string) bool { return p == "anthropic" || p == "openai" }

func mustNew(t *testing.T, def string, s modelset.Spec) *modelset.Set {
	t.Helper()
	set, err := modelset.New(def, s, known)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return set
}

func TestParse(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		in, provider, id string
		ok               bool
	}{
		{"claude-haiku-5-5", "anthropic", "claude-haiku-5-5", true},
		{"anthropic/claude-opus-5-5", "anthropic", "claude-opus-5-5", true},
		{"openai/gpt-5-mini", "openai", "gpt-5-mini", true},
		{"openai/org/model:latest", "openai", "org/model:latest", true}, // ids may hold "/" (Ollama, gateways)
		{"", "", "", false},
		{"anthropic/", "", "", false},
		{"/x", "", "", false},
		{"Anthropic/x", "", "", false},
		{"anthropic/a b", "", "", false},
		{"anthropic/a\nb", "", "", false},
		{"anthropic/" + strings.Repeat("a", modelset.MaxNameLen), "", "", false},
	} {
		p, id, err := modelset.Parse(tc.in)
		if (err == nil) != tc.ok {
			t.Errorf("Parse(%q) error %v, want ok %v", tc.in, err, tc.ok)
			continue
		}
		if !tc.ok {
			if !errors.Is(err, modelset.ErrInvalidName) {
				t.Errorf("Parse(%q) = %v, want ErrInvalidName", tc.in, err)
			}
			continue
		}
		if p != tc.provider || id != tc.id {
			t.Errorf("Parse(%q) = %q, %q; want %q, %q", tc.in, p, id, tc.provider, tc.id)
		}
	}
}

// The criterion's agent: one name and one pattern allowed; another model of
// the same family refused.
func TestAllowsNamesAndPatterns(t *testing.T) {
	t.Parallel()
	set := mustNew(t, "claude-haiku-5-5", modelset.Spec{
		Allow:         []string{"anthropic/claude-sonnet-5-5"},
		AllowPatterns: []string{"anthropic/claude-opus-.*"},
	})
	for name, want := range map[string]bool{
		"claude-haiku-5-5":            true, // the default, bare
		"anthropic/claude-haiku-5-5":  true,
		"claude-sonnet-5-5":           true,
		"anthropic/claude-opus-5-5":   true,
		"anthropic/claude-opus-6":     true,
		"anthropic/claude-fable-5-1":  false,
		"openai/claude-opus-5-5":      false, // another provider
		"anthropic/xclaude-opus-5-5":  false, // anchored at the start
		"anthropic/claude-opus-5-5\t": false,
		"anthropic/claude-sonnet-5-6": false,
		"":                            false,
	} {
		if got := set.Allows(name); got != want {
			t.Errorf("Allows(%q) = %v, want %v", name, got, want)
		}
	}
	if got, err := set.Resolve(""); err != nil || got != "anthropic/claude-haiku-5-5" {
		t.Errorf(`Resolve("") = %q, %v; want the default`, got, err)
	}
	if _, err := set.Resolve("anthropic/claude-fable-5-1"); !errors.Is(err, modelset.ErrNotAllowed) {
		t.Errorf("Resolve(not allowed) = %v, want ErrNotAllowed", err)
	}
	if _, err := set.Resolve("a b"); !errors.Is(err, modelset.ErrInvalidName) {
		t.Errorf("Resolve(bad name) = %v, want ErrInvalidName", err)
	}
}

// A pattern matches the whole name even when it has its own alternation:
// "a|b" must not allow "xa" or "bx".
func TestPatternsAreAnchored(t *testing.T) {
	t.Parallel()
	set := mustNew(t, "claude-haiku-5-5", modelset.Spec{AllowPatterns: []string{"anthropic/a|anthropic/b"}})
	for name, want := range map[string]bool{
		"anthropic/a": true, "anthropic/b": true,
		"anthropic/ab": false, "anthropic/bx": false, "anthropic/xa": false,
	} {
		if got := set.Allows(name); got != want {
			t.Errorf("Allows(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestSingle(t *testing.T) {
	t.Parallel()
	if !mustNew(t, "claude-haiku-5-5", modelset.Spec{}).Single() {
		t.Error("an empty spec should allow the default only")
	}
	if !mustNew(t, "claude-haiku-5-5", modelset.Spec{Allow: []string{"anthropic/claude-haiku-5-5"}}).Single() {
		t.Error("allowing the default again should still be single")
	}
	if mustNew(t, "claude-haiku-5-5", modelset.Spec{AllowPatterns: []string{"x"}}).Single() {
		t.Error("a pattern allows more")
	}
}

func TestVariantsFirstMatchWins(t *testing.T) {
	t.Parallel()
	set := mustNew(t, "claude-haiku-5-5", modelset.Spec{
		Allow:         []string{"anthropic/claude-opus-5-5"},
		AllowPatterns: []string{"openai/.*"},
		Variants: []modelset.Variant{
			{Match: "anthropic/claude-opus-5-5", SystemAppend: "opus"},
			{Match: "re:anthropic/.*", System: "claude", Effort: "high"},
			{Match: "re:openai/.*", System: "gpt", ToolDescriptions: map[string]string{"echo": "say"}},
		},
	})
	for name, want := range map[string]string{
		"anthropic/claude-opus-5-5":   "append:opus",
		"claude-haiku-5-5":            "system:claude",
		"openai/gpt-5-mini":           "system:gpt",
		"anthropic/claude-sonnet-5-5": "system:claude", // a variant may match more than the set allows
	} {
		v, ok := set.Variant(name)
		got := "none"
		switch {
		case ok && v.SystemAppend != "":
			got = "append:" + v.SystemAppend
		case ok:
			got = "system:" + v.System
		}
		if got != want {
			t.Errorf("Variant(%q) = %s, want %s", name, got, want)
		}
	}
	v, _ := set.Variant("openai/x")
	v.ToolDescriptions["echo"] = "changed"
	if v2, _ := set.Variant("openai/x"); v2.ToolDescriptions["echo"] != "say" {
		t.Error("a returned variant shares its map with the set")
	}
}

func TestInvalidSpecs(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("a", modelset.MaxPatternLen+1)
	for _, tc := range []struct {
		name  string
		def   string
		spec  modelset.Spec
		field string
	}{
		{"bad default", "a b", modelset.Spec{}, "default"},
		{"unknown provider", "claude-haiku-5-5", modelset.Spec{Allow: []string{"x", "gemini/g"}}, "allow[1]"},
		{"bad pattern", "claude-haiku-5-5", modelset.Spec{AllowPatterns: []string{"ok", "("}}, "allow_patterns[1]"},
		{"empty pattern", "claude-haiku-5-5", modelset.Spec{AllowPatterns: []string{""}}, "allow_patterns[0]"},
		{"long pattern", "claude-haiku-5-5", modelset.Spec{AllowPatterns: []string{long}}, "allow_patterns[0]"},
		{"too many patterns", "claude-haiku-5-5", modelset.Spec{AllowPatterns: slices.Repeat([]string{"x"}, modelset.MaxPatterns+1)}, "allow_patterns"},
		{"too many names", "claude-haiku-5-5", modelset.Spec{Allow: slices.Repeat([]string{"x"}, modelset.MaxAllow+1)}, "allow"},
		{"empty match", "claude-haiku-5-5", modelset.Spec{Variants: []modelset.Variant{{}}}, "variants[0].match"},
		{"bad match pattern", "claude-haiku-5-5", modelset.Spec{Variants: []modelset.Variant{{Match: "re:("}}}, "variants[0].match"},
		{"match not allowed", "claude-haiku-5-5", modelset.Spec{Variants: []modelset.Variant{{Match: "anthropic/claude-opus-5-5"}}}, "variants[0].match"},
		{"system and append", "claude-haiku-5-5", modelset.Spec{Variants: []modelset.Variant{{Match: "claude-haiku-5-5", System: "a", SystemAppend: "b"}}}, "variants[0].system_append"},
		{"negative max tokens", "claude-haiku-5-5", modelset.Spec{Variants: []modelset.Variant{{Match: "claude-haiku-5-5", MaxTokens: -1}}}, "variants[0].max_tokens"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := modelset.New(tc.def, tc.spec, known)
			var errs modelset.Errors
			if !errors.As(err, &errs) {
				t.Fatalf("New = %v, want Errors", err)
			}
			if !slices.ContainsFunc(errs, func(f modelset.FieldError) bool { return f.Field == tc.field }) {
				t.Errorf("New = %v, want a problem at %s", errs, tc.field)
			}
		})
	}
}

func TestMatching(t *testing.T) {
	t.Parallel()
	set := mustNew(t, "claude-haiku-5-5", modelset.Spec{
		Allow:         []string{"openai/gpt-5-mini", "claude-haiku-5-5"},
		AllowPatterns: []string{"anthropic/claude-(opus|sonnet)-.*"},
	})
	got := set.Matching([]string{"claude-fable-5-1", "claude-opus-5-5", "anthropic/claude-sonnet-5-5", "claude-opus-5-5", "bad name"})
	want := []string{"anthropic/claude-haiku-5-5", "openai/gpt-5-mini", "anthropic/claude-opus-5-5", "anthropic/claude-sonnet-5-5"}
	if !slices.Equal(got, want) {
		t.Errorf("Matching = %q, want %q", got, want)
	}
}

func TestSameProvider(t *testing.T) {
	t.Parallel()
	if !modelset.SameProvider("claude-haiku-5-5", "anthropic/claude-opus-5-5") {
		t.Error("bare and anthropic names share a provider")
	}
	if modelset.SameProvider("anthropic/x", "openai/x") || modelset.SameProvider("", "") {
		t.Error("different or invalid providers matched")
	}
}

// FuzzAllows: matching never panics, an allowed name is always a valid
// one, and a pattern never allows a name it does not match whole.
func FuzzAllows(f *testing.F) {
	for _, s := range [][2]string{
		{"anthropic/claude-.*", "anthropic/claude-opus-5-5"},
		{"a|b", "anthropic/ab"},
		{"anthropic/(x", "anthropic/x"},
		{".*", "openai/gpt\x00"},
	} {
		f.Add(s[0], s[1])
	}
	f.Fuzz(func(t *testing.T, pattern, name string) {
		set, err := modelset.New("claude-haiku-5-5", modelset.Spec{AllowPatterns: []string{pattern}}, nil)
		if err != nil {
			return
		}
		if !set.Allows(name) {
			return
		}
		c, err := modelset.Canonical(name)
		if err != nil {
			t.Fatalf("Allows(%q) for a name that does not parse: %v", name, err)
		}
		if c == set.Default() {
			return
		}
		if !regexp.MustCompile(`^(?:` + pattern + `)$`).MatchString(c) {
			t.Fatalf("pattern %q allowed %q, which it does not match whole", pattern, c)
		}
	})
}
