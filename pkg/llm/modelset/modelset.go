// Package modelset decides which models an agent may run (T125, E24): its
// default model, the names it allows exactly, the patterns that allow more,
// and the variants that change the agent's settings for some models.
//
// A model is named "<provider>/<id>": "anthropic/claude-opus-5-5",
// "openai/gpt-5-mini". A name without a provider is the default
// provider's ([DefaultProvider]). The package knows no provider: whoever
// builds the models checks the provider names ([New]'s known), and a
// pattern may allow ids no list knows.
//
// The package is pure: no I/O, no model code, safe for concurrent use once
// built.
package modelset

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode"
)

// DefaultProvider is the provider of a name that gives none.
const DefaultProvider = "anthropic"

// Limits of a [Spec]. A spec past them is refused.
const (
	MaxAllow       = 64
	MaxPatterns    = 16
	MaxPatternLen  = 256
	MaxVariants    = 32
	MaxNameLen     = 160
	maxProviderLen = 32
)

// PatternPrefix marks a variant's Match as a pattern rather than a name.
const PatternPrefix = "re:"

// ErrNotAllowed means a model is not one the set allows.
var ErrNotAllowed = errors.New("model not allowed")

// ErrInvalidName means a string is not a model name.
var ErrInvalidName = errors.New("invalid model name")

// Spec is what an agent's definition says about its models, beside its
// default: the "models" section of the config file and of a stored agent.
type Spec struct {
	// Allow names models the agent may run, exactly.
	Allow []string `json:"allow,omitempty"`
	// AllowPatterns allow every name a pattern matches whole (RE2 syntax,
	// anchored at both ends: "anthropic/claude-opus-.*").
	AllowPatterns []string `json:"allow_patterns,omitempty"`
	// Variants change the agent's settings for some models; the first that
	// matches a model applies.
	Variants []Variant `json:"variants,omitempty"`
}

// Empty reports whether s allows nothing beyond the default and changes
// nothing.
func (s Spec) Empty() bool {
	return len(s.Allow) == 0 && len(s.AllowPatterns) == 0 && len(s.Variants) == 0
}

// Variant is how the agent differs on the models Match names.
type Variant struct {
	// Match is a model name, or a pattern after [PatternPrefix]
	// ("re:gemini/.*").
	Match string `json:"match"`
	// System replaces the agent's system prompt; SystemAppend is added to
	// it after a blank line. At most one of them is set.
	System       string `json:"system,omitempty"`
	SystemAppend string `json:"system_append,omitempty"`
	// ToolDescriptions override tool descriptions by tool name, over the
	// agent's own overrides (T126).
	ToolDescriptions map[string]string `json:"tool_descriptions,omitempty"`
	// Effort and MaxTokens replace the agent's model settings when set.
	Effort    string `json:"effort,omitempty"`
	MaxTokens int    `json:"max_tokens,omitempty"`
}

// FieldError is one problem of a spec, at a field relative to the spec
// ("allow[1]", "variants[0].match").
type FieldError struct {
	Field   string
	Message string
}

func (e FieldError) Error() string { return e.Field + ": " + e.Message }

// Errors are the problems [New] found, in the order of the fields.
type Errors []FieldError

func (e Errors) Error() string {
	msgs := make([]string, len(e))
	for i, f := range e {
		msgs[i] = f.Error()
	}
	return strings.Join(msgs, "; ")
}

// Set is the models of one agent. The zero value is not usable; build one
// with [New].
type Set struct {
	def      string
	spec     Spec
	allow    []string
	patterns []*regexp.Regexp
	variants []variant
}

type variant struct {
	Variant
	name string // the canonical name, when Match is not a pattern
	re   *regexp.Regexp
}

