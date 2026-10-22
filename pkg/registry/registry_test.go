package registry_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/llm/llmtest"
	"github.com/bsamanvith/agentkit/pkg/registry"
	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/store/memory"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

// def is the definition the test builder knows: a toy of serve's.
type def struct {
	Name     string   `json:"name,omitempty"`
	System   string   `json:"system"`
	Toolsets []string `json:"toolsets,omitempty"`
	Broken   bool     `json:"broken,omitempty"` // the builder fails as a whole
}

// rig is a registry over a memory store, with a builder that counts its
// builds and checks that every agent it builds is valid. After each test it
// checks the registry's invariants: every version reads back as the
// definition it was saved as, and the published version of every agent
// builds to the system prompt of that version.
type rig struct {
	t      *testing.T
	st     *memory.Store
	r      *registry.Registry
	builds atomic.Int64

	mu    sync.Mutex
	saved map[[2]string]map[int64]def // (tenant, agent) → version → definition
}

func newRig(t *testing.T, opts ...registry.Option) *rig {
	t.Helper()
	g := &rig{t: t, st: memory.New(), saved: map[[2]string]map[int64]def{}}
	r, err := registry.New(g.st, g.build, opts...)
	if err != nil {
		t.Fatal(err)
	}
	g.r = r
	t.Cleanup(g.check)
	return g
}

func (g *rig) build(ctx context.Context, name string, raw json.RawMessage) (agentapi.Agent, error) {
	g.builds.Add(1)
	var d def
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&d); err != nil {
		return agentapi.Agent{}, &agentapi.DefinitionError{Fields: []wire.FieldError{{Message: err.Error()}}}
	}
	if d.Name != name {
		g.t.Errorf("the builder got name %q in a definition of %q", d.Name, name)
	}
	var fields []wire.FieldError
	if d.System == "" {
		fields = append(fields, wire.FieldError{Field: "system", Message: "required"})
	}
	for _, ts := range d.Toolsets {
		if ts != "demo" {
			fields = append(fields, wire.FieldError{Field: "toolsets", Message: fmt.Sprintf("names %q, which the server does not have", ts)})
		}
	}
	if len(fields) > 0 {
		return agentapi.Agent{}, &agentapi.DefinitionError{Fields: fields}
	}
	if d.Broken {
		return agentapi.Agent{}, errors.New("the model cannot be built")
	}
	tools, err := tool.NewRegistry(nil)
	if err != nil {
		return agentapi.Agent{}, err
	}
	h, err := harness.New(llmtest.NewFake(g.t), tools, harness.WithSystem(d.System))
	if err != nil {
		return agentapi.Agent{}, err
	}
	return agentapi.Agent{Name: name, System: d.System, Toolsets: d.Toolsets, Harness: h}, nil
}

func ctxOf(t *testing.T, tenant string) context.Context {
	t.Helper()
	tn, err := tenancy.NewTenant(tenant)
	if err != nil {
		t.Fatal(err)
	}
	return tenancy.With(t.Context(), tn)
}

func raw(t *testing.T, d def) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// save saves d as name's next version and records it.
func (g *rig) save(ctx context.Context, name string, d def, publish bool) wire.AgentVersion {
	g.t.Helper()
	v, err := g.r.Save(ctx, "key-1", name, wire.SaveAgentRequest{Definition: raw(g.t, d), Message: "change", Publish: publish})
	if err != nil {
		g.t.Fatalf("Save(%s): %v", name, err)
	}
	if v.Name != name || v.Published != publish || v.Author != "key-1" || v.Message != "change" {
		g.t.Errorf("Save returned %+v", v)
	}
	tn, _ := tenancy.From(ctx)
	g.mu.Lock()
	k := [2]string{tn.ID(), name}
	if g.saved[k] == nil {
		g.saved[k] = map[int64]def{}
	}
	d.Name = name
	g.saved[k][v.Version] = d
	g.mu.Unlock()
	return v
}

func (g *rig) system(ctx context.Context, name string, version int64) (string, int64) {
	g.t.Helper()
	a, err := g.r.Agent(ctx, name, version)
	if err != nil {
		g.t.Fatalf("Agent(%s, %d): %v", name, version, err)
	}
	return a.System, a.Version
}

