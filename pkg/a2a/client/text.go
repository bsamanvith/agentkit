package client

import (
	"encoding/base64"
	"fmt"
	"strings"
	"unicode"

	"github.com/bsamanvith/agentkit/pkg/a2a"
)

// Answer is a task's output as text: each artifact's parts, the artifacts
// one after the other with a blank line between them, and an artifact
// other than the first headed by its name. A task without artifacts gives
// the text of its status message.
func Answer(t a2a.Task) string {
	var b strings.Builder
	for i, a := range t.Artifacts {
		if i > 0 {
			b.WriteString("\n\n")
			if a.Name != "" {
				fmt.Fprintf(&b, "[%s]\n", a.Name)
			}
		}
		b.WriteString(PartsText(a.Parts))
	}
	if b.Len() == 0 {
		return MessageText(t.Status.Message)
	}
	return b.String()
}

// MessageText is the text of a message's parts; "" for none.
func MessageText(m *a2a.Message) string {
	if m == nil {
		return ""
	}
	return PartsText(m.Parts)
}

// PartsText is parts as text: text parts as they are, data parts as their
// JSON, and a file part as a line naming it (its bytes are not text).
func PartsText(parts []a2a.Part) string {
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		switch {
		case p.Text != nil:
			out = append(out, *p.Text)
		case p.Data != nil:
			out = append(out, string(*p.Data))
		case p.URL != nil:
			out = append(out, fmt.Sprintf("[file %s: %s]", fileName(p), *p.URL))
		case p.Raw != nil:
			out = append(out, fmt.Sprintf("[file %s: %d bytes, not shown]", fileName(p), base64.StdEncoding.DecodedLen(len(*p.Raw))))
		}
	}
	return strings.Join(out, "\n")
}

func fileName(p a2a.Part) string {
	name := p.Filename
	if name == "" {
		name = "(unnamed)"
	}
	if p.MediaType != "" {
		name += " (" + p.MediaType + ")"
	}
	return name
}

// sanitize cleans text from a remote agent's card before a model reads it
// as part of a tool description: it drops what a person reviewing it would
// not see (control characters other than newline and tab, format
// characters such as zero-width spaces and bidirectional overrides,
// variation selectors) and keeps at most n runes, marking the cut.
func sanitize(s string, n int) string {
	s = strings.ToValidUTF8(s, "�")
	var b strings.Builder
	kept := 0
	for _, r := range s {
		if hidden(r) {
			continue
		}
		if kept == n {
			b.WriteString("…")
			break
		}
		b.WriteRune(r)
		kept++
	}
	return strings.TrimSpace(b.String())
}

func hidden(r rune) bool {
	switch r {
	case '\n', '\t':
		return false
	case 'ᅟ', 'ᅠ', 'ㅤ', 'ﾠ', '͏', '᠎':
		return true
	}
	return unicode.IsControl(r) ||
		unicode.In(r, unicode.Cf, unicode.Bidi_Control, unicode.Variation_Selector, unicode.Zl, unicode.Zp, unicode.Noncharacter_Code_Point)
}