// Parse splits a model name into its provider and id. A name without "/"
// is [DefaultProvider]'s. The provider is lowercase letters, digits, "-"
// and "_"; the id is printable, without spaces, and the whole name at most
// [MaxNameLen] bytes.
func Parse(name string) (provider, id string, err error) {
	provider, id, ok := strings.Cut(name, "/")
	if !ok {
		provider, id = DefaultProvider, name
	}
	switch {
	case len(provider)+1+len(id) > MaxNameLen:
		return "", "", fmt.Errorf("%w %q: longer than %d bytes", ErrInvalidName, name, MaxNameLen)
	case !validProvider(provider):
		return "", "", fmt.Errorf("%w %q: the provider is lowercase letters, digits, - and _", ErrInvalidName, name)
	case id == "":
		return "", "", fmt.Errorf("%w %q: no model id", ErrInvalidName, name)
	case strings.IndexFunc(id, func(r rune) bool { return unicode.IsSpace(r) || !unicode.IsPrint(r) }) >= 0:
		return "", "", fmt.Errorf("%w %q: the id has a space or a control character", ErrInvalidName, name)
	}
	return provider, id, nil
}

func validProvider(p string) bool {
	if p == "" || len(p) > maxProviderLen {
		return false
	}
	for _, r := range p {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' && r != '_' {
			return false
		}
	}
	return true
}

// Canonical returns name as "<provider>/<id>".
func Canonical(name string) (string, error) {
	p, id, err := Parse(name)
	if err != nil {
		return "", err
	}
	return p + "/" + id, nil
}

// Provider returns the provider of a name, or "" for one that is not a
// name.
func Provider(name string) string {
	p, _, err := Parse(name)
	if err != nil {
		return ""
	}
	return p
}

// New builds the set of an agent whose default model is def (a name). known
// reports whether a provider is one the caller can build (nil: any). A spec
// that is not valid is [Errors]; the default model is always allowed, and
// listing it in Allow too is fine.
func New(def string, s Spec, known func(provider string) bool) (*Set, error) {
	var errs Errors
	bad := func(field, format string, args ...any) {
		errs = append(errs, FieldError{Field: field, Message: fmt.Sprintf(format, args...)})
	}
	name := func(field, n string) (string, bool) {
		c, err := Canonical(n)
		if err != nil {
			bad(field, "%v", err)
			return "", false
		}
		if p := Provider(c); known != nil && !known(p) {
			bad(field, "provider %q is not one this server builds", p)
			return "", false
		}
		return c, true
	}
	set := &Set{spec: cloneSpec(s)}
	if c, ok := name("default", def); ok {
		set.def = c
		set.allow = append(set.allow, c)
	}
	if len(s.Allow) > MaxAllow {
		bad("allow", "at most %d models", MaxAllow)
	}
	for i, n := range s.Allow {
		if c, ok := name(fmt.Sprintf("allow[%d]", i), n); ok && !slices.Contains(set.allow, c) {
			set.allow = append(set.allow, c)
		}
	}
	if len(s.AllowPatterns) > MaxPatterns {
		bad("allow_patterns", "at most %d patterns", MaxPatterns)
	}
	for i, p := range s.AllowPatterns {
		if re, err := compile(p); err != nil {
			bad(fmt.Sprintf("allow_patterns[%d]", i), "%v", err)
		} else {
			set.patterns = append(set.patterns, re)
		}
	}
	if len(s.Variants) > MaxVariants {
		bad("variants", "at most %d variants", MaxVariants)
	}
	for i, v := range s.Variants {
		field := fmt.Sprintf("variants[%d]", i)
		sv := variant{Variant: v}
		switch {
		case strings.HasPrefix(v.Match, PatternPrefix):
			re, err := compile(strings.TrimPrefix(v.Match, PatternPrefix))
			if err != nil {
				bad(field+".match", "%v", err)
				continue
			}
			sv.re = re
		case v.Match == "":
			bad(field+".match", "name a model, or a pattern after %q", PatternPrefix)
			continue
		default:
			c, ok := name(field+".match", v.Match)
			if !ok {
				continue
			}
			sv.name = c
		}
		if v.System != "" && v.SystemAppend != "" {
			bad(field+".system_append", "set system (to replace the prompt) or system_append (to add to it), not both")
		}
		if v.MaxTokens < 0 {
			bad(field+".max_tokens", "negative")
		}
		set.variants = append(set.variants, sv)
	}
	if len(errs) > 0 {
		return nil, errs
	}
	for i, v := range set.variants {
		if v.name != "" && !set.Allows(v.name) {
			errs = append(errs, FieldError{Field: fmt.Sprintf("variants[%d].match", i), Message: fmt.Sprintf("%q is not a model the agent allows", v.name)})
		}
	}
	if len(errs) > 0 {
		return nil, errs
	}
	return set, nil
}

