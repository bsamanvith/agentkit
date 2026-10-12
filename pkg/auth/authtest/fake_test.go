package authtest_test

import (
	"testing"

	"github.com/bsamanvith/agentkit/pkg/auth"
	"github.com/bsamanvith/agentkit/pkg/auth/authtest"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

func TestFakeConformance(t *testing.T) {
	t.Parallel()
	acme, err := tenancy.NewTenant("acme")
	if err != nil {
		t.Fatal(err)
	}
	globex, err := tenancy.NewTenant("globex")
	if err != nil {
		t.Fatal(err)
	}
	creds := []authtest.Credential{
		{Name: "acme", Secret: "tok-acme-0123456789", Apply: authtest.Bearer, Want: auth.Principal{Tenant: acme, User: "ana", Scopes: []string{"*"}, Toolsets: []string{"crm-*"}}},
		{Name: "globex", Secret: "tok-globex-0123456789", Apply: authtest.Bearer, Want: auth.Principal{Tenant: globex, Scopes: []string{"agent:run"}}},
	}
	authtest.RunAuthenticator(t, func(*testing.T) authtest.Subject {
		tokens := map[string]auth.Principal{}
		for _, c := range creds {
			tokens[c.Secret] = c.Want
		}
		return authtest.Subject{Authenticator: authtest.NewFake(tokens), Credentials: creds}
	})
}
