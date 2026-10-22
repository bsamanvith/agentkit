package toolset_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/credentials"
	"github.com/bsamanvith/agentkit/pkg/tool"
	"github.com/bsamanvith/agentkit/pkg/tool/tooltest"
	"github.com/bsamanvith/agentkit/pkg/toolset"
)

// sourceFactory records the source each build was given.
type sourceFactory struct {
	mu   sync.Mutex
	got  map[string]credentials.Source // by the settings' "name"
	seen int
}

func (f *sourceFactory) Type() string { return "needs-credential" }

func (f *sourceFactory) Build(_ context.Context, in toolset.BuildInput) ([]tool.Tool, error) {
	var s struct {
		Name string `json:"name"`
	}
	if err := toolset.DecodeSettings(in.Settings, &s); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seen++
	if f.got == nil {
		f.got = map[string]credentials.Source{}
	}
	f.got[s.Name] = in.Credentials
	return tooltest.Fixtures(), nil
}

// namedSource is what the test provider hands out: which toolset, slot and
// scope it was asked for.
type namedSource struct{ toolset, slot, scope string }

func (namedSource) Get(context.Context) (credentials.Credential, error) { return nil, nil }

func (namedSource) HTTPClient(context.Context, *http.Client, ...credentials.AuthOption) (*http.Client, error) {
	return nil, nil
}

func provider(known ...string) toolset.SourceFunc {
	return func(ts, slot string, scope credentials.Scope) (credentials.Source, error) {
		for _, k := range known {
			if k == slot {
				return namedSource{ts, slot, string(scope)}, nil
			}
		}
		return nil, errors.New("unknown slot " + slot)
	}
}

func credSpec(name, slot string, scope credentials.Scope) toolset.Spec {
	return toolset.Spec{
		Name: name, Type: "needs-credential", Settings: json.RawMessage(`{"name":"` + name + `"}`),
		Credential: &toolset.CredentialRef{Slot: slot, Scope: scope},
	}
}

// A spec's slot reaches its factory as a source for that slot alone; a
// spec without one gets none. Two toolsets may share a slot.
func TestBuildGivesTheSlotsSource(t *testing.T) {
	t.Parallel()
	f := &sourceFactory{}
	cat := newCatalog(t, f)
	specs := []toolset.Spec{
		credSpec("crm-a", "salesforce-a", ""),
		credSpec("crm-a-reports", "salesforce-a", credentials.ScopeTenant),
		credSpec("crm-b", "salesforce-b", ""),
		credSpec("mail", "gmail", credentials.ScopeUser),
		{Name: "plain", Type: "needs-credential", Settings: json.RawMessage(`{"name":"plain"}`)},
	}
	if _, _, err := toolset.Build(t.Context(), cat, specs, toolset.WithSources(provider("salesforce-a", "salesforce-b", "gmail"))); err != nil {
		t.Fatal(err)
	}
	want := map[string]credentials.Source{
		"crm-a":         namedSource{"crm-a", "salesforce-a", ""},
		"crm-a-reports": namedSource{"crm-a-reports", "salesforce-a", "tenant"},
		"crm-b":         namedSource{"crm-b", "salesforce-b", ""},
		"mail":          namedSource{"mail", "gmail", "user"},
		"plain":         nil,
	}
	for name, w := range want {
		if got := f.got[name]; got != w {
			t.Errorf("%s got source %#v, want %#v", name, got, w)
		}
	}
}

// A slot with no source fails the build when the spec is required, and
// skips the toolset when it is optional; a malformed slot or scope fails
// either way, before any factory runs.
func TestBuildCredentialFailures(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		spec     toolset.Spec
		opts     []toolset.BuildOption
		want     error
		problem  bool // skipped as a Problem instead
		noBuilds bool
	}{
		{"unknown slot", credSpec("x", "nope", ""), []toolset.BuildOption{toolset.WithSources(provider("crm"))}, toolset.ErrNoCredentials, false, false},
		{"unknown slot, optional", optional(credSpec("x", "nope", "")), []toolset.BuildOption{toolset.WithSources(provider("crm"))}, toolset.ErrNoCredentials, true, false},
		{"no sources", credSpec("x", "crm", ""), nil, toolset.ErrNoCredentials, false, false},
		{"no sources, optional", optional(credSpec("x", "crm", "")), nil, toolset.ErrNoCredentials, true, false},
		{"bad slot", optional(credSpec("x", "has space", "")), []toolset.BuildOption{toolset.WithSources(provider("crm"))}, toolset.ErrInvalidCredentialRef, false, true},
		{"bad scope", optional(credSpec("x", "crm", "everyone")), []toolset.BuildOption{toolset.WithSources(provider("crm"))}, toolset.ErrInvalidCredentialRef, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := &sourceFactory{}
			regs, problems, err := toolset.Build(t.Context(), newCatalog(t, f), []toolset.Spec{tc.spec}, tc.opts...)
			if tc.problem {
				if err != nil || len(problems) != 1 || !errors.Is(problems[0].Err, tc.want) || regs["x"] != nil {
					t.Fatalf("regs %v, problems %v, err %v; want x skipped with %v", regs, problems, err, tc.want)
				}
				return
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if tc.noBuilds && f.seen != 0 {
				t.Errorf("%d factories ran before the spec was checked", f.seen)
			}
		})
	}
}

func optional(s toolset.Spec) toolset.Spec {
	s.Optional = true
	return s
}

func TestSpecCredentialJSON(t *testing.T) {
	t.Parallel()
	var s toolset.Spec
	if err := json.Unmarshal([]byte(`{"name":"m","type":"gmail","credential":{"slot":"gmail","scope":"user"}}`), &s); err != nil {
		t.Fatal(err)
	}
	if s.Credential == nil || *s.Credential != (toolset.CredentialRef{Slot: "gmail", Scope: credentials.ScopeUser}) {
		t.Errorf("credential = %+v", s.Credential)
	}
	if err := json.Unmarshal([]byte(`{"name":"m","type":"gmail","credential":{"slot":"gmail","key":"sk-123"}}`), &s); err == nil {
		t.Error("a credential with an unknown field (a key in config) was accepted")
	}
}
