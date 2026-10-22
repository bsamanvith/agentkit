package knowledge

import (
	"strings"
	"unicode/utf8"
)

// A Passage is one piece of a [Document], the unit a search finds.
type Passage struct {
	// Source is the document's path in the base, Title its title.
	Source string
	Title  string
	// Section is the path of headings the passage sits under, joined by
	// " > " ("Returns > Refunds"); empty before the first heading.
	Section string
	// Part numbers the passages of a document from 0, in order.
	Part int
	// Text is the passage's text, a part of the document's, trimmed of
	// surrounding white space.
	Text string
}

// Split cuts doc into passages of at most maxBytes bytes of text each, in
// the document's order. A passage never crosses a Markdown heading (an ATX
// heading, "#" to "######", outside a fenced code block); a section longer
// than maxBytes is cut at its last paragraph break that fits, else its last
// line break, else its last space, else at a character boundary. A heading
// with no text of its own before the next one joins that next section. The
// passages' texts are the document's text with only white space lost: at
// the cuts and where a part was white space alone. maxBytes below
// [MinChunkBytes] counts as MinChunkBytes.
func Split(doc Document, maxBytes int) []Passage {
	maxBytes = max(maxBytes, MinChunkBytes)
	var out []Passage
	for _, s := range sections(doc.Text) {
		for p := s.start; p < s.end; {
			end := cut(doc.Text, p, s.end, maxBytes)
			if t := strings.TrimSpace(doc.Text[p:end]); t != "" {
				out = append(out, Passage{
					Source: doc.Source, Title: doc.Title, Section: s.path, Part: len(out), Text: t,
				})
			}
			p = end
		}
	}
	return out
}

// section is a run of a text from one heading to the next.
type section struct {
	start, end int
	path       string
}

// sections cuts text at its headings. Every byte of text is in one section.
func sections(text string) []section {
	var out []section
	var stack []string // stack[i] is the heading of level i+1, "" when none
	cur := section{}
	body := false // cur has text besides its heading line
	fence := ""   // the open code fence's marker, "" outside one
	for p := 0; p < len(text); {
		end := strings.IndexByte(text[p:], '\n') + p + 1
		if end == p { // no newline: the last line
			end = len(text)
		}
		line := text[p:end]
		if level, title, ok := heading(line); ok && fence == "" {
			if p > cur.start {
				if body {
					cur.end = p
					out = append(out, cur)
					cur = section{start: p}
				}
				// else a heading with no text of its own: it joins this one
			}
			stack = append(stack[:min(len(stack), level-1)], make([]string, max(0, level-1-len(stack)))...)
			stack = append(stack, title)
			cur.path = joinPath(stack)
			body = false
		} else {
			fence = nextFence(fence, line)
			if strings.TrimSpace(line) != "" {
				body = true
			}
		}
		p = end
	}
	cur.end = len(text)
	if cur.end > cur.start {
		out = append(out, cur)
	}
	return out
}

func joinPath(stack []string) string {
	var parts []string
	for _, h := range stack {
		if h != "" {
			parts = append(parts, h)
		}
	}
	return strings.Join(parts, " > ")
}

// heading reports whether line is an ATX heading, with its level and text.
func heading(line string) (int, string, bool) {
	s := strings.TrimRight(line, "\r\n")
	indent := len(s) - len(strings.TrimLeft(s, " "))
	if indent > 3 {
		return 0, "", false
	}
	s = s[indent:]
	level := len(s) - len(strings.TrimLeft(s, "#"))
	if level < 1 || level > 6 {
		return 0, "", false
	}
	rest := s[level:]
	if rest != "" && rest[0] != ' ' && rest[0] != '\t' {
		return 0, "", false // "#hashtag" is text
	}
	title := strings.TrimSpace(rest)
	// A closing run of #s, after a space, is not part of the title.
	if t := strings.TrimRight(title, "#"); t != title && (t == "" || strings.HasSuffix(t, " ") || strings.HasSuffix(t, "\t")) {
		title = strings.TrimSpace(t)
	}
	return level, title, true
}

// nextFence returns the open code fence after line, given the one open
// before it ("" for none).
func nextFence(fence, line string) string {
	m, rest := fenceMarker(line)
	switch {
	case m == "":
		return fence
	case fence == "":
		return m
	case m[0] == fence[0] && len(m) >= len(fence) && strings.TrimSpace(rest) == "":
		return "" // a closing fence: as long, and nothing after it
	}
	return fence
}

// fenceMarker returns the run of backticks or tildes (three or more) that
// starts line, after at most three spaces, and the rest of the line; "" when
// it starts none.
func fenceMarker(line string) (marker, rest string) {
	s := strings.TrimRight(line, "\r\n")
	indent := len(s) - len(strings.TrimLeft(s, " "))
	if indent > 3 {
		return "", ""
	}
	s = s[indent:]
	for _, c := range "`~" {
		n := len(s) - len(strings.TrimLeft(s, string(c)))
		if n >= 3 {
			return s[:n], s[n:]
		}
	}
	return "", ""
}

// cut returns where the passage that starts at p in text ends: end when
// text[p:end] fits in maxBytes, else the best break within maxBytes.
func cut(text string, p, end, maxBytes int) int {
	if end-p <= maxBytes {
		return end
	}
	window := text[p : p+maxBytes]
	floor := maxBytes / 4 // a break this early would leave a tiny passage
	if i := strings.LastIndex(window, "\n\n"); i >= floor {
		return p + i + 2
	}
	if i := strings.LastIndexByte(window, '\n'); i >= floor {
		return p + i + 1
	}
	if i := strings.LastIndexAny(window, " \t"); i >= floor {
		return p + i + 1
	}
	n := p + maxBytes
	for n > p && !utf8.RuneStart(text[n]) {
		n--
	}
	if n == p { // cannot happen for valid UTF-8 and maxBytes >= 4
		n = p + maxBytes
	}
	return n
}
