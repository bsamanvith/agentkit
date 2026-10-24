package parallel_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/bsamanvith/agentkit/connectors/internal/apikey"
	"github.com/bsamanvith/agentkit/connectors/parallel"
	"github.com/bsamanvith/agentkit/pkg/credentials"
	"github.com/bsamanvith/agentkit/pkg/tool"
	"github.com/bsamanvith/agentkit/pkg/toolset"
	"github.com/bsamanvith/agentkit/pkg/toolset/toolsettest"
)

// keyEcho is a fake Parallel that answers search and extract with one
// result whose excerpt holds the x-api-key it got, and records the keys.
func keyEcho(t *testing.T) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var keys []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		k := r.Header.Get("x-api-key")
		mu.Lock()
		keys = append(keys, k)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results": [{"url": "https://a.example/", "title": "t", "excerpts": ["your key is ` + k + `"]}], "errors": []}`))
	}))
	t.Cleanup(srv.Close)
	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), keys...)
	}
}

func buildWithSlot(t *testing.T, base, settings string, src credentials.Source, env map[string]string) ([]tool.Tool, error) {
	t.Helper()
	f := parallel.Factory(parallel.WithBaseURL(base),
		parallel.WithLookupEnv(func(k string) (string, bool) { v, ok := env[k]; return v, ok }))
	if settings == "" {
		settings = "{}"
	}
	return f.Build(t.Context(), toolset.BuildInput{Settings: json.RawMessage(settings), Credentials: src})
}

// Criterion (per-user keys): with a credential slot, each call sends the
// key of the user it runs for, read at call time, never the environment's;
// a user with no key gets "parallel is not connected for this account" and
// Parallel is not called; each user's key is redacted from the answer.
func TestEachUserSendsTheirOwnKey(t *testing.T) {
	t.Parallel()
	srv, keys := keyEcho(t)
	src := toolsettest.NewKeySource("parallel")
	src.SetKey("ann", "par-key-of-ann-3e8a")
	src.SetKey("bob", "par-key-of-bob-c402")
	tools, err := buildWithSlot(t, srv.URL, "", src, map[string]string{"PARALLEL_API_KEY": "server-key-never-sent"})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if src.Gets() != 0 {
		t.Errorf("Build read the credential %d times, want none", src.Gets())
	}
	byName := map[string]tool.Tool{}
	for _, tl := range tools {
		byName[tl.Definition().Name] = tl
	}
	for _, c := range []struct{ user, key, tool, input string }{
		{"ann", "par-key-of-ann-3e8a", "parallel_search", `{"query": "bike shops"}`},
		{"bob", "par-key-of-bob-c402", "parallel_search", `{"query": "bike shops"}`},
		{"bob", "par-key-of-bob-c402", "parallel_extract", `{"urls": ["https://a.example/"]}`},
	} {
		out, err := byName[c.tool].Invoke(toolsettest.AsUser(t, c.user), json.RawMessage(c.input))
		if err != nil {
			t.Fatalf("%s as %s: %v", c.tool, c.user, err)
		}
		if strings.Contains(string(out), c.key) || !strings.Contains(string(out), "[redacted]") {
			t.Errorf("%s as %s: output %s, want the user's key redacted", c.tool, c.user, out)
		}
	}
	if got, want := strings.Join(keys(), ","), "par-key-of-ann-3e8a,par-key-of-bob-c402,par-key-of-bob-c402"; got != want {
		t.Errorf("keys sent %q, want %q", got, want)
	}

	src.Set("ann", nil)
	for _, user := range []string{"ann", "carol"} {
		_, err := byName["parallel_search"].Invoke(toolsettest.AsUser(t, user), json.RawMessage(`{"query": "q"}`))
		if !errors.Is(err, credentials.ErrNotConnected) || !strings.Contains(err.Error(), "parallel is not connected for this account") {
			t.Errorf("as %s: err %v, want parallel's not-connected error", user, err)
		}
	}
	if n := len(keys()); n != 3 {
		t.Errorf("Parallel got %d requests, want 3 (none without a key)", n)
	}
}

// A slot holding another kind of credential fails without calling Parallel.
func TestSlotWithAnotherKindOfCredential(t *testing.T) {
	t.Parallel()
	srv, keys := keyEcho(t)
	src := toolsettest.NewKeySource("parallel")
	src.Set("ann", credentials.OAuth2{AccessToken: credentials.NewSecret("tok-55aa")})
	tools, err := buildWithSlot(t, srv.URL, "", src, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	_, err = tools[0].Invoke(toolsettest.AsUser(t, "ann"), json.RawMessage(`{"query": "q"}`))
	if !errors.Is(err, apikey.ErrNotAPIKey) || strings.Contains(err.Error(), "tok-55aa") {
		t.Errorf("err %v, want ErrNotAPIKey without the token", err)
	}
	if len(keys()) != 0 {
		t.Errorf("Parallel was called")
	}
}

// With a slot, the build needs no environment variable; naming one too is
// refused.
func TestSlotBuildRules(t *testing.T) {
	t.Parallel()
	src := toolsettest.NewKeySource("parallel")
	if _, err := buildWithSlot(t, "http://127.0.0.1:1", "", src, nil); err != nil {
		t.Errorf("Build with a slot and no env: %v", err)
	}
	_, err := buildWithSlot(t, "http://127.0.0.1:1", `{"api_key_env": "PARALLEL_API_KEY"}`, src, map[string]string{"PARALLEL_API_KEY": "k"})
	if !errors.Is(err, toolset.ErrInvalidSettings) || !strings.Contains(err.Error(), "credential slot") {
		t.Errorf("Build with a slot and api_key_env: err %v, want ErrInvalidSettings naming both", err)
	}
	if _, err := buildWithSlot(t, "http://127.0.0.1:1", "", nil, nil); err == nil {
		t.Errorf("Build with neither: want an error")
	}
}
