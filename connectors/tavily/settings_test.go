package tavily_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/bsamanvith/agentkit/connectors/tavily"
	"github.com/bsamanvith/agentkit/pkg/tool"
	"github.com/bsamanvith/agentkit/pkg/toolset"
)

// TestAuthModes: each mode sends the headers it promises, on both tools.
// The rig checks every request for exactly one auth mode and the right one
// (expectedKey restates the rule); this test also pins it per case, so a
// broken rule fails here by name.
func TestAuthModes(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name     string
		settings string
		env      map[string]string
		bearer   bool
	}{
		{"auto with the key", `{}`, map[string]string{"TAVILY_API_KEY": canary}, true},
		{"auto without the key", `{"auth": "auto"}`, map[string]string{}, false},
		{"auto with a blank key", `{}`, map[string]string{"TAVILY_API_KEY": "  "}, false},
		{"auto with another variable", `{"api_key_env": "SEARCH_KEY"}`, map[string]string{"SEARCH_KEY": canary, "TAVILY_API_KEY": "tvly-other"}, true},
		{"key", `{"auth": "key"}`, map[string]string{"TAVILY_API_KEY": canary}, true},
		{"keyless with the key set", `{"auth": "keyless"}`, map[string]string{"TAVILY_API_KEY": canary}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t, rigOpts{settings: c.settings, env: c.env})
			if n := len(r.up.requests()); n != 0 {
				t.Fatalf("Build sent %d requests, want none", n)
			}
			r.up.queue(
				reply{status: http.StatusOK, body: fixture(t, "search-go-release")},
				reply{status: http.StatusOK, body: fixture(t, "extract-query-text")},
			)
			if _, err := r.call("tavily_search", `{"query": "go release"}`); err != nil {
				t.Fatal(err)
			}
			if _, err := r.call("tavily_extract", `{"urls": ["https://www.tavily.com"]}`); err != nil {
				t.Fatal(err)
			}
			for _, q := range r.up.requests() {
				authz, keyless := q.header.Get("Authorization"), q.header.Get("X-Tavily-Access-Mode")
				if c.bearer && (authz != "Bearer "+canary || keyless != "") {
					t.Errorf("%s: Authorization %q, keyless %q; want the bearer key only", q.path, authz, keyless)
				}
				if !c.bearer && (authz != "" || keyless != "keyless") {
					t.Errorf("%s: Authorization %q, keyless %q; want the keyless header only", q.path, authz, keyless)
				}
			}
			// Startup says which mode, never the key (the rig checks the key).
			want := `"mode":"keyless"`
			if c.bearer {
				want = `"mode":"key"`
			}
			if !strings.Contains(r.logs.String(), want) {
				t.Errorf("log %s does not report %s", r.logs.String(), want)
			}
		})
	}
}