// check reads every saved version back, and builds every published one.
func (g *rig) check() {
	g.mu.Lock()
	defer g.mu.Unlock()
	for k, versions := range g.saved {
		tn, _ := tenancy.NewTenant(k[0])
		ctx := tenancy.With(context.Background(), tn)
		list, err := g.r.Versions(ctx, k[1])
		if err != nil {
			g.t.Errorf("Versions(%s): %v", k[1], err)
			continue
		}
		for _, v := range list.Versions {
			want, ok := versions[v.Version]
			if !ok {
				continue // a draft the test deleted
			}
			var got def
			if err := json.Unmarshal(v.Definition, &got); err != nil || !equalDef(got, want) {
				g.t.Errorf("%s version %d reads back as %s, want %+v", k[1], v.Version, v.Definition, want)
			}
			if v.Published != (v.Version == list.Published) {
				g.t.Errorf("%s version %d: published %v, but the agent publishes %d", k[1], v.Version, v.Published, list.Published)
			}
		}
		if list.Published != 0 {
			a, err := g.r.Agent(ctx, k[1], 0)
			if err != nil || a.Version != list.Published || a.System != versions[list.Published].System {
				g.t.Errorf("Agent(%s, published) = %+v, %v; want version %d with %q", k[1], a, err, list.Published, versions[list.Published].System)
			}
		}
	}
}

func equalDef(a, b def) bool {
	return a.Name == b.Name && a.System == b.System && slices.Equal(a.Toolsets, b.Toolsets) && a.Broken == b.Broken
}

// Saving creates version 1, editing version 2; publishing 2 makes the
// published agent 2 while version 1 still builds as 1; publishing 1 again
// rolls back.
func TestVersionsPublishAndRollBack(t *testing.T) {
	t.Parallel()
	g := newRig(t)
	ctx := ctxOf(t, "acme")
	if v := g.save(ctx, "support", def{System: "one"}, false); v.Version != 1 {
		t.Errorf("first save is version %d, want 1", v.Version)
	}
	if _, err := g.r.Agent(ctx, "support", 0); !errors.Is(err, agentapi.ErrAgentNotFound) {
		t.Errorf("the published agent before any publication: %v, want ErrAgentNotFound", err)
	}
	if v := g.save(ctx, "support", def{System: "two"}, false); v.Version != 2 {
		t.Errorf("second save is version %d, want 2", v.Version)
	}
	l, err := g.r.Publish(ctx, "support", 2)
	if err != nil || l.Published != 2 || l.Latest != 2 || len(l.Versions) != 2 {
		t.Fatalf("Publish(2) = %+v, %v", l, err)
	}
	if sys, v := g.system(ctx, "support", 0); sys != "two" || v != 2 {
		t.Errorf("published agent = %q version %d, want two, 2", sys, v)
	}
	// A run that started on 1 keeps building as 1.
	if sys, v := g.system(ctx, "support", 1); sys != "one" || v != 1 {
		t.Errorf("version 1 = %q version %d, want one, 1", sys, v)
	}
	if _, err := g.r.Publish(ctx, "support", 1); err != nil {
		t.Fatal(err)
	}
	if sys, v := g.system(ctx, "support", 0); sys != "one" || v != 1 {
		t.Errorf("after the rollback, the published agent = %q version %d, want one, 1", sys, v)
	}
	// Save and publish in one call.
	if v := g.save(ctx, "support", def{System: "three"}, true); v.Version != 3 || v.PublishedAt == nil {
		t.Errorf("save and publish = %+v, want version 3, published", v)
	}
	if sys, _ := g.system(ctx, "support", 0); sys != "three" {
		t.Errorf("published agent = %q, want three", sys)
	}
	all, err := g.r.Agents(ctx)
	if err != nil || len(all) != 1 || all[0].Name != "support" || all[0].Version != 3 {
		t.Errorf("Agents = %+v, %v; want support at 3", all, err)
	}
}

