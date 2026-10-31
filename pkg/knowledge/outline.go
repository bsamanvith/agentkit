package knowledge

import "strings"

// A Section is one heading of a [Document] and the text under it, down to
// the next heading of the same or a higher level: its subsections are
// inside it. Sections make a document a tree an agent can walk, chapter to
// section to concept, besides searching it.
type Section struct {
	// Path is the headings from the top down to this one, joined by " > "
	// as Passage.Section has them ("Pulses > Pigeon pea").
	Path string
	// Title is the heading's own text, Level its depth (1 for "#").
	Title string
	Level int
	// Start and End bound the section in the document's text: from its
	// heading line to the next heading of its level or above.
	Start, End int
}

// Outline returns doc's sections in the document's order: its tree of
// headings, outside fenced code blocks.
func Outline(doc Document) []Section {
	var out []Section
	var stack []string
	var open []int // indexes in out of the sections still open, by level
	fence := ""
	text := doc.Text
	for p := 0; p < len(text); {
		end := strings.IndexByte(text[p:], '\n') + p + 1
		if end == p {
			end = len(text)
		}
		line := text[p:end]
		if level, title, ok := heading(line); ok && fence == "" {
			// Close the sections at this level and below.
			for len(open) > 0 && out[open[len(open)-1]].Level >= level {
				out[open[len(open)-1]].End = p
				open = open[:len(open)-1]
			}
			stack = append(stack[:min(len(stack), level-1)], make([]string, max(0, level-1-len(stack)))...)
			stack = append(stack, title)
			out = append(out, Section{Path: joinPath(stack), Title: title, Level: level, Start: p})
			open = append(open, len(out)-1)
		} else {
			fence = nextFence(fence, line)
		}
		p = end
	}
	for _, i := range open {
		out[i].End = len(text)
	}
	return out
}

// Section returns the section of doc at path (as [Outline] gives it), and
// whether there is one. When two sections have the same path, the first
// wins.
func (b *Base) Section(source, path string) (Document, Section, bool) {
	d, ok := b.Document(source)
	if !ok {
		return Document{}, Section{}, false
	}
	for _, s := range Outline(d) {
		if s.Path == path {
			return d, s, true
		}
	}
	return d, Section{}, false
}
