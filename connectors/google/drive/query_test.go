package drive

import (
	"math/rand/v2"
	"strings"
	"testing"
)

// scanQuoted reads the single-quoted string at the start of s, the way Drive
// does (a backslash escapes the next character), and returns the rest.
func scanQuoted(s string) (value, rest string, ok bool) {
	if s == "" || s[0] != '\'' {
		return "", "", false
	}
	var b strings.Builder
	for i := 1; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
			if i == len(s) {
				return "", "", false
			}
			b.WriteByte(s[i])
		case '\'':
			return b.String(), s[i+1:], true
		default:
			b.WriteByte(s[i])
		}
	}
	return "", "", false
}

// TestQuoteCannotBreakOut: whatever the text, the quoted string holds exactly
// that text and nothing follows it, so no term of the text's own can join the
// query.
func TestQuoteCannotBreakOut(t *testing.T) {
	t.Parallel()
	alphabet := []rune(`'\ "()=<>andor trashed contains é世` + "\n\t")
	rng := rand.New(rand.NewPCG(1, 2))
	inputs := []string{"", `'`, `\`, `\'`, `'\`, `\\'`, `' or trashed = true or name contains '`, `x\`}
	for range 5000 {
		var b strings.Builder
		for range rng.IntN(24) {
			b.WriteRune(alphabet[rng.IntN(len(alphabet))])
		}
		inputs = append(inputs, b.String())
	}
	for _, in := range inputs {
		got, rest, ok := scanQuoted(quote(in))
		if !ok || rest != "" || got != in {
			t.Fatalf("quote(%q) = %q reads back as %q, rest %q, ok %v", in, quote(in), got, rest, ok)
		}
	}
}

func TestSearchQueryBuildsOneExpression(t *testing.T) {
	t.Parallel()
	got, err := searchQuery(searchIn{Query: `it's`, MimeType: "folder", FolderID: "root"})
	want := `trashed = false and (name contains 'it\'s' or fullText contains 'it\'s') and mimeType = 'application/vnd.google-apps.folder' and 'root' in parents`
	if err != nil || got != want {
		t.Errorf("q = %q, %v\nwant %q", got, err, want)
	}
	const raw = `sharedWithMe and starred = true and 'x' in owners`
	if got, err := searchQuery(searchIn{DriveQuery: raw}); err != nil || got != raw {
		t.Errorf("drive_query = %q, %v; want it unchanged", got, err)
	}
	for name, in := range map[string]searchIn{
		"drive_query and query":    {DriveQuery: "starred", Query: "a"},
		"drive_query and mime":     {DriveQuery: "starred", MimeType: "text/plain"},
		"drive_query and folder":   {DriveQuery: "starred", FolderID: "root"},
		"mime type with a quote":   {MimeType: "text/plain' or '1'='1"},
		"folder id with a quote":   {FolderID: "a' in parents or 'b"},
		"folder id with a newline": {FolderID: "a\nb"},
	} {
		if _, err := searchQuery(in); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}
