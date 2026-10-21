package skill

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// frontMatter is what SKILL.md's front matter says. Keys other than these
// (license, allowed-tools, compatibility...) are parsed and ignored.
type frontMatter struct {
	name, description string
	metadata          map[string]string
	tools             []string
	// Problems with metadata and tools, reported under their own rule.
	metadataErr, toolsErr error
}

// parseSkillMD splits SKILL.md into its front matter and its body.
//
// The front matter is YAML, of which this reads the subset skills use, and
// refuses the rest with an error naming the line (no new dependency for a
// few keys): top-level "key: value" pairs whose value is a plain, single- or
// double-quoted scalar, a plain scalar continued on more-indented lines, a
// literal (|) or folded (>) block scalar, a list (flow [a, b] or block
// "- a" items) or a map of scalars one level deep; and comments.
func parseSkillMD(src string) (frontMatter, string, error) {
	src = strings.TrimPrefix(src, "\uFEFF")
	src = strings.ReplaceAll(src, "\r\n", "\n")
	if !strings.HasPrefix(src, "---\n") {
		return frontMatter{}, "", errors.New(`SKILL.md must start with front matter: a line "---", YAML, then a line "---"`)
	}
	lines := strings.Split(src[len("---\n"):], "\n")
	end := -1
	for i, l := range lines {
		if strings.TrimRight(l, " \t") == "---" {
			end = i
			break
		}
	}
	if end < 0 {
		return frontMatter{}, "", errors.New(`the front matter has no closing "---" line`)
	}
	values, err := parseYAML(lines[:end])
	if err != nil {
		return frontMatter{}, "", err
	}
	body := strings.TrimLeft(strings.Join(lines[end+1:], "\n"), "\n")

	var fm frontMatter
	for key, v := range values {
		switch key {
		case "name":
			if fm.name, err = v.scalar(key); err != nil {
				return frontMatter{}, "", err
			}
		case "description":
			if fm.description, err = v.scalar(key); err != nil {
				return frontMatter{}, "", err
			}
		case "metadata":
			switch {
			case v.isNull():
			case v.mapping == nil:
				fm.metadataErr = fmt.Errorf("line %d: metadata must be a map of strings", v.line)
			default:
				fm.metadata = v.mapping
			}
		case "tools":
			fm.tools, fm.toolsErr = parseTools(v)
		}
	}
	return fm, body, nil
}

// toolGlob is what a tools entry may hold: a tool name, with glob characters.
var toolGlob = regexp.MustCompile(`^[a-zA-Z0-9_*?\[\]^!-]{1,128}$`)

