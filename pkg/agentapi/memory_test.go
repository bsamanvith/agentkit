package agentapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
	"github.com/bsamanvith/agentkit/pkg/auth"
	"github.com/bsamanvith/agentkit/pkg/auth/authtest"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

// fakeMemory is an agentapi.Memory over files kept by user (with scope
// user) or shared, under /memories, listed flat. Calls without a tenant
// fail.
type fakeMemory struct {
	scope string
	mu    sync.Mutex
	files map[string]map[string]string // owner -> path -> text
}

func newFakeMemory(scope string) *fakeMemory {
	return &fakeMemory{scope: scope, files: map[string]map[string]string{}}
}

func (m *fakeMemory) owner(ctx context.Context) string {
	if _, ok := tenancy.From(ctx); !ok {
		panic("memory call without a tenant")
	}
	if m.scope == wire.ScopeUser {
		u, _ := tenancy.User(ctx)
		return u
	}
	return ""
}

// put stores a file for user (with scope user).
func (m *fakeMemory) put(user, path, text string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	o := user
	if m.files[o] == nil {
		m.files[o] = map[string]string{}
	}
	m.files[o][path] = text
}

func (m *fakeMemory) Scope() string { return m.scope }

func (m *fakeMemory) View(ctx context.Context, path string) (wire.MemoryView, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !strings.HasPrefix(path, "/memories") || strings.Contains(path, "..") {
		return wire.MemoryView{}, fmt.Errorf("%w: %q", agentapi.ErrMemoryInvalid, path)
	}
	files := m.files[m.owner(ctx)]
	v := wire.MemoryView{Path: path, Limits: wire.MemoryLimits{FileBytes: 100, ScopeBytes: 1000, Files: 10}}
	for _, text := range files {
		v.Usage.Bytes += int64(len(text))
		v.Usage.Files++
	}
	if text, ok := files[path]; ok {
		v.Size, v.Text = int64(len(text)), text
		return v, nil
	}
	if path != "/memories" {
		return wire.MemoryView{}, fmt.Errorf("%w: %s", agentapi.ErrMemoryNotFound, path)
	}
	v.Dir = true
	for p, text := range files {
		v.Entries = append(v.Entries, wire.MemoryEntry{Path: p, Size: int64(len(text))})
		v.Size += int64(len(text))
	}
	slices.SortFunc(v.Entries, func(a, b wire.MemoryEntry) int { return strings.Compare(a.Path, b.Path) })
	return v, nil
}

func (m *fakeMemory) Delete(ctx context.Context, path string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !strings.HasPrefix(path, "/memories/") {
		return fmt.Errorf("%w: %q", agentapi.ErrMemoryInvalid, path)
	}
	files := m.files[m.owner(ctx)]
	if _, ok := files[path]; !ok {
		return fmt.Errorf("%w: %s", agentapi.ErrMemoryNotFound, path)
	}
	delete(files, path)
	return nil
}

// The keys of a memory rig, all of its tenant.
const (
	keyMemU1      = "memory-key-of-user-u1-0123"
	keyMemU2      = "memory-key-of-user-u2-0123"
	keyMemService = "memory-key-of-no-user-0123"
	keyMemReader  = "memory-key-that-reads-0123"
	keyMemAdmin   = "memory-key-of-an-admin-0123"
)

// memRig is a server with three agents: "default" keeps each user's own
// memory, "team" a tenant's shared one, and "plain" none.
type memRig struct {
	*rig
	mine, ours *fakeMemory
}

func newMemRig(t *testing.T) *memRig {
	t.Helper()
	tn, err := tenancy.NewTenant(agentapi.DefaultTenant)
	if err != nil {
		t.Fatal(err)
	}
	read, run := agentapi.ScopeRead, agentapi.ScopeRun
	all := []string{"*"}
	authn := authtest.NewFake(map[string]auth.Principal{
		keyMemU1:      {Tenant: tn, User: "u1", Scopes: []string{read, run}, Toolsets: all},
		keyMemU2:      {Tenant: tn, User: "u2", Scopes: []string{read, run}, Toolsets: all},
		keyMemService: {Tenant: tn, Scopes: []string{read, run}, Toolsets: all},
		keyMemReader:  {Tenant: tn, User: "u1", Scopes: []string{read}, Toolsets: all},
		keyMemAdmin:   {Tenant: tn, User: "admin", Scopes: []string{read, run, agentapi.ScopeAdmin}, Toolsets: all},
	})
	mr := &memRig{mine: newFakeMemory(wire.ScopeUser), ours: newFakeMemory(wire.ScopeTenant)}
	agents := []agentapi.Agent{
		{Name: agentapi.DefaultAgent, Harness: agentHarness(t, echo(t), registry(t))},
		{Name: "team", Harness: agentHarness(t, echo(t), registry(t))},
		{Name: "plain", Harness: agentHarness(t, echo(t), registry(t))},
	}
	mr.rig = newRig(t, echo(t), rigConfig{agents: agents, api: []agentapi.Option{
		agentapi.WithAuthenticator(authn),
		agentapi.WithMemory(map[string]agentapi.Memory{agentapi.DefaultAgent: mr.mine, "team": mr.ours}),
	}})
	mr.token = keyMemU1
	return mr
}