// An invalid definition is refused with the builder's field errors, and
// changes nothing: no version is stored, the published one stays.
func TestInvalidDefinitionChangesNothing(t *testing.T) {
	t.Parallel()
	g := newRig(t)
	ctx := ctxOf(t, "acme")
	g.save(ctx, "a", def{System: "ok"}, true)
	for name, c := range map[string]struct {
		body  string
		field string
	}{
		"unknown toolset":   {`{"system": "x", "toolsets": ["exa"]}`, "toolsets"},
		"no system":         {`{"toolsets": ["demo"]}`, "system"},
		"another name":      {`{"name": "b", "system": "x"}`, "name"},
		"not an object":     {`["system"]`, ""},
		"unknown field":     {`{"system": "x", "colour": 1}`, ""},
		"fails as a whole":  {`{"system": "x", "broken": true}`, ""},
		"null":              {`null`, ""},
		"a name not string": {`{"name": 7, "system": "x"}`, "name"},
	} {
		_, err := g.r.Save(ctx, "key-1", "a", wire.SaveAgentRequest{Definition: json.RawMessage(c.body), Publish: true})
		var de *agentapi.DefinitionError
		if !errors.As(err, &de) || len(de.Fields) == 0 || de.Fields[0].Field != c.field {
			t.Errorf("%s: Save = %v, want a DefinitionError on field %q", name, err, c.field)
		}
		if _, err := g.r.Save(ctx, "key-1", "new", wire.SaveAgentRequest{Definition: json.RawMessage(c.body)}); !errors.As(err, &de) && name != "another name" {
			t.Errorf("%s: creating an agent with it = %v, want a DefinitionError", name, err)
		}
	}
	l, err := g.r.Versions(ctx, "a")
	if err != nil || l.Latest != 1 || l.Published != 1 || len(l.Versions) != 1 {
		t.Errorf("after refused saves, Versions = %+v, %v; want only version 1, published", l, err)
	}
	if _, err := g.r.Versions(ctx, "new"); !errors.Is(err, agentapi.ErrAgentNotFound) {
		t.Errorf("an agent whose creation was refused: %v, want ErrAgentNotFound", err)
	}
}

// The agents of the config file are read-only: saving, publishing or
// deleting under their names is refused, and nothing is stored.
func TestConfigAgentsAreReadOnly(t *testing.T) {
	t.Parallel()
	g := newRig(t, registry.WithReadOnly("default", "search"))
	ctx := ctxOf(t, "acme")
	if _, err := g.r.Save(ctx, "k", "search", wire.SaveAgentRequest{Definition: raw(t, def{System: "x"})}); !errors.Is(err, agentapi.ErrAgentReadOnly) {
		t.Errorf("saving a config agent: %v, want ErrAgentReadOnly", err)
	}
	if _, err := g.r.Publish(ctx, "default", 1); !errors.Is(err, agentapi.ErrAgentReadOnly) {
		t.Errorf("publishing a config agent: %v, want ErrAgentReadOnly", err)
	}
	if err := g.r.DeleteVersion(ctx, "default", 1); !errors.Is(err, agentapi.ErrAgentReadOnly) {
		t.Errorf("deleting a config agent's version: %v, want ErrAgentReadOnly", err)
	}
	if items, _ := g.st.ListItems(ctx, registry.KindAgent); len(items) != 0 {
		t.Errorf("stored %v", items)
	}
}

// Only drafts are deleted; a deleted draft's number is not given again.
func TestDeleteDrafts(t *testing.T) {
	t.Parallel()
	g := newRig(t)
	ctx := ctxOf(t, "acme")
	g.save(ctx, "a", def{System: "1"}, true)
	g.save(ctx, "a", def{System: "2"}, false)
	if err := g.r.DeleteVersion(ctx, "a", 1); !errors.Is(err, agentapi.ErrVersionPublished) {
		t.Errorf("deleting the published version: %v, want ErrVersionPublished", err)
	}
	if err := g.r.DeleteVersion(ctx, "a", 2); err != nil {
		t.Fatal(err)
	}
	g.mu.Lock()
	delete(g.saved[[2]string{"acme", "a"}], 2)
	g.mu.Unlock()
	if err := g.r.DeleteVersion(ctx, "a", 2); !errors.Is(err, agentapi.ErrAgentNotFound) {
		t.Errorf("deleting it again: %v, want ErrAgentNotFound", err)
	}
	if _, err := g.r.Publish(ctx, "a", 2); !errors.Is(err, agentapi.ErrAgentNotFound) {
		t.Errorf("publishing a deleted draft: %v, want ErrAgentNotFound", err)
	}
	if v := g.save(ctx, "a", def{System: "3"}, false); v.Version != 3 {
		t.Errorf("the save after deleting draft 2 is version %d, want 3", v.Version)
	}
}

