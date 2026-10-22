package agentapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
	"github.com/bsamanvith/agentkit/pkg/auth"
	"github.com/bsamanvith/agentkit/pkg/auth/authtest"
	"github.com/bsamanvith/agentkit/pkg/clock/clocktest"
	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/llm/llmtest"
	agentregistry "github.com/bsamanvith/agentkit/pkg/registry"
	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

// The keys of registryRig: an admin of tenant A, an admin of tenant B, a
// key of A that runs but does not administer, and an admin of A limited to
// toolset a.
const (
	keyAdminA = "key-admin-a-0123456789"
	keyAdminB = "key-admin-b-0123456789"
	keyRunA   = "key-run-a-012345678901"
	keyAdminT = "key-admin-a-toolset-a1"
)

// testDefinition is the definition the test builder knows: a toy of
// serve's, with its checks (toolsets the server has, tool names that clash,
// the model's provider).
type testDefinition struct {
	Name            string   `json:"name"`
	System          string   `json:"system"`
	Toolsets        []string `json:"toolsets"`
	PrefixToolNames bool     `json:"prefix_tool_names"`
	Model           struct {
		Provider string `json:"provider"`
	} `json:"model"`
}

// registryRig serves the config agent "default" and the agents of a
// registry over the rig's store, built on one model that answers with the
// system prompt it was sent, so a run's answer says which version ran.
func registryRig(t *testing.T) *rig {
	t.Helper()
	ten := func(name string) tenancy.Tenant {
		tn, err := tenancy.NewTenant(fmt.Sprintf("%s%d", name, time.Now().UnixNano()))
		if err != nil {
			t.Fatal(err)
		}
		return tn
	}
	a, b := ten("a"), ten("b")
	all := []string{"*"}
	authn := authtest.NewFake(map[string]auth.Principal{
		keyAdminA: {Tenant: a, Scopes: all, Toolsets: all, Via: auth.Credential{ID: "admin-a"}},
		keyAdminB: {Tenant: b, Scopes: all, Toolsets: all},
		keyRunA:   {Tenant: a, Scopes: []string{agentapi.ScopeRead, agentapi.ScopeRun}, Toolsets: all},
		keyAdminT: {Tenant: a, Scopes: all, Toolsets: []string{"a"}},
	})
	m := newModel(t, func(req llm.Request) llmtest.Reply {
		return llmtest.Text(req.System + " | " + req.Messages[len(req.Messages)-1].Text())
	})
	m.gate = make(chan struct{}, 16)
	sets := demoTools(t)
	clk := clocktest.NewFake(time.Time{})
	st, _ := testStore(t, clk)
	build := func(ctx context.Context, name string, raw json.RawMessage) (agentapi.Agent, error) {
		var d testDefinition
		if err := decodeStrict(raw, &d); err != nil {
			return agentapi.Agent{}, &agentapi.DefinitionError{Fields: []wire.FieldError{{Message: err.Error()}}}
		}
		var fields []wire.FieldError
		if d.Model.Provider != "" && d.Model.Provider != "fake" {
			fields = append(fields, wire.FieldError{Field: "model.provider", Message: fmt.Sprintf("%q: want fake", d.Model.Provider)})
		}
		used := map[string]tool.Registry{}
		for _, ts := range d.Toolsets {
			if sets[ts] == nil {
				fields = append(fields, wire.FieldError{Field: "toolsets", Message: fmt.Sprintf("names %q, which the server does not have", ts)})
				continue
			}
			used[ts] = sets[ts]
		}
		if len(fields) > 0 {
			return agentapi.Agent{}, &agentapi.DefinitionError{Fields: fields}
		}
		tools, err := agentapi.MergeTools(ctx, used, d.PrefixToolNames)
		if err != nil {
			return agentapi.Agent{}, &agentapi.DefinitionError{Fields: []wire.FieldError{{Field: "toolsets", Message: err.Error()}}}
		}
		h, err := harness.New(m, tools, harness.WithSystem(d.System), harness.WithClock(clk))
		if err != nil {
			return agentapi.Agent{}, err
		}
		return agentapi.Agent{Name: name, System: d.System, Toolsets: d.Toolsets, PrefixToolNames: d.PrefixToolNames, Harness: h}, nil
	}
	reg, err := agentregistry.New(st.(store.ItemStore), build, agentregistry.WithReadOnly(agentapi.DefaultAgent))
	if err != nil {
		t.Fatal(err)
	}
	agents := []agentapi.Agent{{Name: agentapi.DefaultAgent, System: "config", Harness: agentHarness(t, m, registry(t), harness.WithSystem("config"), harness.WithClock(clk))}}
	r := newRig(t, m, rigConfig{agents: agents, clock: clk, store: st, api: []agentapi.Option{
		agentapi.WithTenant(nil), // the principal's
		agentapi.WithAuthenticator(authn),
		agentapi.WithToolsets(
			agentapi.Toolset{Name: "a", Type: "demo", Tools: sets["a"]},
			agentapi.Toolset{Name: "b", Type: "demo", Tools: sets["b"]},
		),
		agentapi.WithAgentRegistry(reg),
	}})
	r.models, r.token = nil, keyAdminA
	return r
}

