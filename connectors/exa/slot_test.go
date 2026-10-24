package exa_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/bsamanvith/agentkit/connectors/exa"
	"github.com/bsamanvith/agentkit/connectors/internal/apikey"
	"github.com/bsamanvith/agentkit/pkg/credentials"
	"github.com/bsamanvith/agentkit/pkg/tool"
	"github.com/bsamanvith/agentkit/pkg/toolset"
	"github.com/bsamanvith/agentkit/pkg/toolset/toolsettest"
)

// keyEcho is a fake Exa that answers every request with one result whose
// text holds the x-api-key it got, and records the keys.
func keyEcho(t *testing.T) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var keys []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		k := r.Header.Get("x-api-key")
		mu.Lock()
		keys = append(keys, k)
		mu.Unlock()
		if r.Header.Get("Authorization") != "" {
			t.Errorf("request has an Authorization header too")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results": [{"url": "https://a.example/", "title": "t", "text": "your key is ` + k + `", "highlights": ["key ` + k + `"]}]}`))
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
	f := exa.Factory(exa.WithBaseURL(base),
		exa.WithLookupEnv(func(k string) (string, bool) { v, ok := env[k]; return v, ok }))
	if settings == "" {
		settings = "{}"
	}
	return f.Build(t.Context(), toolset.BuildInput{Settings: json.RawMessage(settings), Credentials: src})
}

// Criterion (per-user keys): with a credential slot, each call sends the
// key of the user it runs for, read at call time, and the environment's
// key is never used; a user who has not connected a key gets "exa is not
// connected for this account" and Exa is not called; each user's key is
// redacted from what Exa sends back.
func TestEachUserSendsTheirOwnKey(t *testing.T) {
	t.Parallel()
	srv, keys := keyEcho(t)
	src := toolsettest.NewKeySource("exa")
	src.SetKey("ann", "exa-key-of-ann-7c1d")
	src.SetKey("bob", "exa-key-of-bob-91fe")
	tools, err := buildWithSlot(t, srv.URL, "", src, map[string]string{"EXA_API_KEY": "server-key-never-sent"})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if src.Gets() != 0 {
		t.Errorf("Build read the credential %d times, want none (it is read per call)", src.Gets())
	}
	search, extract := tools[0], tools[1]
	for _, c := range []struct {
		user, key string
		tl        tool.Tool
		input     string
	}{
		{"ann", "exa-key-of-ann-7c1d", search, `{"query": "q"}`},
		{"bob", "exa-key-of-bob-91fe", search, `{"query": "q"}`},
		{"ann", "exa-key-of-ann-7c1d", extract, `{"urls": ["https://a.example/"]}`},
	} {
		out, err := c.tl.Invoke(toolsettest.AsUser(t, c.user), json.RawMessage(c.input))
		if err != nil {
			t.Fatalf("%s as %s: %v", c.tl.Definition().Name, c.user, err)
		}
		if strings.Contains(string(out), c.key) || !strings.Contains(string(out), "[redacted]") {
			t.Errorf("%s as %s: output %s, want the user's key redacted", c.tl.Definition().Name, c.user, out)
		}
	}
	if got, want := strings.Join(keys(), ","), "exa-key-of-ann-7c1d,exa-key-of-bob-91fe,exa-key-of-ann-7c1d"; got != want {
		t.Errorf("keys sent %q, want %q", got, want)
	}

	// Bob disconnects; carol never connected.
	src.Set("bob", nil)
	for _, user := range []string{"bob", "carol"} {
		_, err := search.Invoke(toolsettest.AsUser(t, user), json.RawMessage(`{"query": "q"}`))
		if !errors.Is(err, credentials.ErrNotConnected) || !strings.Contains(err.Error(), "exa is not connected for this account") {
			t.Errorf("as %s: err %v, want exa's not-connected error", user, err)
		}
		var te *tool.Error
		if !errors.As(err, &te) || !errors.Is(te.Kind, tool.ErrToolFailed) {
			t.Errorf("as %s: err %v, want a tool.Error of kind ErrToolFailed", user, err)
		}
	}
	if n := len(keys()); n != 3 {
		t.Errorf("Exa got %d requests, want 3 (none without a key)", n)
	}
}

// A slot that holds another kind of credential than an API key fails the
// call without calling Exa, and says so without the value.
func TestSlotWithAnotherKindOfCredential(t *testing.T) {
	t.Parallel()
	srv, keys := keyEcho(t)
	src := toolsettest.NewKeySource("exa")
	src.Set("ann", credentials.Basic{User: "ann", Password: credentials.NewSecret("pw-9d1c")})
	tools, err := buildWithSlot(t, srv.URL, "", src, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	_, err = tools[0].Invoke(toolsettest.AsUser(t, "ann"), json.RawMessage(`{"query": "q"}`))
	if !errors.Is(err, apikey.ErrNotAPIKey) || strings.Contains(err.Error(), "pw-9d1c") {
		t.Errorf("err %v, want ErrNotAPIKey without the password", err)
	}
	if len(keys()) != 0 {
		t.Errorf("Exa was called")
	}
}

// With a slot, the build needs no environment variable, and naming one too
// is refused: the key would have two sources.
func TestSlotBuildRules(t *testing.T) {
	t.Parallel()
	src := toolsettest.NewKeySource("exa")
	if _, err := buildWithSlot(t, "http://127.0.0.1:1", "", src, nil); err != nil {
		t.Errorf("Build with a slot and no env: %v", err)
	}
	_, err := buildWithSlot(t, "http://127.0.0.1:1", `{"api_key_env": "EXA_API_KEY"}`, src, map[string]string{"EXA_API_KEY": "k"})
	if !errors.Is(err, toolset.ErrInvalidSettings) || !strings.Contains(err.Error(), "credential slot") {
		t.Errorf("Build with a slot and api_key_env: err %v, want ErrInvalidSettings naming both", err)
	}
	if _, err := buildWithSlot(t, "http://127.0.0.1:1", "", nil, nil); !errors.Is(err, exa.ErrMissingKey) {
		t.Errorf("Build with neither: err %v, want ErrMissingKey", err)
	}
}