// Saves compare and swap: if_latest names the version the caller read.
func TestSaveIfLatest(t *testing.T) {
	t.Parallel()
	g := newRig(t)
	ctx := ctxOf(t, "acme")
	zero, one := int64(0), int64(1)
	if _, err := g.r.Save(ctx, "k", "a", wire.SaveAgentRequest{Definition: raw(t, def{System: "x"}), IfLatest: &one}); !errors.Is(err, agentapi.ErrAgentNotFound) {
		t.Errorf("saving on version 1 of no agent: %v, want ErrAgentNotFound", err)
	}
	g.save(ctx, "a", def{System: "1"}, false)
	if _, err := g.r.Save(ctx, "k", "a", wire.SaveAgentRequest{Definition: raw(t, def{System: "x"}), IfLatest: &zero}); !errors.Is(err, agentapi.ErrAgentConflict) {
		t.Errorf("creating an agent that exists: %v, want ErrAgentConflict", err)
	}
	v, err := g.r.Save(ctx, "k", "a", wire.SaveAgentRequest{Definition: raw(t, def{System: "2"}), IfLatest: &one})
	if err != nil || v.Version != 2 {
		t.Fatalf("saving on version 1 = %+v, %v", v, err)
	}
	g.mu.Lock()
	g.saved[[2]string{"acme", "a"}][2] = def{Name: "a", System: "2"}
	g.mu.Unlock()
	if _, err := g.r.Save(ctx, "k", "a", wire.SaveAgentRequest{Definition: raw(t, def{System: "x"}), IfLatest: &one}); !errors.Is(err, agentapi.ErrAgentConflict) {
		t.Errorf("saving on a stale version: %v, want ErrAgentConflict", err)
	}
}

// A version's agent is built once, then served from the cache; another
// version is another build. A small cache rebuilds what it let go.
func TestHarnessBuiltOncePerVersion(t *testing.T) {
	t.Parallel()
	g := newRig(t, registry.WithCacheSize(1))
	ctx := ctxOf(t, "acme")
	g.save(ctx, "a", def{System: "1"}, true) // one build to validate
	g.builds.Store(0)
	for range 3 {
		g.system(ctx, "a", 0)
	}
	if n := g.builds.Load(); n != 1 {
		t.Errorf("three runs of one version built it %d times, want once", n)
	}
	g.save(ctx, "a", def{System: "2"}, true)
	g.builds.Store(0)
	if sys, _ := g.system(ctx, "a", 0); sys != "2" {
		t.Errorf("after publishing 2 the agent is %q", sys)
	}
	if n := g.builds.Load(); n != 1 {
		t.Errorf("the newly published version was built %d times, want once", n)
	}
	g.system(ctx, "a", 1) // pushes 2 out of a cache of one
	g.builds.Store(0)
	g.system(ctx, "a", 2)
	if n := g.builds.Load(); n != 1 {
		t.Errorf("a version the cache let go was built %d times, want once again", n)
	}
	// What the cache hands out cannot change what it keeps.
	a, _ := g.r.Agent(ctx, "a", 2)
	a.Toolsets = append(a.Toolsets, "planted")
	if b, _ := g.r.Agent(ctx, "a", 2); slices.Contains(b.Toolsets, "planted") {
		t.Errorf("a caller's change reached the cache: %v", b.Toolsets)
	}
}

