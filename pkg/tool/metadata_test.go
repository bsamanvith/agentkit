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

func TestClassify(t *testing.T) {
	t.Parallel()
	tests := []struct {
		kind     tool.Kind
		risk     tool.Risk
		wantKind tool.Kind
		wantRisk tool.Risk
	}{
		{"", "", tool.KindWrite, tool.RiskMedium}, // unclassified
		{tool.KindRead, "", tool.KindRead, tool.RiskMedium},
		{"", tool.RiskLow, tool.KindWrite, tool.RiskLow},
		{tool.KindRead, tool.RiskLow, tool.KindRead, tool.RiskLow},
		{tool.KindExecute, tool.RiskHigh, tool.KindExecute, tool.RiskHigh},
	}
	for _, tc := range tests {
		k, r := tool.Classify(tool.Definition{Name: "t", Metadata: tool.Metadata{Kind: tc.kind, Risk: tc.risk}})
		if k != tc.wantKind || r != tc.wantRisk {
			t.Errorf("Classify(kind %q, risk %q) = %s/%s, want %s/%s", tc.kind, tc.risk, k, r, tc.wantKind, tc.wantRisk)
		}
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
		"empty tag":              {Tags: []string{""}},
		"tag with space":         {Tags: []string{"two words"}},
		"tag too long":           {Tags: []string{strings.Repeat("a", 65)}},
		"duplicate tag":          {Tags: []string{"pii", "billing", "pii"}},
		"icon with space":        {Display: tool.Display{Icon: "a b"}},
		"present without past":   {Display: tool.Display{Present: "Searching"}},
		"past without present":   {Display: tool.Display{Past: "Searched"}},
		"title with newline":     {Display: tool.Display{Title: "Web\nsearch"}},
		"title with outer space": {Display: tool.Display{Title: " Web search"}},
		"title too long":         {Display: tool.Display{Title: strings.Repeat("é", 81)}},
		"present not UTF-8":      {Display: tool.Display{Present: "Search\xff", Past: "Searched"}},
	}
	good := map[string]tool.Metadata{
		"none":         {},
		"full":         full,
		"longest tag":  {Tags: []string{strings.Repeat("a", 64)}},
		"tag forms":    {Tags: []string{"0", "a.b", "a_b", "a-b"}},
		"title at max": {Display: tool.Display{Title: strings.Repeat("é", 80)}},
		"icon only":    {Display: tool.Display{Icon: "calc"}},
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
}

func yes() *bool { b := true; return &b }
func no() *bool  { b := false; return &b }

// TestAnnotationsTable: each row is a metadata and its MCP annotations. To
// MCP gives exactly the row's annotations; from MCP gives the row's
// metadata back, or the one in back where MCP lacks the concept.
func TestAnnotationsTable(t *testing.T) {
	t.Parallel()
	ext := []string{tool.TagExternal}
	tests := []struct {
		name string
		m    tool.Metadata
		a    *tool.Annotations
		back *tool.Metadata // what FromAnnotations(a) gives, if not m
	}{
		{name: "unclassified", m: tool.Metadata{}, a: nil},
		{
			name: "read", m: tool.Metadata{Kind: tool.KindRead},
			a: &tool.Annotations{ReadOnlyHint: true, OpenWorldHint: no()},
		},
		{
			name: "read, external, titled", m: tool.Metadata{Kind: tool.KindRead, Tags: ext, Display: tool.Display{Title: "Web search"}},
			a: &tool.Annotations{Title: "Web search", ReadOnlyHint: true, OpenWorldHint: yes()},
		},
		{
			name: "write", m: tool.Metadata{Kind: tool.KindWrite},
			a: &tool.Annotations{DestructiveHint: no(), OpenWorldHint: no()},
		},
		{
			name: "write, high risk", m: tool.Metadata{Kind: tool.KindWrite, Risk: tool.RiskHigh},
			a: &tool.Annotations{DestructiveHint: yes(), OpenWorldHint: no()},
		},
		{
			name: "write, idempotent", m: tool.Metadata{Kind: tool.KindWrite, Tags: []string{tool.TagIdempotent}},
			a: &tool.Annotations{DestructiveHint: no(), IdempotentHint: true, OpenWorldHint: no()},
		},
		{
			name: "external and idempotent", m: tool.Metadata{Kind: tool.KindWrite, Tags: []string{tool.TagExternal, tool.TagIdempotent}},
			a: &tool.Annotations{DestructiveHint: no(), IdempotentHint: true, OpenWorldHint: yes()},
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
			a: &tool.Annotations{Title: "Thing"},
		},
		{
			name: "no MCP concept", m: tool.Metadata{Kind: tool.KindSend, Risk: tool.RiskLow, Interrupts: true, RequiresApproval: true, Tags: []string{"billing"}},
			a:    &tool.Annotations{DestructiveHint: no(), OpenWorldHint: no()},
			back: &tool.Metadata{Kind: tool.KindWrite},
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
// for: read or not, destructive (delete or high risk) or not, external,
// idempotent and the title. And MCP's view is a fixed point: annotations
// read back and published again are the same annotations.
func TestAnnotationsRoundTrip(t *testing.T) {
	t.Parallel()
	kinds := append([]tool.Kind{""}, tool.Kinds...)
	risks := append([]tool.Risk{""}, tool.Risks...)
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
					k1, r1 := tool.Classify(tool.Definition{Metadata: m})
					k2, r2 := tool.Classify(tool.Definition{Metadata: back})
					if (k1 == tool.KindRead) != (k2 == tool.KindRead) {
						t.Errorf("%s: read-only became %s", name, k2)
					}
					if k1 != tool.KindRead && (k1 == tool.KindDelete || r1 == tool.RiskHigh) != (r2 == tool.RiskHigh) {
						t.Errorf("%s: destructive came back as %s/%s", name, k2, r2)
					}
					for _, tag := range []string{tool.TagExternal, tool.TagIdempotent} {
						if m.HasTag(tag) != back.HasTag(tag) {
							t.Errorf("%s: tag %s came back as %v", name, tag, back.Tags)
						}
					}
					if back.Display.Title != title {
						t.Errorf("%s: title came back as %q", name, back.Display.Title)
					}
					if again := back.Annotations(); !reflect.DeepEqual(again, a) {
						t.Errorf("%s: annotations %s came back as %s", name, show(a), show(again))
					}
				}
			}
		}
	}
	if want := 6 * 4 * 4 * 2; n != want {
		t.Fatalf("tried %d combinations, want %d", n, want)
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
