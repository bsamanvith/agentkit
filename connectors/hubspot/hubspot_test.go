package hubspot_test

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/bsamanvith/agentkit/connectors/hubspot"
	"github.com/bsamanvith/agentkit/pkg/credentials"
	"github.com/bsamanvith/agentkit/pkg/tool"
	"github.com/bsamanvith/agentkit/pkg/toolset"
	"github.com/bsamanvith/agentkit/pkg/toolset/toolsettest"
)

// fake is a HubSpot that answers every request with an empty search page
// and records the requests' bodies and tokens.
type fake struct {
	mu     sync.Mutex
	bodies []string
	tokens []string
}

func newFake(t *testing.T) (*fake, string) {
	t.Helper()
	f := &fake{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.bodies = append(f.bodies, string(b))
		f.tokens = append(f.tokens, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		f.mu.Unlock()
		_, _ = w.Write([]byte(`{"total": 0, "results": []}`))
	}))
	t.Cleanup(srv.Close)
	return f, srv.URL
}

func (f *fake) seen() (bodies, tokens []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.bodies), slices.Clone(f.tokens)
}

func build(t *testing.T, base, settings string, src credentials.Source) ([]tool.Tool, error) {
	t.Helper()
	f := hubspot.Factory(hubspot.WithBaseURL(base),
		hubspot.WithLookupEnv(func(k string) (string, bool) { return "env-token", k == hubspot.DefaultTokenEnv }))
	if settings == "" {
		settings = "{}"
	}
	return f.Build(t.Context(), toolset.BuildInput{Settings: json.RawMessage(settings), Credentials: src})
}

func find(t *testing.T, tools []tool.Tool, name string) tool.Tool {
	t.Helper()
	for _, tl := range tools {
		if tl.Definition().Name == name {
			return tl
		}
	}
	t.Fatalf("no tool %s", name)
	return nil
}

