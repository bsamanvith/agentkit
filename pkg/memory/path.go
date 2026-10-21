package memory

import (
	"fmt"
	"net/url"
	"strings"
	"unicode/utf8"
)

// Root is the directory every path is under.
const Root = "/memories"

// MaxPathBytes bounds a path, before and after cleaning; MaxNameBytes one
// name in it.
const (
	MaxPathBytes = 256
	MaxNameBytes = 128
)

// CleanPath returns p in its one form: Root, or Root and names joined by
// single slashes, with "." segments and repeated or trailing slashes dropped.
// It refuses, with ErrInvalidPath, a path that is empty or longer than
// MaxPathBytes, not under Root, not valid UTF-8, or that has a ".." segment,
// a name starting with "." (hidden, and so every traversal), a backslash, a
// control character, a name longer than MaxNameBytes, or a percent escape
// that decodes to any of those or to a slash.
func CleanPath(p string) (string, error) {
	bad := func(why string) (string, error) {
		return "", fmt.Errorf("%w: %q: %s", ErrInvalidPath, truncate(p), why)
	}
	switch {
	case p == "":
		return bad("empty")
	case len(p) > MaxPathBytes:
		return bad(fmt.Sprintf("longer than %d bytes", MaxPathBytes))
	case !utf8.ValidString(p):
		return bad("not UTF-8")
	}
	rest, ok := strings.CutPrefix(p, Root)
	if !ok || (rest != "" && rest[0] != '/') {
		return bad("not under " + Root)
	}
	var b strings.Builder
	b.WriteString(Root)
	for name := range strings.SplitSeq(rest, "/") {
		if name == "" || name == "." {
			continue
		}
		if why := badName(name); why != "" {
			return bad(why)
		}
		if strings.Contains(name, "%") {
			dec, err := url.PathUnescape(name)
			if err != nil {
				return bad("bad percent escape")
			}
			if dec == "." || strings.Contains(dec, "/") {
				return bad("escaped traversal")
			}
			if why := badName(dec); why != "" {
				return bad("escaped " + why)
			}
		}
		b.WriteByte('/')
		b.WriteString(name)
	}
	if b.Len() > MaxPathBytes {
		return bad(fmt.Sprintf("longer than %d bytes", MaxPathBytes))
	}
	return b.String(), nil
}

// badName says what is wrong with one name of a path, or "" if nothing is.
func badName(name string) string {
	switch {
	case name == "..":
		return `".." is not allowed`
	case strings.HasPrefix(name, "."):
		return `a name starting with "." is not allowed`
	case len(name) > MaxNameBytes:
		return fmt.Sprintf("a name is longer than %d bytes", MaxNameBytes)
	}
	for _, r := range name {
		if r == '\\' {
			return "backslash"
		}
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			return "control character"
		}
	}
	return ""
}

func truncate(p string) string {
	if len(p) <= 80 {
		return p
	}
	return p[:80] + "..."
}

// Within reports whether path is dir or under it. Both must be clean.
func Within(path, dir string) bool {
	return path == dir || strings.HasPrefix(path, dir+"/")
}

// Depth is how many names path has under Root (0 for Root). It must be
// clean.
func Depth(path string) int {
	return strings.Count(path, "/") - strings.Count(Root, "/")
}

// Parents returns the directories path is under, from Root's child down to
// its parent; Root itself is not among them. path must be clean.
func Parents(path string) []string {
	var out []string
	for i := len(Root) + 1; i < len(path); i++ {
		if path[i] == '/' {
			out = append(out, path[:i])
		}
	}
	return out
}
