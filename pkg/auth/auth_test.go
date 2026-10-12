package auth_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/auth"
)

func TestAllowsToolset(t *testing.T) {
	t.Parallel()
	tests := []struct {
		toolsets []string
		name     string
		want     bool
	}{
		{nil, "search", false},
		{[]string{}, "search", false},
		{[]string{"search"}, "search", true},
		{[]string{"search"}, "search2", false},
		{[]string{"search"}, "Search", false},
		{[]string{"crm-*"}, "crm-salesforce", true},
		{[]string{"crm-*"}, "crm-", true},
		{[]string{"crm-*"}, "crm", false},
		{[]string{"crm-?"}, "crm-a", true},
		{[]string{"crm-?"}, "crm-ab", false},
		{[]string{"*"}, "anything", true},
		{[]string{"*"}, "", true},
		{[]string{"search", "gmail"}, "gmail", true},
		{[]string{"[a-c]*"}, "bing", true},
		{[]string{"[a-c]*"}, "dog", false},
		{[]string{"["}, "[", false}, // malformed: matches nothing
		{[]string{"[", "x"}, "x", true},
	}
	for _, tt := range tests {
		if got := (auth.Principal{Toolsets: tt.toolsets}).AllowsToolset(tt.name); got != tt.want {
			t.Errorf("Toolsets %q AllowsToolset(%q) = %v, want %v", tt.toolsets, tt.name, got, tt.want)
		}
	}
}

func TestHasScope(t *testing.T) {
	t.Parallel()
	tests := []struct {
		scopes []string
		scope  string
		want   bool
	}{
		{nil, "agent:run", false},
		{[]string{"agent:run"}, "agent:run", true},
		{[]string{"agent:run"}, "agent:read", false},
		{[]string{"agent:*"}, "agent:read", true},
		{[]string{"admin:*"}, "agent:run", false},
		{[]string{"*"}, "admin:credentials", true},
		{[]string{"agent:run"}, "agent:*", false}, // the scope asked for is a name, not a pattern
	}
	for _, tt := range tests {
		if got := (auth.Principal{Scopes: tt.scopes}).HasScope(tt.scope); got != tt.want {
			t.Errorf("Scopes %q HasScope(%q) = %v, want %v", tt.scopes, tt.scope, got, tt.want)
		}
	}
}

func TestChain(t *testing.T) {
	t.Parallel()
	acme := auth.Principal{Tenant: tenant(t, "acme")}
	globex := auth.Principal{Tenant: tenant(t, "globex")}
	internal := errors.New("store down")
	answer := func(p auth.Principal, err error) func(*int) auth.Authenticator {
		return func(calls *int) auth.Authenticator {
			return auth.AuthenticatorFunc(func(*http.Request) (auth.Principal, error) {
				*calls++
				return p, err
			})
		}
	}
	none := answer(auth.Principal{}, auth.ErrNoCredentials)
	invalid := answer(auth.Principal{}, auth.ErrInvalidCredentials)
	tests := []struct {
		name      string
		chain     []func(*int) auth.Authenticator
		want      auth.Principal
		wantErr   error
		wantCalls []int
	}{
		{"empty", nil, auth.Principal{}, auth.ErrNoCredentials, nil},
		{"no credentials falls through", []func(*int) auth.Authenticator{none, answer(acme, nil)}, acme, nil, []int{1, 1}},
		{"all without credentials", []func(*int) auth.Authenticator{none, none}, auth.Principal{}, auth.ErrNoCredentials, []int{1, 1}},
		{"invalid stops", []func(*int) auth.Authenticator{invalid, answer(acme, nil)}, auth.Principal{}, auth.ErrInvalidCredentials, []int{1, 0}},
		{"invalid after none stops", []func(*int) auth.Authenticator{none, invalid, answer(acme, nil)}, auth.Principal{}, auth.ErrInvalidCredentials, []int{1, 1, 0}},
		{"internal error stops", []func(*int) auth.Authenticator{answer(auth.Principal{}, internal), answer(acme, nil)}, auth.Principal{}, internal, []int{1, 0}},
		{"first success wins", []func(*int) auth.Authenticator{answer(acme, nil), answer(globex, nil)}, acme, nil, []int{1, 0}},
		{"both sentinels count as invalid", []func(*int) auth.Authenticator{answer(auth.Principal{}, errors.Join(auth.ErrNoCredentials, auth.ErrInvalidCredentials)), answer(acme, nil)}, auth.Principal{}, auth.ErrInvalidCredentials, []int{1, 0}},
	}
	for _, tt := range tests {
		calls := make([]int, len(tt.chain))
		var as []auth.Authenticator
		for i, mk := range tt.chain {
			as = append(as, mk(&calls[i]))
		}
		p, err := auth.Chain(as...).Authenticate(httptest.NewRequest(http.MethodGet, "/", nil))
		if p.Tenant != tt.want.Tenant || !errors.Is(err, tt.wantErr) || (tt.wantErr == nil) != (err == nil) {
			t.Errorf("%s: got %q, %v; want %q, %v", tt.name, p.Tenant.ID(), err, tt.want.Tenant.ID(), tt.wantErr)
		}
		for i := range calls {
			if calls[i] != tt.wantCalls[i] {
				t.Errorf("%s: authenticator %d called %d times, want %d", tt.name, i, calls[i], tt.wantCalls[i])
			}
		}
	}
}

// TestBearerToken pins what BearerToken reads, for authenticators that build
// on it (T041's JWT). Most cases are covered end to end; these are the
// boundaries no configured key can reach.
func TestBearerToken(t *testing.T) {
	t.Parallel()
	pad := func(n int) string { return "Bearer " + strings.Repeat("t", n-len("Bearer ")) }
	tests := []struct {
		name    string
		values  []string
		want    string
		wantErr error
	}{
		{"none", nil, "", auth.ErrNoCredentials},
		{"plain", []string{"Bearer tok.en-1_~+/="}, "tok.en-1_~+/=", nil},
		{"lower case, several spaces", []string{"bearer   tok"}, "tok", nil},
		{"at the length limit", []string{pad(auth.MaxAuthorizationLength)}, strings.Repeat("t", auth.MaxAuthorizationLength-len("Bearer ")), nil},
		{"one byte over", []string{pad(auth.MaxAuthorizationLength + 1)}, "", auth.ErrInvalidCredentials},
		{"NUL", []string{"Bearer tok\x00"}, "", auth.ErrInvalidCredentials},
		{"DEL", []string{"Bearer tok\x7f"}, "", auth.ErrInvalidCredentials},
		{"tab inside", []string{"Bearer to\tk"}, "", auth.ErrInvalidCredentials},
		{"non-ASCII", []string{"Bearer tök"}, "", auth.ErrInvalidCredentials},
		{"trailing space", []string{"Bearer tok "}, "", auth.ErrInvalidCredentials},
		{"empty header", []string{""}, "", auth.ErrInvalidCredentials},
		{"two headers", []string{"Bearer a", "Bearer a"}, "", auth.ErrInvalidCredentials},
	}
	for _, tt := range tests {
		h := http.Header{}
		for _, v := range tt.values {
			h.Add("Authorization", v)
		}
		got, err := auth.BearerToken(h)
		if got != tt.want || !errors.Is(err, tt.wantErr) || (tt.wantErr == nil) != (err == nil) {
			t.Errorf("%s: got %q, %v; want %q, %v", tt.name, got, err, tt.want, tt.wantErr)
		}
	}
}