// TestSettingsRejected: bad settings fail Build with ErrInvalidSettings and
// a message that says what to fix, never echoing a secret.
func TestSettingsRejected(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name     string
		settings string
		env      map[string]string
		mentions string // what the message must name
	}{
		{"literal api_key", `{"api_key": "` + canary + `"}`, nil, "api_key_env"},
		{"literal api_key, other case", `{"API_Key": "` + canary + `"}`, nil, "api_key_env"},
		{"key in api_key_env", `{"api_key_env": "` + canary + `"}`, nil, "api_key_env"},
		{"key mode without the variable", `{"auth": "key", "api_key_env": "MY_TAVILY_KEY"}`, map[string]string{"TAVILY_API_KEY": canary}, "MY_TAVILY_KEY"},
		{"key mode with a blank variable", `{"auth": "key"}`, map[string]string{"TAVILY_API_KEY": ""}, "TAVILY_API_KEY"},
		{"unknown field", `{"serach_defaults": {}}`, nil, "serach_defaults"},
		{"unknown nested field", `{"search_defaults": {"max_results": 3}}`, nil, "max_results"},
		{"bad auth", `{"auth": "maybe"}`, nil, "auth"},
		{"bad search_depth", `{"search_defaults": {"search_depth": "deep"}}`, nil, "search_depth"},
		{"bad topic", `{"search_defaults": {"topic": "sports"}}`, nil, "topic"},
		{"bad chunks_per_source", `{"search_defaults": {"chunks_per_source": 4}}`, nil, "chunks_per_source"},
		{"bad extract_depth", `{"extract_defaults": {"extract_depth": "deep"}}`, nil, "extract_depth"},
		{"bad format", `{"extract_defaults": {"format": "html"}}`, nil, "format"},
		{"bad timeout", `{"timeout": "soon"}`, nil, "timeout"},
		{"zero timeout", `{"timeout": "0s"}`, nil, "timeout"},
		{"content cap too small", `{"max_content_chars": {"search": 10}}`, nil, "max_content_chars"},
		{"base_url over http", `{"base_url": "http://api.tavily.com"}`, nil, "base_url"},
		{"base_url over http to a private IP", `{"base_url": "http://10.0.0.1:8080"}`, nil, "base_url"},
		{"base_url other scheme", `{"base_url": "ftp://api.tavily.com"}`, nil, "base_url"},
		{"base_url without a scheme", `{"base_url": "api.tavily.com"}`, nil, "base_url"},
		{"base_url with a user", `{"base_url": "https://` + canary + `@api.tavily.com"}`, nil, "base_url"},
		{"base_url with a query", `{"base_url": "https://api.tavily.com?api_key=` + canary + `"}`, nil, "base_url"},
		{"base_url not a URL", `{"base_url": "https://%zz"}`, nil, "base_url"},
		{"not an object", `[]`, nil, ""},
		{"trailing data", `{} {}`, nil, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			env := c.env
			lookup := func(k string) (string, bool) { v, ok := env[k]; return v, ok }
			_, err := tavily.Factory(tavily.WithLookupEnv(lookup)).Build(t.Context(), json.RawMessage(c.settings))
			if !errors.Is(err, toolset.ErrInvalidSettings) {
				t.Fatalf("Build = %v, want ErrInvalidSettings", err)
			}
			// The message is what an operator reads to fix the config.
			if !strings.Contains(err.Error(), c.mentions) {
				t.Errorf("error %q does not mention %q", err, c.mentions)
			}
			if strings.Contains(err.Error(), canary) {
				t.Errorf("error %q echoes the key", err)
			}
		})
	}
}

// TestOptionalToolsetSkipped: through toolset.Build, a tavily toolset that
// fails is skipped when optional (cmd/agentkit shows it in /readyz) and
// fails the build when required.
func TestOptionalToolsetSkipped(t *testing.T) {
	t.Parallel()
	catalog, err := toolset.NewCatalog(toolset.Demo(), tavily.Factory())
	if err != nil {
		t.Fatal(err)
	}
	specs := []toolset.Spec{
		{Name: "demo", Type: toolset.DemoType},
		{Name: "search", Type: tavily.Type, Settings: json.RawMessage(`{"auth": "key"}`), Optional: true},
	}
	regs, problems, err := toolset.Build(t.Context(), catalog, specs)
	if err != nil || len(regs) != 1 || regs["demo"] == nil || len(problems) != 1 || problems[0].Toolset != "search" {
		t.Errorf("optional: regs %v, problems %v, err %v; want demo built and search skipped", regs, problems, err)
	}
	specs[1].Optional = false
	if _, _, err := toolset.Build(t.Context(), catalog, specs); !errors.Is(err, toolset.ErrInvalidSettings) {
		t.Errorf("required: err %v, want ErrInvalidSettings", err)
	}
}

// TestBaseURLSetting: a base_url setting sends the toolset's requests there,
// over the WithBaseURL option (cmd/agentkit tests point a config at a local
// replay server this way); http is accepted for localhost and loopback IPs.
func TestBaseURLSetting(t *testing.T) {
	t.Parallel()
	up := newUpstream(t)
	up.queue(reply{status: http.StatusOK, body: fixture(t, "search-go-release")})
	settings := `{"auth": "keyless", "base_url": "` + up.srv.URL + `/"}`
	tools, err := tavily.Factory(tavily.WithBaseURL("http://127.0.0.1:9")).Build(t.Context(), json.RawMessage(settings))
	if err != nil {
		t.Fatal(err)
	}
	reg, err := tool.NewRegistry(tools)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Invoke(t.Context(), "tavily_search", json.RawMessage(`{"query": "go release"}`)); err != nil {
		t.Fatal(err)
	}
	if q := up.requests(); len(q) != 1 || q[0].path != "/search" {
		t.Errorf("requests %+v, want one to /search on the base_url", q)
	}
	for _, u := range []string{"https://tavily.example/v1", "http://localhost:8080", "http://127.0.0.2", "http://[::1]:9000/"} {
		if _, err := tavily.Factory().Build(t.Context(), json.RawMessage(`{"base_url": "`+u+`"}`)); err != nil {
			t.Errorf("base_url %s: %v", u, err)
		}
	}
}