// compile compiles a pattern anchored at both ends.
func compile(p string) (*regexp.Regexp, error) {
	switch {
	case p == "":
		return nil, errors.New("empty pattern")
	case len(p) > MaxPatternLen:
		return nil, fmt.Errorf("longer than %d bytes", MaxPatternLen)
	}
	re, err := regexp.Compile(`^(?:` + p + `)$`)
	if err != nil {
		return nil, fmt.Errorf("not a pattern: %w", err)
	}
	return re, nil
}

func cloneSpec(s Spec) Spec {
	s.Allow = slices.Clone(s.Allow)
	s.AllowPatterns = slices.Clone(s.AllowPatterns)
	s.Variants = slices.Clone(s.Variants)
	for i, v := range s.Variants {
		if v.ToolDescriptions != nil {
			m := make(map[string]string, len(v.ToolDescriptions))
			for k, d := range v.ToolDescriptions {
				m[k] = d
			}
			s.Variants[i].ToolDescriptions = m
		}
	}
	return s
}

// Default returns the default model's canonical name.
func (s *Set) Default() string { return s.def }

// Spec returns the spec the set was built from, as given.
func (s *Set) Spec() Spec { return cloneSpec(s.spec) }

// Names returns the models allowed by name, canonical, the default first.
func (s *Set) Names() []string { return slices.Clone(s.allow) }

// Patterns returns the allow patterns as given.
func (s *Set) Patterns() []string { return slices.Clone(s.spec.AllowPatterns) }

// Single reports whether the set allows only its default model.
func (s *Set) Single() bool { return len(s.allow) == 1 && len(s.patterns) == 0 }

// Allows reports whether the set allows the model name (canonical or not).
func (s *Set) Allows(name string) bool {
	c, err := Canonical(name)
	if err != nil {
		return false
	}
	if slices.Contains(s.allow, c) {
		return true
	}
	for _, re := range s.patterns {
		if re.MatchString(c) {
			return true
		}
	}
	return false
}

// Resolve returns the canonical name of the model a run asks for: the
// default for "", the name when the set allows it, and otherwise an error
// matching [ErrNotAllowed] or [ErrInvalidName].
func (s *Set) Resolve(name string) (string, error) {
	if name == "" {
		return s.def, nil
	}
	c, err := Canonical(name)
	if err != nil {
		return "", err
	}
	if !s.Allows(c) {
		return "", fmt.Errorf("%w: %q", ErrNotAllowed, c)
	}
	return c, nil
}

// Variant returns the first variant that matches the model name, and
// whether one did.
func (s *Set) Variant(name string) (Variant, bool) {
	c, err := Canonical(name)
	if err != nil {
		return Variant{}, false
	}
	for _, v := range s.variants {
		if v.name == c || (v.re != nil && v.re.MatchString(c)) {
			return cloneSpec(Spec{Variants: []Variant{v.Variant}}).Variants[0], true
		}
	}
	return Variant{}, false
}

// Matching returns the models of the set among known ones, for a picker:
// the default, then the names allowed exactly, then the known names a
// pattern allows, each once, canonical.
func (s *Set) Matching(known []string) []string {
	out := slices.Clone(s.allow)
	for _, k := range known {
		c, err := Canonical(k)
		if err != nil || slices.Contains(out, c) {
			continue
		}
		if s.Allows(c) {
			out = append(out, c)
		}
	}
	return out
}

// SameProvider reports whether two names have the same provider.
func SameProvider(a, b string) bool {
	pa, pb := Provider(a), Provider(b)
	return pa != "" && pa == pb
}
