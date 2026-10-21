package agentapi_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
	"github.com/bsamanvith/agentkit/pkg/auth"
	"github.com/bsamanvith/agentkit/pkg/auth/authtest"
	"github.com/bsamanvith/agentkit/pkg/store/scoped"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

// The keys of accessRig: tenant A with everything, tenant B with everything,
// tenant A reading only, and tenant A with toolset a only.
const (
	keyA     = "key-tenant-a-0123456789"
	keyB     = "key-tenant-b-0123456789"
	keyRead  = "key-a-read-only-0123456"
	keyOnlyA = "key-a-toolset-a-only-01"
)

// syncBuffer is a log the server writes from several goroutines.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// accessRig serves two agents, "both" (toolsets a and b) and "solo" (a),
// behind the four keys, each acting for its principal's tenant. Its log is
// kept, at debug level.
func accessRig(t *testing.T) (*rig, *syncBuffer) {
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
		keyA:     {Tenant: a, User: "ann", Scopes: all, Toolsets: all},
		keyB:     {Tenant: b, Scopes: all, Toolsets: all},
		keyRead:  {Tenant: a, Scopes: []string{agentapi.ScopeRead}, Toolsets: all},
		keyOnlyA: {Tenant: a, Scopes: all, Toolsets: []string{"a"}},
	})
	sets := demoTools(t)
	m := echo(t)
	agents := []agentapi.Agent{
		{Name: "both", Toolsets: []string{"a", "b"}, Harness: agentHarness(t, m, registry(t))},
		{Name: "solo", Toolsets: []string{"a"}, Harness: agentHarness(t, m, registry(t))},
	}
	log := &syncBuffer{}
	r := newRig(t, m, rigConfig{agents: agents, api: []agentapi.Option{
		agentapi.WithTenant(nil), // the principal's
		agentapi.WithAuthenticator(authn),
		agentapi.WithToolsets(
			agentapi.Toolset{Name: "a", Type: "demo", Tools: sets["a"]},
			agentapi.Toolset{Name: "b", Type: "demo", Tools: sets["b"]},
		),
		agentapi.WithLogger(slog.New(slog.NewJSONHandler(log, &slog.HandlerOptions{Level: slog.LevelDebug}))),
	}})
	// Runs here are posted raw: there is no transcript to check. The rig's
	// own requests (its checks) are A's.
	r.models, r.token = nil, keyA
	return r, log
}

func as(key string) []string { return []string{"Authorization", "Bearer " + key} }

func asJSON(key string) []string {
	return append(as(key), "Accept", "application/json")
}

// apiCode is the error code of an error body, "" for another body.
func apiCode(body []byte) string {
	var e wire.ErrorResponse
	if json.Unmarshal(body, &e) != nil || e.Error == nil {
		return ""
	}
	return e.Error.Code
}

// Every endpoint, and paths that are none, answer 401 in the API's error
// format to a request with no credentials or wrong ones.
func TestEveryPathNeedsCredentials(t *testing.T) {
	t.Parallel()
	r, _ := accessRig(t)
	r.token = "" // no credentials unless a case sends some
	t.Cleanup(func() { r.token = keyA })
	paths := []string{"GET /v1/nope", "POST /v1/", "GET /v1/agents/../toolsets"}
	for _, rt := range r.srv.Routes() {
		paths = append(paths, strings.NewReplacer("{id}", "x", "{name}", "x").Replace(rt))
	}
	for _, p := range paths {
		method, path, _ := strings.Cut(p, " ")
		for _, cred := range [][]string{nil, as("not-a-key-0123456789"), {"Authorization", "Basic eDp5"}} {
			resp, body := r.do(method, path, `{}`, cred...)
			if resp.StatusCode != http.StatusUnauthorized || apiCode(body) != agentapi.CodeUnauthorized || !strings.HasPrefix(resp.Header.Get("WWW-Authenticate"), "Bearer") {
				t.Errorf("%s with %q = %d %s, want 401 unauthorized with a Bearer challenge", p, cred, resp.StatusCode, body)
			}
		}
	}
}