// parseTools reads the tools key: a list, or one string of names separated
// by spaces or commas.
func parseTools(v value) ([]string, error) {
	var names []string
	switch {
	case v.isNull():
		return nil, nil
	case v.list != nil:
		names = v.list
	case v.mapping == nil:
		names = strings.FieldsFunc(v.str, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' || r == '\n' })
	default:
		return nil, fmt.Errorf("line %d: tools must be a list of tool names or globs", v.line)
	}
	for _, n := range names {
		if _, err := path.Match(n, ""); err != nil || !toolGlob.MatchString(n) {
			return nil, fmt.Errorf("line %d: tools: %q is not a tool name or glob", v.line, n)
		}
	}
	return names, nil
}

// value is one top-level value: exactly one of str, list and mapping is set,
// or none for an empty value.
type value struct {
	line    int // 1-based line in SKILL.md
	str     string
	isStr   bool
	list    []string
	mapping map[string]string
}

func (v value) isNull() bool { return !v.isStr && v.list == nil && v.mapping == nil }

func (v value) scalar(key string) (string, error) {
	if v.list != nil || v.mapping != nil {
		return "", fmt.Errorf("line %d: %s must be a string", v.line, key)
	}
	return v.str, nil
}

var keyPattern = regexp.MustCompile(`^([A-Za-z0-9_][A-Za-z0-9_.-]*)[ \t]*:(?:[ \t]+(.*)|[ \t]*)$`)

// parseYAML parses the front matter's lines (line 1 of the slice is line 2
// of SKILL.md).
func parseYAML(lines []string) (map[string]value, error) {
	out := map[string]value{}
	lineNo := func(i int) int { return i + 2 }
	for i := 0; i < len(lines); {
		l := lines[i]
		if isBlankOrComment(l) {
			i++
			continue
		}
		if l[0] == ' ' || l[0] == '\t' {
			return nil, fmt.Errorf("line %d: unexpected indentation", lineNo(i))
		}
		m := keyPattern.FindStringSubmatch(l)
		if m == nil {
			return nil, fmt.Errorf("line %d: want \"key: value\" (this reader takes a subset of YAML)", lineNo(i))
		}
		key, rest := m[1], strings.TrimSpace(m[2])
		if _, dup := out[key]; dup {
			return nil, fmt.Errorf("line %d: key %q appears twice", lineNo(i), key)
		}
		// The more-indented lines that follow belong to this key.
		j := i + 1
		for j < len(lines) && (strings.TrimSpace(lines[j]) == "" || lines[j][0] == ' ' || lines[j][0] == '\t') {
			j++
		}
		block := lines[i+1 : j]
		v, err := parseValue(rest, block, lineNo(i))
		if err != nil {
			return nil, err
		}
		out[key] = v
		i = j
	}
	return out, nil
}

func isBlankOrComment(l string) bool {
	t := strings.TrimSpace(l)
	return t == "" || strings.HasPrefix(t, "#")
}

// parseValue reads a key's value from the rest of its line and the
// more-indented lines under it.
func parseValue(rest string, block []string, line int) (value, error) {
	v := value{line: line}
	switch {
	case rest == "" || strings.HasPrefix(rest, "#"):
		return parseNested(block, line)
	case rest[0] == '|' || rest[0] == '>':
		s, err := blockScalar(rest, block, line)
		v.str, v.isStr = s, true
		return v, err
	case rest[0] == '[':
		if hasContent(block) {
			return v, fmt.Errorf("line %d: a flow list must fit on one line", line)
		}
		list, err := flowList(rest, line)
		v.list = list
		if v.list == nil && err == nil {
			v.list = []string{}
		}
		return v, err
	case rest[0] == '{':
		return v, fmt.Errorf("line %d: flow maps ({...}) are not supported; write one \"key: value\" per line", line)
	}
	s, tail, err := scalar(rest, line)
	if err != nil {
		return v, err
	}
	if tail != "" {
		return v, fmt.Errorf("line %d: unexpected %q after the value", line, tail)
	}
	if hasContent(block) {
		if rest[0] == '"' || rest[0] == '\'' {
			return v, fmt.Errorf("line %d: a quoted string must fit on one line here; use a block scalar (| or >) for long text", line)
		}
		// A plain scalar continued on more-indented lines: folded.
		s = foldPlain(append([]string{s}, block...))
	}
	v.str, v.isStr = s, true
	return v, nil
}

func hasContent(block []string) bool {
	for _, l := range block {
		if !isBlankOrComment(l) {
			return true
		}
	}
	return false
}

// foldPlain joins a multi-line plain scalar: lines with a space between,
// blank lines as line breaks.
func foldPlain(lines []string) string {
	var b strings.Builder
	pendingBreaks := 0
	first := true
	for _, l := range lines {
		t := strings.TrimSpace(l)
		if t == "" {
			pendingBreaks++
			continue
		}
		if c := strings.Index(t, " #"); c >= 0 {
			t = strings.TrimSpace(t[:c])
		}
		switch {
		case first:
		case pendingBreaks > 0:
			b.WriteString(strings.Repeat("\n", pendingBreaks))
		default:
			b.WriteByte(' ')
		}
		b.WriteString(t)
		first, pendingBreaks = false, 0
	}
	return b.String()
}

// parseNested reads a value that starts on the next line: a block list, a
// map of scalars, or nothing.
func parseNested(block []string, line int) (value, error) {
	v := value{line: line}
	var items []struct {
		text string
		line int
	}
	for k, l := range block {
		if isBlankOrComment(l) {
			continue
		}
		items = append(items, struct {
			text string
			line int
		}{l, line + 1 + k})
	}
	if len(items) == 0 {
		return v, nil
	}
	indent := len(items[0].text) - len(strings.TrimLeft(items[0].text, " \t"))
	isList := strings.HasPrefix(strings.TrimSpace(items[0].text), "-")
	if isList {
		v.list = []string{}
	} else {
		v.mapping = map[string]string{}
	}
	for _, it := range items {
		t := strings.TrimSpace(it.text)
		if len(it.text)-len(strings.TrimLeft(it.text, " \t")) != indent {
			return v, fmt.Errorf("line %d: nesting deeper than one level is not supported", it.line)
		}
		if isList {
			if t != "-" && !strings.HasPrefix(t, "- ") {
				return v, fmt.Errorf("line %d: want a list item \"- value\"", it.line)
			}
			s, tail, err := scalar(strings.TrimSpace(strings.TrimPrefix(t, "-")), it.line)
			if err != nil {
				return v, err
			}
			if tail != "" {
				return v, fmt.Errorf("line %d: unexpected %q after the value", it.line, tail)
			}
			v.list = append(v.list, s)
			continue
		}
		m := keyPattern.FindStringSubmatch(t)
		if m == nil {
			return v, fmt.Errorf("line %d: want \"key: value\"", it.line)
		}
		rest := strings.TrimSpace(m[2])
		if rest != "" && strings.ContainsRune("|>[{", rune(rest[0])) {
			return v, fmt.Errorf("line %d: a nested value must be a string on one line", it.line)
		}
		s, tail, err := scalar(rest, it.line)
		if err != nil {
			return v, err
		}
		if tail != "" {
			return v, fmt.Errorf("line %d: unexpected %q after the value", it.line, tail)
		}
		if _, dup := v.mapping[m[1]]; dup {
			return v, fmt.Errorf("line %d: key %q appears twice", it.line, m[1])
		}
		v.mapping[m[1]] = s
	}
	return v, nil
}

// scalar reads one scalar at the start of s (quoted or plain) and returns
// what follows it, with a trailing comment dropped.
func scalar(s string, line int) (string, string, error) {
	if s == "" || strings.HasPrefix(s, "#") {
		return "", "", nil
	}
	switch s[0] {
	case '"':
		return doubleQuoted(s, line)
	case '\'':
		var b strings.Builder
		for i := 1; i < len(s); i++ {
			if s[i] != '\'' {
				b.WriteByte(s[i])
				continue
			}
			if i+1 < len(s) && s[i+1] == '\'' {
				b.WriteByte('\'')
				i++
				continue
			}
			return b.String(), trimComment(s[i+1:]), nil
		}
		return "", "", fmt.Errorf("line %d: unterminated single-quoted string", line)
	case '&', '*', '!', '%', '@', '`':
		return "", "", fmt.Errorf("line %d: %q starts a YAML feature this reader does not support (anchors, aliases, tags); quote the value", line, s[0])
	}
	plain := s
	if c := strings.Index(plain, " #"); c >= 0 {
		plain = plain[:c]
	}
	return strings.TrimSpace(plain), "", nil
}

func trimComment(tail string) string {
	t := strings.TrimSpace(tail)
	if strings.HasPrefix(t, "#") {
		return ""
	}
	return t
}

// doubleQuoted reads a double-quoted scalar with YAML's common escapes.
func doubleQuoted(s string, line int) (string, string, error) {
	var b strings.Builder
	for i := 1; i < len(s); i++ {
		c := s[i]
		switch c {
		case '"':
			return b.String(), trimComment(s[i+1:]), nil
		case '\\':
			if i+1 >= len(s) {
				return "", "", fmt.Errorf("line %d: unterminated double-quoted string", line)
			}
			i++
			switch e := s[i]; e {
			case '0':
				b.WriteByte(0)
			case 'a':
				b.WriteByte('\a')
			case 'b':
				b.WriteByte('\b')
			case 't', '\t':
				b.WriteByte('\t')
			case 'n':
				b.WriteByte('\n')
			case 'v':
				b.WriteByte('\v')
			case 'f':
				b.WriteByte('\f')
			case 'r':
				b.WriteByte('\r')
			case 'e':
				b.WriteByte(0x1b)
			case ' ', '"', '/', '\\':
				b.WriteByte(e)
			case 'N':
				b.WriteString("\u0085")
			case '_':
				b.WriteString(" ")
			case 'L':
				b.WriteString(" ")
			case 'P':
				b.WriteString(" ")
			case 'x', 'u', 'U':
				n := map[byte]int{'x': 2, 'u': 4, 'U': 8}[e]
				if i+n >= len(s) {
					return "", "", fmt.Errorf("line %d: short \\%c escape", line, e)
				}
				r, err := strconv.ParseUint(s[i+1:i+1+n], 16, 32)
				if err != nil || !utf8.ValidRune(rune(r)) {
					return "", "", fmt.Errorf("line %d: bad \\%c escape", line, e)
				}
				b.WriteRune(rune(r))
				i += n
			default:
				return "", "", fmt.Errorf("line %d: unknown escape \\%c", line, e)
			}
		default:
			b.WriteByte(c)
		}
	}
	return "", "", fmt.Errorf("line %d: unterminated double-quoted string", line)
}

// flowList reads a one-line flow list of scalars: [a, "b", 'c'].
func flowList(s string, line int) ([]string, error) {
	var out []string
	rest := strings.TrimSpace(s[1:])
	for {
		if strings.HasPrefix(rest, "]") {
			if trimComment(rest[1:]) != "" {
				return nil, fmt.Errorf("line %d: unexpected text after ]", line)
			}
			return out, nil
		}
		if rest == "" {
			return nil, fmt.Errorf("line %d: unterminated flow list", line)
		}
		var item string
		switch rest[0] {
		case '"', '\'':
			v, tail, err := scalar(rest, line)
			if err != nil {
				return nil, err
			}
			item, rest = v, tail
		case '[', '{':
			return nil, fmt.Errorf("line %d: nested flow collections are not supported", line)
		default:
			end := strings.IndexAny(rest, ",]")
			if end < 0 {
				return nil, fmt.Errorf("line %d: unterminated flow list", line)
			}
			item, rest = strings.TrimSpace(rest[:end]), rest[end:]
		}
		out = append(out, item)
		rest = strings.TrimSpace(rest)
		if strings.HasPrefix(rest, ",") {
			rest = strings.TrimSpace(rest[1:])
		}
	}
}

// blockScalar reads a literal (|) or folded (>) block scalar with an
// optional chomping indicator (- or +).
func blockScalar(header string, block []string, line int) (string, error) {
	h := header
	if c := strings.Index(h, " #"); c >= 0 {
		h = h[:c]
	}
	h = strings.TrimSpace(h)
	folded := h[0] == '>'
	chomp := h[1:]
	if chomp != "" && chomp != "-" && chomp != "+" {
		return "", fmt.Errorf("line %d: block scalar header %q: only |, >, |-, >-, |+ and >+ are supported", line, h)
	}
	// Trailing blank lines belong to the chomping, not the content.
	last := len(block)
	for last > 0 && strings.TrimSpace(block[last-1]) == "" {
		last--
	}
	trailing := len(block) - last
	content := block[:last]
	indent := -1
	for _, l := range content {
		if strings.TrimSpace(l) != "" {
			indent = len(l) - len(strings.TrimLeft(l, " "))
			break
		}
	}
	if indent <= 0 {
		return "", nil
	}
	var lines []string
	for k, l := range content {
		if strings.TrimSpace(l) == "" {
			lines = append(lines, "")
			continue
		}
		if len(l)-len(strings.TrimLeft(l, " ")) < indent {
			return "", fmt.Errorf("line %d: block scalar line is less indented than its first line", line+1+k)
		}
		lines = append(lines, l[indent:])
	}
	var text string
	if folded {
		var b strings.Builder
		for k, l := range lines {
			if k > 0 {
				prev := lines[k-1]
				switch {
				case l == "" || prev == "":
					b.WriteByte('\n')
				case strings.HasPrefix(l, " ") || strings.HasPrefix(prev, " "):
					b.WriteByte('\n') // more-indented lines keep their breaks
				default:
					b.WriteByte(' ')
				}
			}
			b.WriteString(l)
		}
		text = b.String()
		// Each blank line between paragraphs stands for one break, and the
		// one before it was folded into it above.
		text = strings.ReplaceAll(text, "\n\n", "\n")
	} else {
		text = strings.Join(lines, "\n")
	}
	switch chomp {
	case "-":
		return text, nil
	case "+":
		return text + strings.Repeat("\n", 1+trailing), nil
	}
	return text + "\n", nil
}