func decodeStrict(raw []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

// save posts a version of name and returns it, failing the test unless it
// is created.
func (r *rig) save(name, definition string, publish bool) wire.AgentVersion {
	r.t.Helper()
	body, _ := json.Marshal(wire.SaveAgentRequest{Definition: json.RawMessage(definition), Message: "edit", Publish: publish})
	resp, got := r.do("POST", "/v1/agents/"+name+"/versions", string(body))
	var v wire.AgentVersion
	if resp.StatusCode != http.StatusCreated || json.Unmarshal(got, &v) != nil {
		r.t.Fatalf("saving %s = %d %s, want 201", name, resp.StatusCode, got)
	}
	if want := fmt.Sprintf("/v1/agents/%s/versions/%d", name, v.Version); resp.Header.Get("Location") != want {
		r.t.Errorf("Location %q, want %q", resp.Header.Get("Location"), want)
	}
	return v
}

func (r *rig) publish(name string, version int64) wire.AgentVersionList {
	r.t.Helper()
	resp, got := r.do("POST", "/v1/agents/"+name+"/publish", fmt.Sprintf(`{"version": %d}`, version))
	var l wire.AgentVersionList
	if resp.StatusCode != http.StatusOK || json.Unmarshal(got, &l) != nil || l.Published != version {
		r.t.Fatalf("publishing %s %d = %d %s", name, version, resp.StatusCode, got)
	}
	return l
}

// runAgent runs a new conversation of agent to the end, as JSON.
func (r *rig) runAgent(agent, message, conversationID string) agentapi.Run {
	r.t.Helper()
	r.model.gate <- struct{}{}
	body, _ := json.Marshal(agentapi.RunRequest{Message: message, Agent: agent, ConversationID: conversationID})
	resp, got := r.do("POST", "/v1/runs", string(body), "Accept", "application/json")
	var run agentapi.Run
	if resp.StatusCode != http.StatusOK || json.Unmarshal(got, &run) != nil {
		r.t.Fatalf("running %s = %d %s", agent, resp.StatusCode, got)
	}
	return run
}

// Saving an agent creates version 1, editing it version 2; publishing 2
// makes new runs use it while a run started on 1 still reports
// agent_version 1, and goes on with version 1 when it is resumed after the
// publication; publishing 1 again rolls back.
func TestRegistryPublishAndRollBack(t *testing.T) {
	t.Parallel()
	r := registryRig(t)
	if v := r.save("support", `{"system": "v1", "toolsets": ["a"]}`, true); v.Version != 1 || !v.Published || v.Author != "admin-a" {
		t.Errorf("first save = %+v, want version 1, published, by admin-a", v)
	}
	first := r.runAgent("support", "hi", "")
	if first.AgentVersion != 1 || first.Answer != "v1 | hi" {
		t.Errorf("run on version 1 = version %d, %q", first.AgentVersion, first.Answer)
	}

	// A run started on 1, stopped while it waits for the model.
	held := r.startAgent(t.Context(), "held", "", "support")
	held.until(agentapi.TypeRunStart, 0)
	if resp, body := r.do("POST", "/v1/runs/"+held.runID()+"/stop", ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("stop = %d %s", resp.StatusCode, body)
	}
	held.all()

	if v := r.save("support", `{"system": "v2", "toolsets": ["a"]}`, false); v.Version != 2 || v.Published {
		t.Errorf("second save = %+v, want version 2, a draft", v)
	}
	if run := r.runAgent("support", "draft?", ""); run.AgentVersion != 1 || run.Answer != "v1 | draft?" {
		t.Errorf("a run while 2 is a draft = version %d, %q; want 1", run.AgentVersion, run.Answer)
	}
	r.publish("support", 2)
	second := r.runAgent("support", "hi", "")
	if second.AgentVersion != 2 || second.Answer != "v2 | hi" {
		t.Errorf("run after publishing 2 = version %d, %q", second.AgentVersion, second.Answer)
	}
	if got := r.getRun(first.ID); got.AgentVersion != 1 {
		t.Errorf("GET of the run started on 1 = agent_version %d, want 1", got.AgentVersion)
	}
	// The held run resumes on the version it started on.
	r.model.gate <- struct{}{}
	resumed := r.open(r.postPath(t.Context(), "/v1/runs/"+held.runID()+"/resume", ""))
	resumed.all()
	if e := resumed.last(); e.Type != agentapi.TypeRunDone || e.Answer != "v1 | held" {
		t.Errorf("resumed run ended %s, want answered by version 1", jsonOf(e))
	}
	if got := r.getRun(held.runID()); got.AgentVersion != 1 {
		t.Errorf("the resumed run reports agent_version %d, want 1", got.AgentVersion)
	}
	var ag wire.Agent
	if r.get("/v1/agents/support", &ag); ag.Version != 2 || ag.System != "v2" || ag.ReadOnly {
		t.Errorf("GET /v1/agents/support = %+v, want version 2", ag)
	}

	// Rolling back is publishing 1 again; the conversation of a run on 2
	// goes on with 1, since new runs use the published version.
	r.publish("support", 1)
	if run := r.runAgent("support", "again", ""); run.AgentVersion != 1 || run.Answer != "v1 | again" {
		t.Errorf("run after the rollback = version %d, %q", run.AgentVersion, run.Answer)
	}
	if run := r.runAgent("", "more", second.ConversationID); run.AgentVersion != 1 || !strings.HasPrefix(run.Answer, "v1 | ") {
		t.Errorf("continuing a conversation started on 2 = version %d, %q; want 1", run.AgentVersion, run.Answer)
	}
	var l wire.AgentVersionList
	if r.get("/v1/agents/support/versions", &l); l.Published != 1 || l.Latest != 2 || len(l.Versions) != 2 || !l.Versions[0].Published || l.Versions[1].Published || l.Versions[1].PublishedAt == nil {
		t.Errorf("versions = %+v, want 1 published and 2 published before", l)
	}
	var one wire.AgentVersion
	if r.get("/v1/agents/support/versions/1", &one); one.Version != 1 || !bytes.Contains(one.Definition, []byte(`"system":"v1"`)) {
		t.Errorf("GET version 1 = %+v", one)
	}
}

// An invalid definition (an unknown toolset, a tool name clash, a bad
// model) is refused with field-level errors and changes nothing.
func TestRegistryRefusesInvalidDefinitions(t *testing.T) {
	t.Parallel()
	r := registryRig(t)
	r.save("support", `{"system": "ok"}`, true)
	for name, c := range map[string]struct {
		definition string
		fields     []string
	}{
		"unknown toolset": {`{"system": "x", "toolsets": ["exa"]}`, []string{"toolsets"}},
		"tool name clash": {`{"system": "x", "toolsets": ["a", "b"]}`, []string{"toolsets"}},
		"bad model":       {`{"system": "x", "model": {"provider": "acme"}}`, []string{"model.provider"}},
		"two at once":     {`{"system": "x", "toolsets": ["exa"], "model": {"provider": "acme"}}`, []string{"model.provider", "toolsets"}},
		"another name":    {`{"name": "other", "system": "x"}`, []string{"name"}},
	} {
		for _, agent := range []string{"support", "fresh"} {
			body, _ := json.Marshal(wire.SaveAgentRequest{Definition: json.RawMessage(c.definition), Publish: true})
			resp, got := r.do("POST", "/v1/agents/"+agent+"/versions", string(body))
			var e agentapi.ErrorResponse
			if resp.StatusCode != http.StatusBadRequest || json.Unmarshal(got, &e) != nil || e.Error.Code != agentapi.CodeInvalidDefinition {
				t.Errorf("%s (%s) = %d %s, want 400 invalid_definition", name, agent, resp.StatusCode, got)
				continue
			}
			var fields []string
			for _, f := range e.Error.Fields {
				fields = append(fields, f.Field)
			}
			slices.Sort(fields)
			if !slices.Equal(fields, c.fields) {
				t.Errorf("%s (%s): fields %v, want %v", name, agent, fields, c.fields)
			}
		}
	}
	var l wire.AgentVersionList
	if r.get("/v1/agents/support/versions", &l); l.Latest != 1 || l.Published != 1 || len(l.Versions) != 1 {
		t.Errorf("after the refused saves, versions = %+v; want version 1 only", l)
	}
	if code := r.get("/v1/agents/fresh/versions", &l); code != http.StatusNotFound {
		t.Errorf("GET versions of an agent never created = %d, want 404", code)
	}
	if run := r.runAgent("support", "still", ""); run.AgentVersion != 1 || run.Answer != "ok | still" {
		t.Errorf("run after the refused saves = version %d, %q", run.AgentVersion, run.Answer)
	}
}

// The config file's agents are listed as read-only; changing one through
// the API is refused with read_only.
func TestRegistryConfigAgentsAreReadOnly(t *testing.T) {
	t.Parallel()
	r := registryRig(t)
	r.save("support", `{"system": "stored"}`, true)
	var list wire.AgentList
	r.get("/v1/agents", &list)
	byName := map[string]wire.Agent{}
	for _, a := range list.Agents {
		byName[a.Name] = a
	}
	if d := byName["default"]; !d.ReadOnly || d.Version != 0 || !d.Default {
		t.Errorf("the config agent is listed as %+v, want read-only, the default, with no version", d)
	}
	if s := byName["support"]; s.ReadOnly || s.Version != 1 {
		t.Errorf("the stored agent is listed as %+v, want version 1, not read-only", s)
	}
	for _, c := range []struct{ method, path, body string }{
		{"POST", "/v1/agents/default/versions", `{"definition": {"system": "mine"}}`},
		{"POST", "/v1/agents/default/publish", `{"version": 1}`},
		{"DELETE", "/v1/agents/default/versions/1", ""},
	} {
		resp, body := r.do(c.method, c.path, c.body)
		if resp.StatusCode != http.StatusConflict || apiCode(body) != agentapi.CodeReadOnly {
			t.Errorf("%s %s = %d %s, want 409 read_only", c.method, c.path, resp.StatusCode, body)
		}
	}
	var l wire.AgentVersionList
	if code := r.get("/v1/agents/default/versions", &l); code != http.StatusOK || !l.ReadOnly || len(l.Versions) != 0 {
		t.Errorf("GET /v1/agents/default/versions = %d %+v, want read-only with no versions", code, l)
	}
	if run := r.runAgent("default", "hi", ""); run.AgentVersion != 0 || run.Answer != "config | hi" {
		t.Errorf("a run of the config agent = version %d, %q; want no version", run.AgentVersion, run.Answer)
	}
}

// Each tenant has its own agents, and the registry's endpoints need
// agent:admin: a key that only runs may run a stored agent but not change
// or read its versions, and an admin limited to some toolsets defines only
// agents of those.
func TestRegistryTenantsAndScope(t *testing.T) {
	t.Parallel()
	r := registryRig(t)
	run := r.save("support", `{"system": "a's"}`, true)
	// Tenant B sees none of A's agent, and has its own of the same name.
	for _, path := range []string{"/v1/agents/support", "/v1/agents/support/versions", "/v1/agents/support/versions/1"} {
		if resp, body := r.do("GET", path, "", as(keyAdminB)...); resp.StatusCode != http.StatusNotFound {
			t.Errorf("B: GET %s = %d %s, want 404", path, resp.StatusCode, body)
		}
	}
	if resp, body := r.do("POST", "/v1/runs", `{"message": "hi", "agent": "support"}`, asJSON(keyAdminB)...); resp.StatusCode != http.StatusNotFound {
		t.Errorf("B running A's agent = %d %s, want 404", resp.StatusCode, body)
	}
	if resp, body := r.do("POST", "/v1/agents/support/publish", `{"version": 1}`, as(keyAdminB)...); resp.StatusCode != http.StatusNotFound {
		t.Errorf("B publishing A's agent = %d %s, want 404", resp.StatusCode, body)
	}
	if resp, body := r.do("POST", "/v1/agents/support/versions", `{"definition": {"system": "b's"}, "publish": true}`, as(keyAdminB)...); resp.StatusCode != http.StatusCreated {
		t.Errorf("B creating its own support = %d %s, want 201", resp.StatusCode, body)
	}
	var list wire.AgentList
	r.get("/v1/agents", &list)
	if n := len(list.Agents); n != 2 {
		t.Errorf("A lists %d agents, want 2 (default and its support)", n)
	}
	if ag := r.runAgent("support", "whose", ""); ag.Answer != "a's | whose" || ag.AgentVersion != run.Version {
		t.Errorf("A's run of support = %q version %d", ag.Answer, ag.AgentVersion)
	}

	// Scopes: a key that runs but does not administer.
	r.model.gate <- struct{}{}
	if resp, body := r.do("POST", "/v1/runs", `{"message": "hi", "agent": "support"}`, asJSON(keyRunA)...); resp.StatusCode != http.StatusOK {
		t.Errorf("a run key running the stored agent = %d %s, want 200", resp.StatusCode, body)
	}
	for _, c := range []struct{ method, path, body string }{
		{"POST", "/v1/agents/support/versions", `{"definition": {"system": "x"}}`},
		{"POST", "/v1/agents/support/publish", `{"version": 1}`},
		{"GET", "/v1/agents/support/versions", ""},
		{"GET", "/v1/agents/support/versions/1", ""},
		{"DELETE", "/v1/agents/support/versions/1", ""},
	} {
		resp, body := r.do(c.method, c.path, c.body, as(keyRunA)...)
		if resp.StatusCode != http.StatusForbidden || apiCode(body) != agentapi.CodeForbidden {
			t.Errorf("a run key: %s %s = %d %s, want 403", c.method, c.path, resp.StatusCode, body)
		}
	}
	// An admin limited to toolset a defines no agent of toolset b.
	if resp, body := r.do("POST", "/v1/agents/narrow/versions", `{"definition": {"system": "x", "toolsets": ["b"]}}`, as(keyAdminT)...); resp.StatusCode != http.StatusForbidden {
		t.Errorf("an admin of toolset a saving an agent of b = %d %s, want 403", resp.StatusCode, body)
	}
	if resp, body := r.do("POST", "/v1/agents/narrow/versions", `{"definition": {"system": "x", "toolsets": ["a"]}}`, as(keyAdminT)...); resp.StatusCode != http.StatusCreated {
		t.Errorf("an admin of toolset a saving an agent of a = %d %s, want 201", resp.StatusCode, body)
	}
}

// Drafts are deleted; a published version is kept (409 version_published).
func TestRegistryDeleteDraft(t *testing.T) {
	t.Parallel()
	r := registryRig(t)
	r.save("support", `{"system": "1"}`, true)
	r.save("support", `{"system": "2"}`, false)
	if resp, body := r.do("DELETE", "/v1/agents/support/versions/1", ""); resp.StatusCode != http.StatusConflict || apiCode(body) != agentapi.CodeVersionPublished {
		t.Errorf("deleting the published version = %d %s, want 409 version_published", resp.StatusCode, body)
	}
	if resp, body := r.do("DELETE", "/v1/agents/support/versions/2", ""); resp.StatusCode != http.StatusNoContent || len(body) != 0 {
		t.Errorf("deleting the draft = %d %s, want 204", resp.StatusCode, body)
	}
	if resp, body := r.do("GET", "/v1/agents/support/versions/2", ""); resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET of the deleted draft = %d %s, want 404", resp.StatusCode, body)
	}
	for _, path := range []string{"/v1/agents/support/versions/0", "/v1/agents/support/versions/x"} {
		if resp, body := r.do("GET", path, ""); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("GET %s = %d %s, want 400", path, resp.StatusCode, body)
		}
	}
}
