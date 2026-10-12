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
// [Classify].
const (
	KindRead    Kind = "read"    // reads only; changes nothing
	KindWrite   Kind = "write"   // creates or changes something
	KindDelete  Kind = "delete"  // removes something
	KindSend    Kind = "send"    // communicates outside: an email, a post, a message
	KindExecute Kind = "execute" // runs code or commands
)

// Kinds lists every kind, in order of increasing reach.
var Kinds = []Kind{KindRead, KindWrite, KindDelete, KindSend, KindExecute}

// Risk is how much harm a wrong call could do. A tool that declares none is
// classified as [RiskMedium] by [Classify].
type Risk string

// The risks.
const (
	RiskLow    Risk = "low"
	RiskMedium Risk = "medium"
	RiskHigh   Risk = "high"
)

// Risks lists every risk, lowest first.
var Risks = []Risk{RiskLow, RiskMedium, RiskHigh}

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
	// Kind is what the tool does. Empty means unclassified: see [Classify].
	Kind Kind `json:"kind,omitempty"`
	// Risk is how much harm a wrong call could do. Empty means unclassified.
	Risk Risk `json:"risk,omitempty"`
	// Interrupts marks a tool that needs a person to answer, such as
	// ask_user. It is not offered to runs that must never wait.
	Interrupts bool `json:"interrupts,omitempty"`
	// RequiresApproval asks that a person approve each call before it runs,
	// unless a policy says otherwise.
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

// Classify reports d's kind and risk, applying the default for an
// unclassified tool to each one it leaves empty: [KindWrite] and
// [RiskMedium]. So a read-only policy blocks a tool until someone classifies
// it. Policies read a tool's class through Classify, never from the fields.
func Classify(d Definition) (Kind, Risk) {
	k, r := d.Kind, d.Risk
	if k == "" {
		k = KindWrite
	}
	if r == "" {
		r = RiskMedium
	}
	return k, r
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
	if m.Kind != "" && !slices.Contains(Kinds, m.Kind) {
		return fmt.Errorf("kind %q must be one of %v", m.Kind, Kinds)
	}
	if m.Risk != "" && !slices.Contains(Risks, m.Risk) {
		return fmt.Errorf("risk %q must be one of %v", m.Risk, Risks)
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
// characters, no surrounding space, at most maxDisplayRunes runes.
func displayText(s string) error {
	switch {
	case !utf8.ValidString(s):
		return fmt.Errorf("%q is not valid UTF-8", s)
	case strings.IndexFunc(s, unicode.IsControl) >= 0:
		return fmt.Errorf("%q has a control character", s)
	case strings.TrimSpace(s) != s:
		return fmt.Errorf("%q has leading or trailing space", s)
	case utf8.RuneCountInString(s) > maxDisplayRunes:
		return fmt.Errorf("%q is longer than %d characters", s, maxDisplayRunes)
	}
	return nil
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
// MCP can express. The mapping:
//
//   - readOnlyHint: the kind is read.
//   - destructiveHint: the kind is delete or the risk is high; unstated for
//     a read tool (MCP ignores it there) and for an unclassified one.
//   - openWorldHint: tagged external; false for a classified tool that is
//     not, unstated for an unclassified one.
//   - idempotentHint: tagged idempotent.
//   - title: the display title.
//
// Interrupts, RequiresApproval, other tags, low risk, and the send and
// execute kinds have no MCP counterpart and are not published.
func (m Metadata) Annotations() *Annotations {
	classified := m.Kind != "" || m.Risk != ""
	a := Annotations{
		Title:          m.Display.Title,
		ReadOnlyHint:   m.Kind == KindRead,
		IdempotentHint: m.HasTag(TagIdempotent),
	}
	if classified && m.Kind != KindRead {
		a.DestructiveHint = ptr(m.Kind == KindDelete || m.Risk == RiskHigh)
	}
	if m.HasTag(TagExternal) || classified {
		a.OpenWorldHint = ptr(m.HasTag(TagExternal))
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
//     Unstated: unclassified, which [Classify] reports as write/medium.
//   - openWorldHint true: tag external; idempotentHint true: tag idempotent.
//   - title: the display title.
//
// A nil a gives the zero Metadata: an unclassified tool. Hints from a server
// you do not trust are only claims; policies can still narrow what they allow.
func FromAnnotations(a *Annotations) Metadata {
	var m Metadata
	if a == nil {
		return m
	}
	switch {
	case a.ReadOnlyHint:
		m.Kind = KindRead
	case a.DestructiveHint != nil && *a.DestructiveHint:
		m.Kind, m.Risk = KindWrite, RiskHigh
	case a.DestructiveHint != nil:
		m.Kind = KindWrite
	}
	if a.OpenWorldHint != nil && *a.OpenWorldHint {
		m.Tags = append(m.Tags, TagExternal)
	}
	if a.IdempotentHint {
		m.Tags = append(m.Tags, TagIdempotent)
	}
	m.Display.Title = a.Title
	return m
}

func ptr[T any](v T) *T { return &v }
