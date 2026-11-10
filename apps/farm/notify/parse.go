package notify

import (
	"fmt"
	"slices"
	"strings"
	"unicode"
)

// DefaultRoutes is the farm app's tree: urgent alerts on every free lane
// and on WhatsApp, falling back to SMS; warnings on the free lanes; the
// rest by push. Lanes that are off are skipped, so it works with none of
// the paid lanes' keys.
const DefaultRoutes = "urgent=fanout(push,telegram,chain(whatsapp,sms)); warning=fanout(push,telegram); digest=fanout(push,email); default=push"

// Parse builds a [Router] from routes, using lanes by name. Routes are
// separated by ";", each "key=tree": the key is a severity (urgent,
// warning, advice), a kind (alert, digest, reply, test), both as
// "kind.severity", or "default"; a tree is a lane's name,
// fanout(tree, ...) or chain(tree, ...). A name that is not in lanes is an
// error; give the lanes that are not configured as [Off].
func Parse(routes string, lanes map[string]Sender) (Sender, error) {
	var rs []Route
	var fallback Sender
	seen := map[string]bool{}
	for part := range strings.SplitSeq(routes, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		key, tree, ok := strings.Cut(part, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" {
			return nil, fmt.Errorf("%w: route %q: want key=tree", ErrInvalid, part)
		}
		if seen[key] {
			return nil, fmt.Errorf("%w: route %q given twice", ErrInvalid, key)
		}
		seen[key] = true
		p := &parser{src: tree, lanes: lanes}
		s, err := p.tree()
		if err == nil {
			p.space()
			if p.pos < len(p.src) {
				err = p.errf("unexpected %q", p.src[p.pos:])
			}
		}
		if err != nil {
			return nil, fmt.Errorf("route %s: %w", key, err)
		}
		if key == "default" {
			fallback = s
			continue
		}
		kind, sev, err := splitKey(key)
		if err != nil {
			return nil, err
		}
		rs = append(rs, Route{Kind: kind, Severity: sev, Send: s})
	}
	if len(rs) == 0 && fallback == nil {
		return nil, fmt.Errorf("%w: no routes", ErrInvalid)
	}
	return Router(rs, fallback), nil
}

var (
	severities = []string{Urgent, Warning, Advice}
	kinds      = []string{KindAlert, KindDigest, KindReply, KindTest}
)

func splitKey(key string) (kind, sev string, err error) {
	if k, s, ok := strings.Cut(key, "."); ok {
		if !slices.Contains(kinds, k) || !slices.Contains(severities, s) {
			return "", "", fmt.Errorf("%w: route key %q: want kind.severity, such as alert.urgent", ErrInvalid, key)
		}
		return k, s, nil
	}
	switch {
	case slices.Contains(severities, key):
		return "", key, nil
	case slices.Contains(kinds, key):
		return key, "", nil
	}
	return "", "", fmt.Errorf("%w: route key %q: want default, a severity (%s), a kind (%s) or kind.severity",
		ErrInvalid, key, strings.Join(severities, ", "), strings.Join(kinds, ", "))
}

type parser struct {
	src   string
	pos   int
	lanes map[string]Sender
	depth int
}

func (p *parser) errf(format string, args ...any) error {
	return fmt.Errorf("%w: at %d in %q: %s", ErrInvalid, p.pos, p.src, fmt.Sprintf(format, args...))
}

func (p *parser) space() {
	for p.pos < len(p.src) && unicode.IsSpace(rune(p.src[p.pos])) {
		p.pos++
	}
}

func (p *parser) word() string {
	p.space()
	start := p.pos
	for p.pos < len(p.src) {
		c := p.src[p.pos]
		if !(c == '-' || c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') {
			break
		}
		p.pos++
	}
	return strings.ToLower(p.src[start:p.pos])
}

func (p *parser) tree() (Sender, error) {
	p.depth++
	defer func() { p.depth-- }()
	if p.depth > 8 {
		return nil, p.errf("nested too deep")
	}
	w := p.word()
	if w == "" {
		return nil, p.errf("want a lane, fanout(...) or chain(...)")
	}
	p.space()
	if p.pos < len(p.src) && p.src[p.pos] == '(' {
		if w != "fanout" && w != "chain" {
			return nil, p.errf("%q is not fanout or chain", w)
		}
		p.pos++
		var kids []Sender
		for {
			k, err := p.tree()
			if err != nil {
				return nil, err
			}
			kids = append(kids, k)
			p.space()
			if p.pos >= len(p.src) {
				return nil, p.errf("missing )")
			}
			c := p.src[p.pos]
			p.pos++
			if c == ')' {
				break
			}
			if c != ',' {
				return nil, p.errf("want , or )")
			}
		}
		if w == "fanout" {
			return FanOut(kids...), nil
		}
		return Chain(kids...), nil
	}
	s, ok := p.lanes[w]
	if !ok {
		names := make([]string, 0, len(p.lanes))
		for n := range p.lanes {
			names = append(names, n)
		}
		slices.Sort(names)
		return nil, p.errf("no lane %q: want one of %s", w, strings.Join(names, ", "))
	}
	return s, nil
}
