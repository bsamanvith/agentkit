package tool_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/tool"
)

// full has every metadata field set.
var full = tool.Metadata{
	Kind: tool.KindSend, Risk: tool.RiskHigh, Interrupts: true, RequiresApproval: true,
	Tags:    []string{tool.TagExternal, "billing", "pii"},
	Display: tool.Display{Title: "Send email", Icon: "mail", Present: "Sending an email", Past: "Sent an email"},
}

func noop(context.Context, struct{}) (struct{}, error) { return struct{}{}, nil }

func TestMetadataJSONRoundTrip(t *testing.T) {
	t.Parallel()
	ft, err := tool.Func("send_email", "Send an email.", noop, tool.WithMetadata(full))
	if err != nil {
		t.Fatal(err)
	}
	def := ft.Definition()
	b, err := json.Marshal(def)
	if err != nil {
		t.Fatal(err)
	}
	var back tool.Definition
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(back.Metadata, full) {
		t.Errorf("metadata after a JSON round trip = %+v, want %+v", back.Metadata, full)
	}

	// The wire names are a contract with JSON clients and stored definitions.
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(b, &wire); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"kind", "risk", "interrupts", "requiresApproval", "tags", "display"} {
		if _, ok := wire[k]; !ok {
			t.Errorf("JSON %s has no %q", b, k)
		}
	}
	var display map[string]string
	if err := json.Unmarshal(wire["display"], &display); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"title": "Send email", "icon": "mail", "present": "Sending an email", "past": "Sent an email"}
	if !reflect.DeepEqual(display, want) {
		t.Errorf("display JSON = %v, want %v", display, want)
	}

	// An unclassified definition's JSON is what it was before metadata: the
	// new fields are all omitted.
	plain, err := json.Marshal(tool.Definition{Name: "x", InputSchema: json.RawMessage(`{"type":"object"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(plain), `{"name":"x","inputSchema":{"type":"object"}}`; got != want {
		t.Errorf("unclassified definition JSON = %s, want %s", got, want)
	}
}

func TestCloneSharesNothing(t *testing.T) {
	t.Parallel()
	orig := tool.Definition{
		Name: "t", InputSchema: json.RawMessage(`{"type":"object"}`), OutputSchema: json.RawMessage(`{"type":"object"}`),
		Metadata: full,
	}
	orig.Tags = append([]string(nil), full.Tags...)
	snapshot, err := json.Marshal(orig)
	if err != nil {
		t.Fatal(err)
	}
	c := orig.Clone()
	if !reflect.DeepEqual(c, orig) {
		t.Fatalf("Clone = %+v, want %+v", c, orig)
	}
	c.Tags[0] = "scribbled"
	c.InputSchema[0] = '!'
	c.OutputSchema[0] = '!'
	c.Display.Title = "changed"
	if after, _ := json.Marshal(orig); string(after) != string(snapshot) {
		t.Errorf("writing to a clone changed the original:\nbefore %s\nafter  %s", snapshot, after)
	}

	// The metadata given to WithMetadata is copied too.
	m := full
	m.Tags = append([]string(nil), full.Tags...)
	ft, err := tool.Func("t", "", noop, tool.WithMetadata(m))
	if err != nil {
		t.Fatal(err)
	}
	m.Tags[0] = "scribbled"
	if got := ft.Definition().Tags; !reflect.DeepEqual(got, full.Tags) {
		t.Errorf("writing to the metadata given to WithMetadata changed the tool's tags to %v", got)
	}
}

// TestClass: each field left empty, or set to a value outside the sets (a
// definition that skipped validation), is the unclassified default, and
// Classified is true only when neither was defaulted.
func TestClass(t *testing.T) {
	t.Parallel()
	tests := []struct {
		kind           tool.Kind
		risk           tool.Risk
		wantKind       tool.Kind
		wantRisk       tool.Risk
		wantClassified bool
	}{
		{"", "", tool.KindWrite, tool.RiskMedium, false}, // unclassified
		{tool.KindRead, "", tool.KindRead, tool.RiskMedium, false},
		{"", tool.RiskLow, tool.KindWrite, tool.RiskLow, false},
		{tool.KindRead, tool.RiskLow, tool.KindRead, tool.RiskLow, true},
		{tool.KindExecute, tool.RiskHigh, tool.KindExecute, tool.RiskHigh, true},
		{"Read", tool.RiskLow, tool.KindWrite, tool.RiskLow, false},
		{tool.KindRead, "none", tool.KindRead, tool.RiskMedium, false},
	}
	for _, tc := range tests {
		d := tool.Definition{Name: "t", Metadata: tool.Metadata{Kind: tc.kind, Risk: tc.risk}}
		k, r := d.Class()
		if k != tc.wantKind || r != tc.wantRisk || d.Classified() != tc.wantClassified {
			t.Errorf("kind %q, risk %q: Class() = %s/%s, Classified() = %v; want %s/%s, %v",
				tc.kind, tc.risk, k, r, d.Classified(), tc.wantKind, tc.wantRisk, tc.wantClassified)
		}
	}
}

// TestKindsAndRisksAreCopies: a caller writing to the lists cannot change
// what is valid.
func TestKindsAndRisksAreCopies(t *testing.T) {
	t.Parallel()
	tool.Kinds()[0] = "admin"
	tool.Risks()[0] = "none"
	if _, err := tool.Func("t", "", noop, tool.WithMetadata(tool.Metadata{Kind: "admin", Risk: "none"})); !errors.Is(err, tool.ErrInvalidDefinition) {
		t.Errorf("after writing to Kinds() and Risks(), Func = %v, want ErrInvalidDefinition", err)
	}
	if got := tool.Kinds(); got[0] != tool.KindRead || len(got) != 5 {
		t.Errorf("Kinds() = %v", got)
	}
	if got := tool.Risks(); !reflect.DeepEqual(got, []tool.Risk{tool.RiskLow, tool.RiskMedium, tool.RiskHigh}) {
		t.Errorf("Risks() = %v, want lowest first", got)
	}
}

// TestMetadataValidation: metadata that breaks a documented rule is refused
// by both Func and NewRegistry (which also sees wrapper tools); metadata that
// keeps them is accepted.
func TestMetadataValidation(t *testing.T) {
	t.Parallel()
	bad := map[string]tool.Metadata{
		"unknown kind":           {Kind: "readonly"},
		"unknown risk":           {Risk: "critical"},
		"upper-case tag":         {Tags: []string{"External"}},
		"tag starting with -":    {Tags: []string{"-x"}},
		"tag starting with .":    {Tags: []string{".x"}},
		"tag starting with _":    {Tags: []string{"_x"}},
		"empty tag":              {Tags: []string{""}},
		"tag with space":         {Tags: []string{"two words"}},
		"tag too long":           {Tags: []string{strings.Repeat("a", 65)}},
		"duplicate tag":          {Tags: []string{"pii", "billing", "pii"}},
		"icon with space":        {Display: tool.Display{Icon: "a b"}},
		"upper-case icon":        {Display: tool.Display{Icon: "Search"}},
		"icon too long":          {Display: tool.Display{Icon: strings.Repeat("a", 33)}},
		"present without past":   {Display: tool.Display{Present: "Searching"}},
		"past without present":   {Display: tool.Display{Past: "Searched"}},
		"title with newline":     {Display: tool.Display{Title: "Web\nsearch"}},
		"title with outer space": {Display: tool.Display{Title: " Web search"}},
		"title, trailing space":  {Display: tool.Display{Title: "Web search "}},
		"title too long":         {Display: tool.Display{Title: strings.Repeat("é", 81)}},
		"title, line separator":  {Display: tool.Display{Title: "Web\u2028search"}},
		"title, bidi override":   {Display: tool.Display{Title: "\u202eWeb search"}},
		"present not UTF-8":      {Display: tool.Display{Present: "Search\xff", Past: "Searched"}},
		"past with newline":      {Display: tool.Display{Present: "Searching", Past: "Searched\n"}},
		"past too long":          {Display: tool.Display{Present: "Searching", Past: strings.Repeat("é", 81)}},
	}
	good := map[string]tool.Metadata{
		"none":         {},
		"full":         full,
		"longest tag":  {Tags: []string{strings.Repeat("a", 64)}},
		"tag forms":    {Tags: []string{"0", "a.b", "a_b", "a-b"}},
		"title at max": {Display: tool.Display{Title: strings.Repeat("é", 80)}},
		"icon only":    {Display: tool.Display{Icon: "calc"}},
		"icon at max":  {Display: tool.Display{Icon: strings.Repeat("a", 32)}},
		"emoji title":  {Display: tool.Display{Title: "👩\u200d💻 Code"}}, // a joiner is not a bidi control
	}
	register := func(m tool.Metadata) error {
		_, err := tool.NewRegistry([]tool.Tool{customTool{name: "t", meta: m}})
		return err
	}
	build := func(m tool.Metadata) error {
		_, err := tool.Func("t", "", noop, tool.WithMetadata(m))
		return err
	}
	for name, m := range bad {
		if err := build(m); !errors.Is(err, tool.ErrInvalidDefinition) {
			t.Errorf("%s: Func = %v, want ErrInvalidDefinition", name, err)
		}
		if err := register(m); !errors.Is(err, tool.ErrInvalidDefinition) {
			t.Errorf("%s: NewRegistry = %v, want ErrInvalidDefinition", name, err)
		}
	}
	for name, m := range good {
		if err := build(m); err != nil {
			t.Errorf("%s: Func = %v, want success", name, err)
		}
		if err := register(m); err != nil {
			t.Errorf("%s: NewRegistry = %v, want success", name, err)
		}
	}
	if _, err := tool.Func("t", "", noop, tool.FuncOption{}); err != nil {
		t.Errorf("Func with a zero FuncOption = %v, want success", err)
	}
}

func yes() *bool { b := true; return &b }
func no() *bool  { b := false; return &b }

// TestAnnotationsTable: each row is a metadata and its MCP annotations. To
// MCP gives exactly the row's annotations; from MCP gives the row's
// metadata back, or the one in back where MCP lacks the concept or an
// unstated hint takes MCP's default (an unstated openWorldHint is external).
func TestAnnotationsTable(t *testing.T) {
	t.Parallel()
	ext := []string{tool.TagExternal}
	tests := []struct {
		name string
		m    tool.Metadata
		a    *tool.Annotations
		back *tool.Metadata // what FromAnnotations(a) gives, if not m
	}{
		{name: "unclassified", m: tool.Metadata{}, a: nil, back: &tool.Metadata{Tags: ext}},
		{
			name: "read", m: tool.Metadata{Kind: tool.KindRead},
			a: &tool.Annotations{ReadOnlyHint: true, OpenWorldHint: no()},
		},
		{
			// MCP ignores destructiveHint on a read-only tool; it is not sent.
			name: "read, high risk", m: tool.Metadata{Kind: tool.KindRead, Risk: tool.RiskHigh},
			a:    &tool.Annotations{ReadOnlyHint: true, OpenWorldHint: no()},
			back: &tool.Metadata{Kind: tool.KindRead},
		},
		{
			name: "read, external, titled", m: tool.Metadata{Kind: tool.KindRead, Tags: ext, Display: tool.Display{Title: "Web search"}},
			a: &tool.Annotations{Title: "Web search", ReadOnlyHint: true, OpenWorldHint: yes()},
		},
		{
			// MCP's destructiveHint false means "only additive updates", which
			// a write does not promise; MCP has no other way to say write.
			name: "write", m: tool.Metadata{Kind: tool.KindWrite},
			a:    &tool.Annotations{OpenWorldHint: no()},
			back: &tool.Metadata{},
		},
		{
			name: "write, high risk", m: tool.Metadata{Kind: tool.KindWrite, Risk: tool.RiskHigh},
			a: &tool.Annotations{DestructiveHint: yes(), OpenWorldHint: no()},
		},
		{
			name: "write, idempotent", m: tool.Metadata{Kind: tool.KindWrite, Tags: []string{tool.TagIdempotent}},
			a:    &tool.Annotations{IdempotentHint: true, OpenWorldHint: no()},
			back: &tool.Metadata{Tags: []string{tool.TagIdempotent}},
		},
		{
			name: "external and idempotent", m: tool.Metadata{Kind: tool.KindWrite, Tags: []string{tool.TagExternal, tool.TagIdempotent}},
			a:    &tool.Annotations{IdempotentHint: true, OpenWorldHint: yes()},
			back: &tool.Metadata{Tags: []string{tool.TagExternal, tool.TagIdempotent}},
		},
		{
			name: "execute", m: tool.Metadata{Kind: tool.KindExecute, Risk: tool.RiskMedium},
			a:    &tool.Annotations{OpenWorldHint: no()},
			back: &tool.Metadata{},
		},
		{
			name: "delete", m: tool.Metadata{Kind: tool.KindDelete},
			a:    &tool.Annotations{DestructiveHint: yes(), OpenWorldHint: no()},
			back: &tool.Metadata{Kind: tool.KindWrite, Risk: tool.RiskHigh},
		},
		{
			name: "unclassified, external", m: tool.Metadata{Tags: ext},
			a: &tool.Annotations{OpenWorldHint: yes()},
		},
		{
			name: "unclassified, titled", m: tool.Metadata{Display: tool.Display{Title: "Thing"}},
			a:    &tool.Annotations{Title: "Thing"},
			back: &tool.Metadata{Tags: ext, Display: tool.Display{Title: "Thing"}},
		},
		{
			// A risk alone says nothing about the world the tool reaches.
			name: "risk only, high", m: tool.Metadata{Risk: tool.RiskHigh},
			a:    &tool.Annotations{DestructiveHint: yes()},
			back: &tool.Metadata{Kind: tool.KindWrite, Risk: tool.RiskHigh, Tags: ext},
		},
		{name: "risk only, low", m: tool.Metadata{Risk: tool.RiskLow}, a: nil, back: &tool.Metadata{Tags: ext}},
		{
			// A send reaches outside whether or not it is tagged external.
			name: "send, no MCP concept", m: tool.Metadata{Kind: tool.KindSend, Risk: tool.RiskLow, Interrupts: true, RequiresApproval: true, Tags: []string{"billing"}},
			a:    &tool.Annotations{OpenWorldHint: yes()},
			back: &tool.Metadata{Tags: []string{tool.TagExternal}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.m.Annotations(); !reflect.DeepEqual(got, tc.a) {
				t.Errorf("Annotations() = %s, want %s", show(got), show(tc.a))
			}
			want := tc.m
			if tc.back != nil {
				want = *tc.back
			}
			if got := tool.FromAnnotations(tc.a); !reflect.DeepEqual(got, want) {
				t.Errorf("FromAnnotations(%s) = %+v, want %+v", show(tc.a), got, want)
			}
		})
	}
}

// TestAnnotationsRoundTrip: for every combination of kind, risk, the mapped
// tags and a title, going to MCP and back keeps everything MCP has a concept
// for: read or not, destructive (delete or high risk) or not, open world
// (external or send), idempotent and the title. What comes back is valid
// metadata, and published again it is never less cautious than the first
// time: no hint moves toward read-only, non-destructive or closed-world.
func TestAnnotationsRoundTrip(t *testing.T) {
	t.Parallel()
	kinds := append([]tool.Kind{""}, tool.Kinds()...)
	risks := append([]tool.Risk{""}, tool.Risks()...)
	tagSets := [][]string{nil, {tool.TagExternal}, {tool.TagIdempotent}, {"billing", tool.TagExternal, tool.TagIdempotent}}
	n := 0
	for _, k := range kinds {
		for _, r := range risks {
			for _, tags := range tagSets {
				for _, title := range []string{"", "Title"} {
					n++
					m := tool.Metadata{Kind: k, Risk: r, Tags: tags, Display: tool.Display{Title: title}}
					a := m.Annotations()
					back := tool.FromAnnotations(a)
					name := fmt.Sprintf("%+v", m)
					k1, r1 := m.Class()
					k2, r2 := back.Class()
					if (k1 == tool.KindRead) != (k2 == tool.KindRead) {
						t.Errorf("%s: read-only became %s", name, k2)
					}
					if k1 != tool.KindRead && (k1 == tool.KindDelete || r1 == tool.RiskHigh) != (r2 == tool.RiskHigh) {
						t.Errorf("%s: destructive came back as %s/%s", name, k2, r2)
					}
					// Open world, stated or by MCP's default when the kind is unknown.
					if open := m.HasTag(tool.TagExternal) || k == tool.KindSend || k == ""; back.HasTag(tool.TagExternal) != open {
						t.Errorf("%s: open world came back as tags %v", name, back.Tags)
					}
					if m.HasTag(tool.TagIdempotent) != back.HasTag(tool.TagIdempotent) {
						t.Errorf("%s: tag idempotent came back as %v", name, back.Tags)
					}
					if back.Display.Title != title {
						t.Errorf("%s: title came back as %q", name, back.Display.Title)
					}
					if _, err := tool.Func("t", "", noop, tool.WithMetadata(back)); err != nil {
						t.Errorf("%s: what came back is invalid: %v", name, err)
					}
					if again := back.Annotations(); !asCautious(again, a) {
						t.Errorf("%s: annotations %s came back as %s, which is less cautious", name, show(a), show(again))
					}
				}
			}
		}
	}
	if want := 6 * 4 * 4 * 2; n != want {
		t.Fatalf("tried %d combinations, want %d", n, want)
	}
}

// asCautious reports whether annotations b claim no more safety than a:
// read-only only if a is, non-destructive or closed-world only if a says so,
// with an unstated hint read as MCP's default (destructive, open world).
func asCautious(b, a *tool.Annotations) bool {
	var zero tool.Annotations
	if a == nil {
		a = &zero
	}
	if b == nil {
		b = &zero
	}
	destructive := func(x *tool.Annotations) bool { return x.DestructiveHint == nil || *x.DestructiveHint }
	open := func(x *tool.Annotations) bool { return x.OpenWorldHint == nil || *x.OpenWorldHint }
	return (!b.ReadOnlyHint || a.ReadOnlyHint) &&
		(destructive(b) || !destructive(a)) &&
		(open(b) || !open(a)) &&
		b.IdempotentHint == a.IdempotentHint && b.Title == a.Title
}

// TestFromAnnotations: hints from another server, including ones our own
// mapping never produces, give metadata that always passes validation.
func TestFromAnnotations(t *testing.T) {
	t.Parallel()
	ext := []string{tool.TagExternal}
	tests := []struct {
		name string
		a    *tool.Annotations
		want tool.Metadata
	}{
		{"no annotations", nil, tool.Metadata{Tags: ext}},
		{"additive only", &tool.Annotations{DestructiveHint: no(), OpenWorldHint: no()}, tool.Metadata{Kind: tool.KindWrite}},
		{"read-only wins", &tool.Annotations{ReadOnlyHint: true, DestructiveHint: yes(), OpenWorldHint: no()}, tool.Metadata{Kind: tool.KindRead}},
		{"open world by default", &tool.Annotations{ReadOnlyHint: true}, tool.Metadata{Kind: tool.KindRead, Tags: ext}},
		{"closed world", &tool.Annotations{OpenWorldHint: no()}, tool.Metadata{}},
		{"title trimmed", &tool.Annotations{Title: "  Web search\n", OpenWorldHint: no()}, tool.Metadata{Display: tool.Display{Title: "Web search"}}},
		{"multi-line title dropped", &tool.Annotations{Title: "Web\nsearch", OpenWorldHint: no()}, tool.Metadata{}},
		{"long title dropped", &tool.Annotations{Title: strings.Repeat("a", 81), OpenWorldHint: no()}, tool.Metadata{}},
		{"bad UTF-8 title dropped", &tool.Annotations{Title: "Web\xff", OpenWorldHint: no()}, tool.Metadata{}},
	}
	for _, tc := range tests {
		got := tool.FromAnnotations(tc.a)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: FromAnnotations = %+v, want %+v", tc.name, got, tc.want)
		}
		if _, err := tool.Func("t", "", noop, tool.WithMetadata(got)); err != nil {
			t.Errorf("%s: FromAnnotations gave invalid metadata: %v", tc.name, err)
		}
	}
}

func show(a *tool.Annotations) string {
	if a == nil {
		return "nil"
	}
	b := func(p *bool) string {
		if p == nil {
			return "unset"
		}
		return fmt.Sprint(*p)
	}
	return fmt.Sprintf("{title %q readOnly %v destructive %s idempotent %v openWorld %s}",
		a.Title, a.ReadOnlyHint, b(a.DestructiveHint), a.IdempotentHint, b(a.OpenWorldHint))
}
