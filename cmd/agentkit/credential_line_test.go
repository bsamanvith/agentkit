package main

import (
	"encoding/json"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
	"github.com/bsamanvith/agentkit/pkg/toolset"
)

// GET /v1/toolsets says where a Google toolset's token comes from: the
// variable access_token_env names (GOOGLE_ACCESS_TOKEN by default), or the
// credential slot its spec names.
func TestGoogleToolsetCredentialLine(t *testing.T) {
	t.Parallel()
	unset := func(string) string { return "" }
	for _, typ := range []string{"gmail", "calendar", "drive"} {
		got := credentialOf(toolset.Spec{Name: typ, Type: typ, Settings: json.RawMessage(`{}`)}, unset)
		if got.Status != wire.CredentialMissing || got.Env != "GOOGLE_ACCESS_TOKEN" {
			t.Errorf("%s without a token: %+v, want missing GOOGLE_ACCESS_TOKEN", typ, got)
		}
	}
	got := credentialOf(toolset.Spec{Name: "mail", Type: "gmail", Settings: json.RawMessage(`{"access_token_env":"MAIL_TOKEN"}`)},
		func(k string) string { return map[string]string{"MAIL_TOKEN": "x"}[k] })
	if got.Status != wire.CredentialConfigured || got.Env != "MAIL_TOKEN" {
		t.Errorf("gmail with MAIL_TOKEN set: %+v", got)
	}
	got = credentialOf(toolset.Spec{Name: "gmail", Type: "gmail", Credential: &toolset.CredentialRef{Slot: "gmail", Scope: "user"}}, unset)
	if got.Slot != "gmail" || got.Scope != "user" {
		t.Errorf("gmail with a slot: %+v", got)
	}
}
