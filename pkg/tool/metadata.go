package tool

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Kind is what a tool does to the world. Policies (allow, flag, approve or
// deny) match on it.
type Kind string

// The kinds. A tool that declares none is classified as [KindWrite] by
// [Metadata.Class].
const (
	KindRead    Kind = "read"    // reads only; changes nothing
	KindWrite   Kind = "write"   // creates or changes something
	KindDelete  Kind = "delete"  // removes something
	KindSend    Kind = "send"    // communicates outside: an email, a post, a message
	KindExecute Kind = "execute" // runs code or commands
)

var kinds = []Kind{KindRead, KindWrite, KindDelete, KindSend, KindExecute}

// Kinds returns every kind. They are not ordered: a send does not reach
// further than a delete, it reaches somewhere else.
func Kinds() []Kind { return slices.Clone(kinds) }

// Risk is how much harm a wrong call could do. A tool that declares none is
// classified as [RiskMedium] by [Metadata.Class].
type Risk string

// The risks.
const (
	RiskLow    Risk = "low"
	RiskMedium Risk = "medium"
	RiskHigh   Risk = "high"
)

var risks = []Risk{RiskLow, RiskMedium, RiskHigh}

// Risks returns every risk, lowest first.
func Risks() []Risk { return slices.Clone(risks) }

// Tags with a meaning shared with MCP's tool annotations (see [Annotations]).
const (
	// TagExternal marks a tool that reaches an open world of external
	// entities, such as the web (MCP's openWorldHint).
	TagExternal = "external"
	// TagIdempotent marks a tool whose repeated calls with the same input
	// have no further effect (MCP's idempotentHint).
	TagIdempotent = "idempotent"
)

// Metadata says what a tool does, for policies, interrupts and display. Every
// field is optional; set it on a [Func] tool with [WithMetadata]. It is
// embedded in [Definition], so its fields read as the definition's own.
type Metadata struct {
	// Kind is what the tool does. Empty means unclassified: see [Metadata.Class].
	Kind Kind `json:"kind,omitempty"`
	// Risk is how much harm a wrong call could do. Empty means unclassified.
	Risk Risk `json:"risk,omitempty"`
	// Interrupts marks a tool that needs a person to answer, such as
	// ask_user, so that runs that must never wait can leave it out. Nothing
	// acts on it yet: the interaction modes (T084) will.
	Interrupts bool `json:"interrupts,omitempty"`
	// RequiresApproval asks that a person approve each call before it runs,
	// unless a policy says otherwise. Nothing enforces it yet: the policy
	// engine (T083) and approval holds (T084) will.
	RequiresApproval bool `json:"requiresApproval,omitempty"`
	// Tags are free-form labels policies can match, such as "external",
	// "billing" or "pii". Each matches ^[a-z0-9][a-z0-9_.-]{0,63}$; no
	// duplicates.
	Tags []string `json:"tags,omitempty"`
	// Display is how a person sees the tool's calls.
	Display Display `json:"display,omitzero"`
}

// Display is how a console shows a tool and its calls (docs/UI.md, "Tool
// calls"): one line with an icon and a verb phrase, present tense while the
// call runs and past tense once it is done.
type Display struct {
	// Title is the tool's human-readable name ("Web search"). It is
	// published as MCP's annotations.title.
	Title string `json:"title,omitempty"`
	// Icon names an icon of the console's set ("search", "page", "calc").
	// An unknown name draws nothing. It matches ^[a-z][a-z0-9-]{0,31}$.
	Icon string `json:"icon,omitempty"`
	// Present is the verb phrase while the call runs ("Searching the web").
	Present string `json:"present,omitempty"`
	// Past is the verb phrase once the call is done ("Searched the web").
	// Present and Past are set together or not at all.
	Past string `json:"past,omitempty"`
}

// Class reports the tool's kind and risk as policies see them. Each one that
// is empty, or not one of [Kinds] or [Risks], is replaced by the default for
// an unclassified tool: [KindWrite] and [RiskMedium]. So a read-only policy
// blocks a tool until someone classifies it, and a definition that skipped
// validation cannot slip past a rule with a kind no rule names. Policies read
// a tool's class through Class, never from the fields.
func (m Metadata) Class() (Kind, Risk) {
	k, r := m.Kind, m.Risk
	if !slices.Contains(kinds, k) {
		k = KindWrite
	}
	if !slices.Contains(risks, r) {
		r = RiskMedium
	}
	return k, r
}

// Classified reports whether [Metadata.Class] used no default: the tool
// declares both a valid kind and a valid risk.
func (m Metadata) Classified() bool {
	return slices.Contains(kinds, m.Kind) && slices.Contains(risks, m.Risk)
}

// HasTag reports whether m carries tag.
func (m Metadata) HasTag(tag string) bool { return slices.Contains(m.Tags, tag) }

const maxDisplayRunes = 80

var (
	tagPattern  = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,63}$`)
	iconPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
)

// checkMetadata checks m's fields against their documented rules.
func checkMetadata(m Metadata) error {
	if m.Kind != "" && !slices.Contains(kinds, m.Kind) {
		return fmt.Errorf("kind %q must be one of %v", m.Kind, kinds)
	}
	if m.Risk != "" && !slices.Contains(risks, m.Risk) {
		return fmt.Errorf("risk %q must be one of %v", m.Risk, risks)
	}
	for i, tag := range m.Tags {
		if !tagPattern.MatchString(tag) {
			return fmt.Errorf("tag %q must match %s", tag, tagPattern)
		}
		if slices.Contains(m.Tags[:i], tag) {
			return fmt.Errorf("tag %q appears twice", tag)
		}
	}
	d := m.Display
	if d.Icon != "" && !iconPattern.MatchString(d.Icon) {
		return fmt.Errorf("display icon %q must match %s", d.Icon, iconPattern)
	}
	if (d.Present == "") != (d.Past == "") {
		return fmt.Errorf("display present %q and past %q must be set together", d.Present, d.Past)
	}
	for _, f := range []struct{ name, text string }{{"title", d.Title}, {"present", d.Present}, {"past", d.Past}} {
		if err := displayText(f.text); err != nil {
			return fmt.Errorf("display %s: %w", f.name, err)
		}
	}
	return nil
}

// displayText checks one line of display text: valid UTF-8, no control
// characters, line or paragraph separators or bidirectional controls (which
// can make a line read as something else), no surrounding space, at most
// maxDisplayRunes runes.
func displayText(s string) error {
	switch {
	case !utf8.ValidString(s):
		return fmt.Errorf("%q is not valid UTF-8", s)
	case strings.IndexFunc(s, notOnALine) >= 0:
		return fmt.Errorf("%q has a control character", s)
	case strings.TrimSpace(s) != s:
		return fmt.Errorf("%q has leading or trailing space", s)
	case utf8.RuneCountInString(s) > maxDisplayRunes:
		return fmt.Errorf("%q is longer than %d characters", s, maxDisplayRunes)
	}
	return nil
}

func notOnALine(r rune) bool {
	return unicode.IsControl(r) || unicode.In(r, unicode.Zl, unicode.Zp, unicode.Bidi_Control)
}

// Annotations are MCP's tool annotations
// (https://modelcontextprotocol.io/specification/latest/server/tools), the
// hints an MCP server publishes about a tool. A nil pointer hint is unstated.
// pkg/mcp/server publishes them; they mirror the SDK's type so this package
// does not depend on the SDK.
type Annotations struct {
	Title           string
	ReadOnlyHint    bool
	DestructiveHint *bool
	IdempotentHint  bool
	OpenWorldHint   *bool
}

// Annotations maps m to MCP's tool annotations, or nil when m says nothing
// MCP can express. A hint is stated only when m says enough to make it true;
// otherwise it is left unstated, and MCP's defaults (destructive, open world)
// apply, which are the cautious reading. The mapping:
//
//   - readOnlyHint: the kind is read.
//   - destructiveHint: true when the kind is delete or the risk is high.
//     Never false: MCP's false means "only additive updates", and no kind
//     says that (a write may overwrite, an execute may do anything). Unstated
//     for a read tool, where MCP ignores it.
//   - openWorldHint: true when tagged external or the kind is send (which
//     reaches outside by definition); false for any other tool that declares
//     its kind; unstated for one that does not, since then we do not know.
//   - idempotentHint: tagged idempotent.
//   - title: the display title.
//
// Interrupts, RequiresApproval, other tags, low and medium risk, and the
// write, send and execute kinds have no MCP counterpart and are not
// published.
func (m Metadata) Annotations() *Annotations {
	a := Annotations{
		Title:          m.Display.Title,
		ReadOnlyHint:   m.Kind == KindRead,
		IdempotentHint: m.HasTag(TagIdempotent),
	}
	if m.Kind == KindDelete || (m.Risk == RiskHigh && m.Kind != KindRead) {
		a.DestructiveHint = ptr(true)
	}
	if open := m.HasTag(TagExternal) || m.Kind == KindSend; open || m.Kind != "" {
		a.OpenWorldHint = ptr(open)
	}
	if a == (Annotations{}) {
		return nil
	}
	return &a
}

// FromAnnotations classifies a tool from its MCP annotations, the inverse of
// [Metadata.Annotations] where both sides have the concept:
//
//   - readOnlyHint true: kind read.
//   - otherwise destructiveHint true: kind write, risk high (MCP's
//     "destructive" covers deletes and overwrites alike, so the kind stays
//     the general one and the risk carries the warning); false: kind write.
//     Unstated: unclassified, which [Metadata.Class] reports as write/medium.
//   - openWorldHint true or unstated (MCP's default is an open world): tag
//     external; false: no tag.
//   - idempotentHint true: tag idempotent.
//   - title: the display title, trimmed, and dropped if it is still not one
//     line of at most 80 characters.
//
// The result always passes [Func]'s and [NewRegistry]'s checks. A nil a is
// a tool with no annotations: unclassified, and tagged external. Hints from
// a server you do not trust are only claims; policies can still narrow what
// they allow.
func FromAnnotations(a *Annotations) Metadata {
	if a == nil {
		a = &Annotations{}
	}
	var m Metadata
	switch {
	case a.ReadOnlyHint:
		m.Kind = KindRead
	case a.DestructiveHint != nil && *a.DestructiveHint:
		m.Kind, m.Risk = KindWrite, RiskHigh
	case a.DestructiveHint != nil:
		m.Kind = KindWrite
	}
	if a.OpenWorldHint == nil || *a.OpenWorldHint {
		m.Tags = append(m.Tags, TagExternal)
	}
	if a.IdempotentHint {
		m.Tags = append(m.Tags, TagIdempotent)
	}
	if title := strings.TrimSpace(a.Title); displayText(title) == nil {
		m.Display.Title = title
	}
	return m
}

func ptr[T any](v T) *T { return &v }