// Criterion: search builds HubSpot's filter groups from the input: one
// group (every filter must hold), no value for the HAS_PROPERTY operators,
// the query trimmed, newest first, and the page cursor passed on.
func TestSearchRequest(t *testing.T) {
	t.Parallel()
	f, base := newFake(t)
	tools, err := build(t, base, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = find(t, tools, "hubspot_search").Invoke(t.Context(), json.RawMessage(`{"object": "deals", "query": "  renewal ", "after": "20",
		"filters": [{"property": "amount", "operator": "GTE", "value": "10000"}, {"property": "closedate", "operator": "NOT_HAS_PROPERTY"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	bodies, _ := f.seen()
	var got, want any
	_ = json.Unmarshal([]byte(bodies[0]), &got)
	_ = json.Unmarshal([]byte(`{"query": "renewal", "limit": 10, "after": "20",
		"sorts": [{"propertyName": "hs_lastmodifieddate", "direction": "DESCENDING"}],
		"filterGroups": [{"filters": [
			{"propertyName": "amount", "operator": "GTE", "value": "10000"},
			{"propertyName": "closedate", "operator": "NOT_HAS_PROPERTY"}]}]}`), &want)
	if g, w := mustJSON(got), mustJSON(want); g != w {
		t.Errorf("body\n got %s\nwant %s", g, w)
	}
}

func mustJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

// Criterion: an unknown object or operator is refused before any request.
func TestInvalidInputSendsNothing(t *testing.T) {
	t.Parallel()
	f, base := newFake(t)
	tools, err := build(t, base, `{"read_only": false}`, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ tool, input string }{
		{"hubspot_search", `{"object": "owners"}`},
		{"hubspot_search", `{"object": "deals", "filters": [{"property": "amount", "operator": "IN", "value": "1"}]}`},
		{"hubspot_search", `{"object": "deals", "limit": 101}`},
		{"hubspot_search", `{"object": "deals", "after": "2&limit=100"}`},
		{"hubspot_get_record", `{"object": "notes", "id": "1"}`},
		{"hubspot_create_record", `{"object": "contacts", "properties": {"e mail": "x"}}`},
		{"hubspot_create_note", `{"body": "x", "records": [{"object": "owners", "id": "1"}]}`},
	} {
		_, err := find(t, tools, c.tool).Invoke(t.Context(), json.RawMessage(c.input))
		if !errors.Is(err, tool.ErrInvalidInput) {
			t.Errorf("%s %s: err %v, want ErrInvalidInput", c.tool, c.input, err)
		}
	}
	if bodies, _ := f.seen(); len(bodies) != 0 {
		t.Errorf("%d requests sent, want none", len(bodies))
	}
}

// Criterion: writes are absent by default and medium risk when on.
func TestGatingFollowsSettings(t *testing.T) {
	t.Parallel()
	names := func(tools []tool.Tool) []string {
		var out []string
		for _, tl := range tools {
			out = append(out, tl.Definition().Name)
		}
		return out
	}
	tools, err := build(t, "http://127.0.0.1:1", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(names(tools), ","); got != "hubspot_search,hubspot_get_record" {
		t.Errorf("default tools %s, want the two read tools", got)
	}
	tools, err = build(t, "http://127.0.0.1:1", `{"read_only": false}`, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 5 {
		t.Fatalf("tools %v, want five", names(tools))
	}
	for _, tl := range tools[2:] {
		m := tl.Definition().Metadata
		if m.Kind != tool.KindWrite || m.Risk != tool.RiskMedium {
			t.Errorf("%s: kind %s risk %s, want write and medium", tl.Definition().Name, m.Kind, m.Risk)
		}
	}
}

// Criterion: the slot's OAuth token is sent per caller, never the
// environment's; a caller with none gets "not connected" and nothing is
// sent.
func TestSlotToken(t *testing.T) {
	t.Parallel()
	f, base := newFake(t)
	src := toolsettest.NewKeySource("hubspot")
	src.Set("ann", credentials.OAuth2{AccessToken: credentials.NewSecret("ann-oauth")})
	src.Set("bob", credentials.OAuth2{AccessToken: credentials.NewSecret("bob-oauth")})
	tools, err := build(t, base, "", src)
	if err != nil {
		t.Fatal(err)
	}
	search := find(t, tools, "hubspot_search")
	for _, u := range []string{"ann", "bob"} {
		if _, err := search.Invoke(toolsettest.AsUser(t, u), json.RawMessage(`{"object": "contacts"}`)); err != nil {
			t.Fatalf("%s: %v", u, err)
		}
	}
	_, err = search.Invoke(toolsettest.AsUser(t, "carol"), json.RawMessage(`{"object": "contacts"}`))
	if err == nil || !strings.Contains(err.Error(), "no HubSpot account is connected") {
		t.Errorf("carol: err %v, want not connected", err)
	}
	if _, tokens := f.seen(); strings.Join(tokens, ",") != "ann-oauth,bob-oauth" {
		t.Errorf("tokens sent %v, want each caller's own", tokens)
	}
	if _, err := build(t, base, `{"access_token_env": "HUBSPOT_ACCESS_TOKEN"}`, src); !errors.Is(err, toolset.ErrInvalidSettings) {
		t.Errorf("slot and access_token_env: err %v, want ErrInvalidSettings", err)
	}
}

// Scopes asks for the read scopes, and the write ones only when writes are
// on.
func TestScopes(t *testing.T) {
	t.Parallel()
	read, err := hubspot.Scopes(json.RawMessage(`{}`))
	if err != nil || strings.Join(read, " ") != "oauth tickets crm.objects.contacts.read crm.objects.companies.read crm.objects.deals.read" {
		t.Errorf("read-only scopes %v, %v", read, err)
	}
	write, _ := hubspot.Scopes(json.RawMessage(`{"read_only": false}`))
	if !slices.Contains(write, "crm.objects.deals.write") || slices.Contains(read, "crm.objects.deals.write") {
		t.Errorf("write scopes %v", write)
	}
}
