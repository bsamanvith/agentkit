package memory_test

import (
	"errors"
	"path"
	"strings"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/memory"
)

func TestCleanPath(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ in, want string }{
		{"/memories", "/memories"},
		{"/memories/", "/memories"},
		{"/memories/notes.md", "/memories/notes.md"},
		{"/memories//a/./b/", "/memories/a/b"},
		{"/memories/a b/ü.md", "/memories/a b/ü.md"},
		{"/memories/100%25.md", "/memories/100%25.md"},
	} {
		if got, err := memory.CleanPath(c.in); err != nil || got != c.want {
			t.Errorf("CleanPath(%q) = %q, %v; want %q", c.in, got, err, c.want)
		}
	}
	for _, in := range []string{
		"", "/", "memories", "/memoriesx/a", "/etc/passwd", "/memories/..", "/memories/../etc",
		"/memories/a/../../etc", "/memories/.env", `/memories/a\b`, "/memories/%2e%2e/x",
		"/memories/%2E%2e", "/memories/a%2fb", "/memories/a%5cb", "/memories/%zz", "/memories/a\x00",
		"/memories/a\nb", "/memories/\xff", "/memories/" + strings.Repeat("a", memory.MaxNameBytes+1),
		"/memories/" + strings.Repeat("a/", memory.MaxPathBytes),
		// Short once cleaned, but too long as given.
		"/memories/" + strings.Repeat("./", memory.MaxPathBytes/2) + "x",
	} {
		if got, err := memory.CleanPath(in); !errors.Is(err, memory.ErrInvalidPath) {
			t.Errorf("CleanPath(%q) = %q, %v; want ErrInvalidPath", in, got, err)
		}
	}
}

// FuzzMemoryPath: whatever the input, a path CleanPath accepts is Root or
// under it, has no "..", is its own clean form, and stays within the limits.
func FuzzMemoryPath(f *testing.F) {
	for _, s := range []string{
		"/memories/a", "/memories/../x", "/memories/a/../../b", "/memories//./a/", "/memories/%2e%2e/%2f",
		"/memories/..\\..", "/memoriesx", "/memories/\x00", "/memories/a/.", "../memories", "/memories/...",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		got, err := memory.CleanPath(in)
		if err != nil {
			if !errors.Is(err, memory.ErrInvalidPath) || got != "" {
				t.Fatalf("CleanPath(%q) = %q, %v: a refusal must be ErrInvalidPath and no path", in, got, err)
			}
			return
		}
		if !memory.Within(got, memory.Root) {
			t.Fatalf("CleanPath(%q) = %q, outside %s", in, got, memory.Root)
		}
		if path.Clean(got) != got || strings.Contains(got, "\\") || len(got) > memory.MaxPathBytes {
			t.Fatalf("CleanPath(%q) = %q, not clean", in, got)
		}
		for _, name := range strings.Split(got, "/")[1:] {
			if name == ".." || strings.HasPrefix(name, ".") {
				t.Fatalf("CleanPath(%q) = %q, with a name %q", in, got, name)
			}
		}
		// Resolving it as a file path keeps it under Root too.
		if r := path.Join("/", got); !memory.Within(r, memory.Root) {
			t.Fatalf("CleanPath(%q) = %q resolves to %q", in, got, r)
		}
		if again, err := memory.CleanPath(got); err != nil || again != got {
			t.Fatalf("CleanPath(%q) = %q, %v: not idempotent", got, again, err)
		}
	})
}
