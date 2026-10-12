package auth_test

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/auth"
	"github.com/bsamanvith/agentkit/pkg/auth/authtest"
)

var secrets = []string{acmeKey, acmeUser, globexKey}

const (
	challenge        = `Bearer realm="agentkit"`
	invalidChallenge = `Bearer realm="agentkit", error="invalid_token"`
)

func TestValidKeyGivesConfiguredPrincipal(t *testing.T) {
	t.Parallel()
	r := newRig(t, testKeys(t), secrets)
	entries := testEntries(t)
	for i, key := range secrets {
		for _, scheme := range []string{"Bearer ", "bearer ", "BEARER ", "Bearer   "} {
			res := r.get([]string{scheme + key})
			want := principalOf(t, entries[i])
			if !res.called || !authtest.Equal(res.seen.principal, want) {
				t.Errorf("%s%s: status %d, handler saw %+v; want %+v", scheme, entries[i].ID, res.status, res.seen.principal, want)
			}
		}
	}
}

func TestRejectedRequestsGet401(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		authz []string
		want  string
	}{
		{"missing", nil, challenge},
		{"wrong key", []string{"Bearer " + strings.Repeat("x", len(acmeKey))}, invalidChallenge},
		{"one byte differs", []string{"Bearer " + acmeKey[:len(acmeKey)-1] + "d"}, invalidChallenge},
		{"prefix", []string{"Bearer " + acmeKey[:len(acmeKey)-1]}, invalidChallenge},
		{"superstring", []string{"Bearer " + acmeKey + "x"}, invalidChallenge},
		{"basic", []string{"Basic " + acmeKey}, invalidChallenge},
		{"no scheme", []string{acmeKey}, invalidChallenge},
		{"empty token", []string{"Bearer "}, invalidChallenge},
		{"two headers", []string{"Bearer " + acmeKey, "Bearer " + acmeKey}, invalidChallenge},
		{"oversized", []string{"Bearer " + acmeKey + strings.Repeat(" ", auth.MaxAuthorizationLength)}, invalidChallenge},
		{"oversized key", []string{"Bearer " + strings.Repeat(acmeKey, auth.MaxAuthorizationLength/len(acmeKey))}, invalidChallenge},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			res := newRig(t, testKeys(t), secrets).get(tt.authz)
			if res.status != http.StatusUnauthorized || res.called {
				t.Fatalf("status %d, handler called %v; want 401 and not called", res.status, res.called)
			}
			if got := res.header.Values("WWW-Authenticate"); len(got) != 1 || got[0] != tt.want {
				t.Errorf("WWW-Authenticate = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestKeysNeverCrossTenants(t *testing.T) {
	t.Parallel()
	r := newRig(t, testKeys(t), secrets)
	spoof := []string{"X-Tenant", "globex", "X-Tenant-Id", "globex", "X-Agentkit-Tenant", "globex", "X-User", "mallory", "X-Agentkit-User", "mallory"}
	for _, key := range []string{acmeKey, acmeUser} {
		res := r.get([]string{"Bearer " + key}, spoof...)
		if !res.called || res.seen.tenant.ID() != "acme" || res.seen.user == "mallory" {
			t.Errorf("an acme key with headers naming globex and mallory: called %v, tenant %q, user %q", res.called, res.seen.tenant.ID(), res.seen.user)
		}
	}
	if res := r.get([]string{"Bearer " + globexKey}, "X-Tenant", "acme"); res.seen.tenant.ID() != "globex" {
		t.Errorf("the globex key with X-Tenant: acme gave tenant %q", res.seen.tenant.ID())
	}
}

func TestInternalErrorsAnswer500(t *testing.T) {
	t.Parallel()
	tests := map[string]auth.Authenticator{
		"error": auth.AuthenticatorFunc(func(*http.Request) (auth.Principal, error) {
			return auth.Principal{}, errors.New("session store unreachable")
		}),
		"principal with no tenant": auth.AuthenticatorFunc(func(*http.Request) (auth.Principal, error) {
			return auth.Principal{User: "ana"}, nil
		}),
	}
	for name, a := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			res := newRig(t, a, nil).get([]string{"Bearer " + acmeKey})
			if res.status != http.StatusInternalServerError || res.called {
				t.Errorf("status %d, handler called %v; want 500 and not called", res.status, res.called)
			}
		})
	}
}

func TestRequestID(t *testing.T) {
	t.Parallel()
	r := newRig(t, testKeys(t), secrets)
	ok := r.get([]string{"Bearer " + acmeKey}, auth.RequestIDHeader, "req-01:abc.DEF_9")
	if ok.seen.requestID != "req-01:abc.DEF_9" {
		t.Errorf("a valid client request id: handler saw %q", ok.seen.requestID)
	}
	ids := map[string]bool{}
	for _, sent := range []string{"", "has space", "new\nline", strings.Repeat("a", 129), "ünï"} {
		res := r.get([]string{"Bearer " + acmeKey}, auth.RequestIDHeader, sent)
		got := res.seen.requestID
		if got == sent || !validRequestIDish(got) || ids[got] {
			t.Errorf("client request id %q: handler saw %q, want a new valid id", sent, got)
		}
		ids[got] = true
	}
	if res := r.get(nil, auth.RequestIDHeader, "req-02"); res.header.Get(auth.RequestIDHeader) != "req-02" {
		t.Errorf("a 401 carries request id %q, want the client's", res.header.Get(auth.RequestIDHeader))
	}
}

func validRequestIDish(s string) bool { return s != "" && validRequestID(s) }

func TestFailureOptions(t *testing.T) {
	t.Parallel()
	var got []string
	writer := func(w http.ResponseWriter, _ *http.Request, status int) {
		got = append(got, w.Header().Get("WWW-Authenticate"), w.Header().Get(auth.RequestIDHeader))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"v":1,"error":{"code":"unauthorized"}}`))
	}
	r := newRig(t, testKeys(t), secrets,
		auth.WithRealm(`agent"kit`),
		auth.WithResourceMetadata("https://agentkit.example/.well-known/oauth-protected-resource"),
		auth.WithErrorWriter(writer))
	none := r.get(nil, auth.RequestIDHeader, "r1")
	bad := r.get([]string{"Bearer nope-nope-nope-nope"}, auth.RequestIDHeader, "r2")

	base := `Bearer realm="agent\"kit", resource_metadata="https://agentkit.example/.well-known/oauth-protected-resource"`
	want := []string{base, "r1", base + `, error="invalid_token"`, "r2"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("the error writer saw headers %q, want %q", got, want)
	}
	for _, res := range []result{none, bad} {
		if res.status != http.StatusUnauthorized || res.header.Get("Content-Type") != "application/json" {
			t.Errorf("status %d, content type %q: the error writer's response was not used", res.status, res.header.Get("Content-Type"))
		}
	}
}