// Each tenant has its own agents: another tenant's are not found, and the
// same name in two tenants is two agents.
func TestTenants(t *testing.T) {
	t.Parallel()
	g := newRig(t)
	a, b := ctxOf(t, "acme"), ctxOf(t, "globex")
	g.save(a, "support", def{System: "acme's"}, true)
	g.save(b, "support", def{System: "globex's"}, true)
	g.save(b, "own", def{System: "b"}, true)
	if sys, _ := g.system(a, "support", 0); sys != "acme's" {
		t.Errorf("acme's support is %q", sys)
	}
	if _, err := g.r.Agent(a, "own", 0); !errors.Is(err, agentapi.ErrAgentNotFound) {
		t.Errorf("another tenant's agent: %v, want ErrAgentNotFound", err)
	}
	if _, err := g.r.Version(a, "own", 1); !errors.Is(err, agentapi.ErrAgentNotFound) {
		t.Errorf("another tenant's version: %v, want ErrAgentNotFound", err)
	}
	if _, err := g.r.Publish(a, "own", 1); !errors.Is(err, agentapi.ErrAgentNotFound) {
		t.Errorf("publishing another tenant's agent: %v, want ErrAgentNotFound", err)
	}
	if all, _ := g.r.Agents(a); len(all) != 1 {
		t.Errorf("acme lists %d agents, want 1", len(all))
	}
	if _, err := g.r.Agent(t.Context(), "support", 0); !errors.Is(err, tenancy.ErrNoTenant) {
		t.Errorf("no tenant: %v, want ErrNoTenant", err)
	}
}

// A published version that no longer builds (its toolset left the config)
// is left out of the list rather than failing it, and cannot be published
// again.
func TestPublishedButBroken(t *testing.T) {
	t.Parallel()
	g := newRig(t)
	ctx := ctxOf(t, "acme")
	g.save(ctx, "ok", def{System: "fine"}, true)
	// Stored behind the registry's back, as by an older server whose
	// config had the toolset.
	v, err := g.st.SaveItem(ctx, store.ItemVersion{Kind: registry.KindAgent, Name: "gone", Data: json.RawMessage(`{"name":"gone","system":"x","toolsets":["exa"]}`)}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.st.PublishItem(ctx, registry.KindAgent, "gone", v.Version); err != nil {
		t.Fatal(err)
	}
	all, err := g.r.Agents(ctx)
	if err != nil || len(all) != 1 || all[0].Name != "ok" {
		t.Errorf("Agents = %+v, %v; want only ok", all, err)
	}
	var de *agentapi.DefinitionError
	if _, err := g.r.Publish(ctx, "gone", 1); !errors.As(err, &de) {
		t.Errorf("publishing a version that does not build: %v, want a DefinitionError", err)
	}
	if _, err := g.r.Agent(ctx, "gone", 0); !errors.As(err, &de) {
		t.Errorf("running it: %v, want a DefinitionError", err)
	}
}

// The definition is stored with its name, keys sorted, and its text as
// written: a prompt keeps its < and &.
func TestDefinitionStoredNormalized(t *testing.T) {
	t.Parallel()
	g := newRig(t)
	ctx := ctxOf(t, "acme")
	v, err := g.r.Save(ctx, "k", "a", wire.SaveAgentRequest{Definition: json.RawMessage(`{ "toolsets": ["demo"], "system": "a < b & c" }`)})
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"name":"a","system":"a < b & c","toolsets":["demo"]}`; string(v.Definition) != want {
		t.Errorf("stored %s, want %s", v.Definition, want)
	}
	g.mu.Lock()
	g.saved[[2]string{"acme", "a"}] = map[int64]def{1: {Name: "a", System: "a < b & c", Toolsets: []string{"demo"}}}
	g.mu.Unlock()
	for _, bad := range []string{"", "a/b", "x y"} {
		if _, err := g.r.Save(ctx, "k", bad, wire.SaveAgentRequest{Definition: raw(t, def{System: "x"})}); !errors.Is(err, agentapi.ErrAgentInvalid) {
			t.Errorf("Save(%q): %v, want ErrAgentInvalid", bad, err)
		}
	}
}