func (mr *memRig) view(path, key string) (int, wire.MemoryView) {
	mr.t.Helper()
	resp, body := mr.do("GET", path, "", "Authorization", "Bearer "+key)
	var v wire.MemoryView
	if resp.StatusCode == http.StatusOK {
		if err := json.Unmarshal(body, &v); err != nil {
			mr.t.Fatal(err)
		}
	}
	return resp.StatusCode, v
}

// TestMemoryEndpoints: a user views their own memory of an agent, a file's
// text and the listing with usage and limits, and deletes from it; another
// user of the tenant sees none of it; a bad path is 400, a missing one 404,
// and an agent without memory, or unknown, 404.
func TestMemoryEndpoints(t *testing.T) {
	t.Parallel()
	mr := newMemRig(t)
	mr.mine.put("u1", "/memories/prefs.md", "Units: metric\n")
	mr.mine.put("u1", "/memories/notes/trip.md", "Lisbon in May\n")

	code, v := mr.view("/v1/agents/default/memory", keyMemU1)
	if code != 200 || !v.Dir || v.Agent != "default" || v.Scope != "user" || v.V != agentapi.WireVersion || len(v.Entries) != 2 ||
		v.Usage != (wire.MemoryUsage{Bytes: 28, Files: 2}) || v.Limits != (wire.MemoryLimits{FileBytes: 100, ScopeBytes: 1000, Files: 10}) {
		t.Errorf("u1's listing = %d %+v", code, v)
	}
	if code, v := mr.view("/v1/agents/default/memory?path=/memories/prefs.md", keyMemU1); code != 200 || v.Dir || v.Text != "Units: metric\n" || v.Size != 14 {
		t.Errorf("u1's file = %d %+v", code, v)
	}
	if code, v := mr.view("/v1/agents/default/memory", keyMemU2); code != 200 || len(v.Entries) != 0 || v.Usage.Files != 0 {
		t.Errorf("u2's listing = %d %+v, want empty", code, v)
	}
	if code, _ := mr.view("/v1/agents/default/memory?path=/memories/prefs.md", keyMemU2); code != 404 {
		t.Errorf("u2's view of u1's file = %d, want 404", code)
	}
	if resp, _ := mr.do("DELETE", "/v1/agents/default/memory?path=/memories/prefs.md", "", "Authorization", "Bearer "+keyMemU2); resp.StatusCode != 404 {
		t.Errorf("u2 deleting u1's file = %d, want 404", resp.StatusCode)
	}
	if resp, body := mr.do("DELETE", "/v1/agents/default/memory?path=/memories/prefs.md", ""); resp.StatusCode != 204 {
		t.Errorf("u1 deleting their file = %d %s", resp.StatusCode, body)
	}
	if code, v := mr.view("/v1/agents/default/memory", keyMemU1); code != 200 || len(v.Entries) != 1 || v.Entries[0].Path != "/memories/notes/trip.md" {
		t.Errorf("u1's listing after the delete = %d %+v", code, v)
	}
	for path, want := range map[string]int{
		"/v1/agents/default/memory?path=/etc/passwd":       400,
		"/v1/agents/default/memory?path=/memories/nope.md": 404,
		"/v1/agents/plain/memory":                          404,
		"/v1/agents/nope/memory":                           404,
	} {
		if code, _ := mr.view(path, keyMemU1); code != want {
			t.Errorf("GET %s = %d, want %d", path, code, want)
		}
	}
}

// TestMemoryEndpointScopes: viewing needs agent:read, deleting agent:run;
// a user's own memory needs a credential that is a user's; deleting from a
// tenant's shared memory needs agent:admin, which viewing it does not.
func TestMemoryEndpointScopes(t *testing.T) {
	t.Parallel()
	mr := newMemRig(t)
	mr.mine.put("u1", "/memories/a.md", "a")
	mr.ours.put("", "/memories/team.md", "shared")
	mr.ours.put("", "/memories/old.md", "stale")
	for _, c := range []struct {
		method, path, key string
		want              int
	}{
		{"GET", "/v1/agents/default/memory", keyMemReader, 200},
		{"DELETE", "/v1/agents/default/memory?path=/memories/a.md", keyMemReader, 403},
		{"GET", "/v1/agents/default/memory", keyMemService, 403},
		{"DELETE", "/v1/agents/default/memory?path=/memories/a.md", keyMemService, 403},
		{"GET", "/v1/agents/team/memory?path=/memories/team.md", keyMemService, 200},
		{"GET", "/v1/agents/team/memory", keyMemU2, 200},
		{"DELETE", "/v1/agents/team/memory?path=/memories/team.md", keyMemU1, 403},
		{"DELETE", "/v1/agents/team/memory?path=/memories/old.md", keyMemAdmin, 204},
	} {
		if resp, body := mr.do(c.method, c.path, "", "Authorization", "Bearer "+c.key); resp.StatusCode != c.want {
			t.Errorf("%s %s with %s = %d %s, want %d", c.method, c.path, c.key, resp.StatusCode, body, c.want)
		}
	}
	if code, v := mr.view("/v1/agents/team/memory", keyMemU1); code != 200 || v.Scope != "tenant" || len(v.Entries) != 1 || v.Entries[0].Path != "/memories/team.md" {
		t.Errorf("the team's memory after the deletes = %d %+v, want team.md only", code, v)
	}
}
