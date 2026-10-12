package auth_test

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/auth"
	"github.com/bsamanvith/agentkit/pkg/auth/authtest"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

func bearerCredentials(t *testing.T) []authtest.Credential {
	entries := testEntries(t)
	var out []authtest.Credential
	for i, key := range secrets {
		out = append(out, authtest.Credential{Name: entries[i].ID, Secret: key, Apply: authtest.Bearer, Want: principalOf(t, entries[i])})
	}
	return out
}

func TestAPIKeysConformance(t *testing.T) {
	t.Parallel()
	authtest.RunAuthenticator(t, func(t *testing.T) authtest.Subject {
		return authtest.Subject{Authenticator: testKeys(t), Credentials: bearerCredentials(t)}
	})
}

func TestChainConformance(t *testing.T) {
	t.Parallel()
	nothing := auth.AuthenticatorFunc(func(*http.Request) (auth.Principal, error) { return auth.Principal{}, auth.ErrNoCredentials })
	authtest.RunAuthenticator(t, func(t *testing.T) authtest.Subject {
		return authtest.Subject{Authenticator: auth.Chain(nothing, testKeys(t), nothing), Credentials: bearerCredentials(t)}
	})
}

func TestConfigErrors(t *testing.T) {
	t.Parallel()
	good := func() []auth.APIKey { return testEntries(t) }
	with := func(i int, f func(e *auth.APIKey)) []auth.APIKey {
		es := good()
		f(&es[i])
		return es
	}
	upper := strings.ToUpper(auth.HashKey(acmeKey)[len("sha256:"):])
	tests := []struct {
		name    string
		entries []auth.APIKey
		wantID  string
		wantIs  error
	}{
		{"duplicate id", with(2, func(e *auth.APIKey) { e.ID = "acme-ci" }), "acme-ci", nil},
		{"duplicate hash", with(2, func(e *auth.APIKey) { e.Hash = auth.HashKey(acmeKey) }), "globex-ops", nil},
		{"duplicate hash in upper case", with(2, func(e *auth.APIKey) { e.Hash = "sha256:" + upper }), "globex-ops", nil},
		{"hash without prefix", with(1, func(e *auth.APIKey) { e.Hash = strings.TrimPrefix(e.Hash, "sha256:") }), "acme-ana", nil},
		{"hash of another kind", with(1, func(e *auth.APIKey) { e.Hash = "md5:" + strings.Repeat("a", 32) }), "acme-ana", nil},
		{"hash too short", with(1, func(e *auth.APIKey) { e.Hash = e.Hash[:len(e.Hash)-2] }), "acme-ana", nil},
		{"hash too long", with(1, func(e *auth.APIKey) { e.Hash += "00" }), "acme-ana", nil},
		{"hash not hex", with(1, func(e *auth.APIKey) { e.Hash = e.Hash[:len(e.Hash)-1] + "g" }), "acme-ana", nil},
		{"no hash", with(1, func(e *auth.APIKey) { e.Hash = "" }), "acme-ana", nil},
		{"invalid tenant", with(0, func(e *auth.APIKey) { e.Tenant = "../etc" }), "acme-ci", tenancy.ErrInvalidTenant},
		{"no tenant", with(0, func(e *auth.APIKey) { e.Tenant = "" }), "acme-ci", tenancy.ErrInvalidTenant},
		{"no id", with(0, func(e *auth.APIKey) { e.ID = "" }), "", nil},
		{"id with a space", with(0, func(e *auth.APIKey) { e.ID = "acme ci" }), "acme ci", nil},
		{"user with a newline", with(1, func(e *auth.APIKey) { e.User = "ana\nadmin" }), "acme-ana", nil},
		{"malformed toolset pattern", with(0, func(e *auth.APIKey) { e.Toolsets = []string{"crm-["} }), "acme-ci", nil},
		{"empty scope", with(0, func(e *auth.APIKey) { e.Scopes = []string{""} }), "acme-ci", nil},
	}
	for _, tt := range tests {
		_, err := auth.NewAPIKeys(tt.entries)
		checkConfigError(t, tt.name, err, tt.wantID, tt.wantIs)
	}

	keyTests := []struct {
		name string
		e    auth.APIKey
		key  string
	}{
		{"short key", auth.APIKey{ID: "short", Tenant: "acme"}, "fifteen-bytes!!"},
		{"key with a space", auth.APIKey{ID: "spaced", Tenant: "acme"}, "a key with spaces in it"},
		{"key and hash", auth.APIKey{ID: "both", Tenant: "acme", Hash: auth.HashKey(acmeKey)}, globexKey},
	}
	for _, tt := range keyTests {
		_, err := auth.KeyEntry(tt.e, tt.key)
		checkConfigError(t, tt.name, err, tt.e.ID, nil)
	}
	if _, err := auth.KeyEntry(auth.APIKey{ID: "sixteen", Tenant: "acme"}, "sixteen-bytes-ok"); err != nil {
		t.Errorf("a 16-byte key: %v", err)
	}
	if _, err := auth.NewAPIKeys(nil); err == nil {
		t.Errorf("no entries: no error")
	}
}

func checkConfigError(t *testing.T, name string, err error, wantID string, wantIs error) {
	t.Helper()
	var ce *auth.ConfigError
	if !errors.As(err, &ce) || ce.ID != wantID {
		t.Errorf("%s: got %v, want a *ConfigError for entry %q", name, err, wantID)
		return
	}
	if wantID != "" && !strings.Contains(err.Error(), wantID) {
		t.Errorf("%s: the message %q does not name the entry %q", name, err, wantID)
	}
	if wantIs != nil && !errors.Is(err, wantIs) {
		t.Errorf("%s: got %v, want it to match %v", name, err, wantIs)
	}
	for _, s := range append([]string{"fifteen-bytes!!", "a key with spaces in it"}, secrets...) {
		if authtest.Leaks(err.Error(), s) {
			t.Errorf("%s: the error contains a key or its hash: %v", name, err)
		}
	}
}