// A tenant's runs and conversations do not exist for another tenant.
func TestTenantsDoNotSeeEachOther(t *testing.T) {
	t.Parallel()
	r, _ := accessRig(t)
	resp, body := r.do("POST", "/v1/runs", `{"message":"hi","agent":"solo"}`, asJSON(keyA)...)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("A's run: %d %s", resp.StatusCode, body)
	}
	var run agentapi.Run
	if err := json.Unmarshal(body, &run); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		"/v1/runs/" + run.ID, "/v1/runs/" + run.ID + "/events",
		"/v1/conversations/" + run.ConversationID, "/v1/runs?conversation_id=" + run.ConversationID,
	} {
		if resp, _ := r.do("GET", path, "", as(keyA)...); resp.StatusCode != http.StatusOK {
			t.Errorf("A: GET %s = %d, want 200", path, resp.StatusCode)
		}
		if resp, body := r.do("GET", path, "", as(keyB)...); resp.StatusCode != http.StatusNotFound {
			t.Errorf("B: GET %s = %d %s, want 404", path, resp.StatusCode, body)
		}
	}
	if resp, body := r.do("POST", "/v1/runs", `{"message":"mine now","conversation_id":"`+run.ConversationID+`"}`, asJSON(keyB)...); resp.StatusCode != http.StatusNotFound {
		t.Errorf("B continuing A's conversation = %d %s, want 404", resp.StatusCode, body)
	}
	if resp, body := r.do("POST", "/v1/runs/"+run.ID+"/input", `{"answers":[{"id":"x","approved":true}]}`, asJSON(keyB)...); resp.StatusCode != http.StatusNotFound {
		t.Errorf("B answering A's run = %d %s, want 404", resp.StatusCode, body)
	}
	for key, want := range map[string]int{keyA: 1, keyB: 0} {
		_, body := r.do("GET", "/v1/conversations", "", as(key)...)
		var list wire.ConversationList
		if err := json.Unmarshal(body, &list); err != nil || len(list.Conversations) != want {
			t.Errorf("GET /v1/conversations as %s = %s, want %d conversations", key[:12], body, want)
		}
	}
}

// A key without agent:run reads, but starts, continues, stops and deletes
// nothing: 403 forbidden.
func TestScopes(t *testing.T) {
	t.Parallel()
	r, _ := accessRig(t)
	_, body := r.do("POST", "/v1/runs", `{"message":"hi","agent":"solo"}`, asJSON(keyA)...)
	var run agentapi.Run
	if err := json.Unmarshal(body, &run); err != nil {
		t.Fatal(err)
	}
	for _, rt := range r.srv.Routes() {
		method, _, _ := strings.Cut(rt, " ")
		path := strings.NewReplacer("{id}", run.ID, "{name}", "solo", method+" ", "").Replace(rt)
		if strings.HasPrefix(rt, "DELETE /v1/conversations/") {
			path = "/v1/conversations/" + run.ConversationID
		}
		resp, body := r.do(method, path, `{"message":"x"}`, as(keyRead)...)
		admin := strings.HasSuffix(rt, "/review") // agent:admin, even to read
		switch {
		case method == "GET" && !admin && resp.StatusCode == http.StatusForbidden:
			t.Errorf("read-only %s = 403, want it served", rt)
		case (method != "GET" || admin) && (resp.StatusCode != http.StatusForbidden || apiCode(body) != agentapi.CodeForbidden):
			t.Errorf("read-only %s = %d %s, want 403 forbidden", rt, resp.StatusCode, body)
		}
	}
	if resp, _ := r.do("GET", "/v1/runs/"+run.ID, "", as(keyRead)...); resp.StatusCode != http.StatusOK {
		t.Errorf("read-only GET of its tenant's run = %d, want 200", resp.StatusCode)
	}
}

// A key limited to toolset a is shown only a, and only the agents whose
// toolsets it may all use; the others answer as if they did not exist.
func TestToolsetsOfAKey(t *testing.T) {
	t.Parallel()
	r, _ := accessRig(t)
	names := func(path, key, field string) []string {
		_, body := r.do("GET", path, "", as(key)...)
		var v map[string]json.RawMessage
		var list []struct{ Name string }
		if err := json.Unmarshal(body, &v); err != nil || json.Unmarshal(v[field], &list) != nil {
			t.Fatalf("GET %s: %s", path, body)
		}
		var out []string
		for _, x := range list {
			out = append(out, x.Name)
		}
		return out
	}
	if got := names("/v1/toolsets", keyOnlyA, "toolsets"); !slices.Equal(got, []string{"a"}) {
		t.Errorf("toolsets = %v, want [a]", got)
	}
	if got := names("/v1/agents", keyOnlyA, "agents"); !slices.Equal(got, []string{"solo"}) {
		t.Errorf("agents = %v, want [solo]", got)
	}
	if got := names("/v1/agents", keyA, "agents"); !slices.Equal(got, []string{"both", "solo"}) {
		t.Errorf("agents of a key with every toolset = %v", got)
	}
	same := func(what, hidden, unknown string, do func(string) (*http.Response, []byte)) {
		t.Helper()
		hr, hb := do(hidden)
		ur, ub := do(unknown)
		hm := strings.ReplaceAll(string(hb), hidden, "NAME")
		um := strings.ReplaceAll(string(ub), unknown, "NAME")
		if hr.StatusCode != http.StatusNotFound || hr.StatusCode != ur.StatusCode || hm != um {
			t.Errorf("%s: hidden %q = %d %s, unknown %q = %d %s: want the same 404", what, hidden, hr.StatusCode, hb, unknown, ur.StatusCode, ub)
		}
	}
	same("tools", "b", "zz", func(n string) (*http.Response, []byte) {
		return r.do("GET", "/v1/toolsets/"+n+"/tools", "", as(keyOnlyA)...)
	})
	same("agent", "both", "zz", func(n string) (*http.Response, []byte) {
		return r.do("GET", "/v1/agents/"+n, "", as(keyOnlyA)...)
	})
	same("run", "both", "zz", func(n string) (*http.Response, []byte) {
		return r.do("POST", "/v1/runs", `{"message":"hi","agent":"`+n+`"}`, asJSON(keyOnlyA)...)
	})
	same("conversation", "both", "zz", func(n string) (*http.Response, []byte) {
		return r.do("POST", "/v1/conversations", `{"agent":"`+n+`"}`, as(keyOnlyA)...)
	})

	// A conversation of "both", started with the full key, cannot be
	// continued with the narrow one, of the same tenant.
	_, body := r.do("POST", "/v1/runs", `{"message":"hi","agent":"both"}`, asJSON(keyA)...)
	var run agentapi.Run
	if err := json.Unmarshal(body, &run); err != nil {
		t.Fatal(err)
	}
	if resp, body := r.do("POST", "/v1/runs", `{"message":"more","conversation_id":"`+run.ConversationID+`"}`, asJSON(keyOnlyA)...); resp.StatusCode != http.StatusNotFound {
		t.Errorf("continuing a conversation of a hidden agent = %d %s, want 404", resp.StatusCode, body)
	}
	if resp, _ := r.do("POST", "/v1/runs", `{"message":"ok","agent":"solo"}`, asJSON(keyOnlyA)...); resp.StatusCode != http.StatusOK {
		t.Errorf("a run of solo with the narrow key = %d, want 200", resp.StatusCode)
	}
}

// The keys never reach a response or the log, debug level included.
func TestKeysAreNeverEchoed(t *testing.T) {
	t.Parallel()
	r, log := accessRig(t)
	var seen []string
	for _, key := range []string{keyA, keyB, keyRead, keyOnlyA, "wrong-key-0123456789abc"} {
		for _, req := range [][3]string{{"POST", "/v1/runs", `{"message":"hi","agent":"solo"}`}, {"GET", "/v1/agents", ""}, {"GET", "/v1/nope", ""}, {"DELETE", "/v1/conversations/x", ""}} {
			resp, body := r.do(req[0], req[1], req[2], asJSON(key)...)
			var h bytes.Buffer
			_ = resp.Header.Write(&h)
			seen = append(seen, string(body), h.String())
		}
	}
	seen = append(seen, log.String())
	for _, key := range []string{keyA, keyB, keyRead, keyOnlyA, "wrong-key-0123456789abc"} {
		for _, s := range seen {
			if authtest.Leaks(s, key) {
				t.Fatalf("key %s... appears in %q", key[:8], s)
			}
		}
	}
}

// WithToken and WithAuthenticator together are a mistake.
func TestTokenOrAuthenticator(t *testing.T) {
	t.Parallel()
	_, err := agentapi.New(agentHarness(t, echo(t), registry(t)),
		agentapi.WithStore(scoped.Memory(nil)), agentapi.WithToken(testToken), agentapi.WithAuthenticator(authtest.NewFake(nil)))
	if !errors.Is(err, agentapi.ErrInvalidConfig) {
		t.Errorf("New = %v, want ErrInvalidConfig", err)
	}
}
