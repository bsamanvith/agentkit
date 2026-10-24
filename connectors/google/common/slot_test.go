package common_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/bsamanvith/agentkit/connectors/google/common"
	"github.com/bsamanvith/agentkit/pkg/credentials"
	"github.com/bsamanvith/agentkit/pkg/toolset/toolsettest"
)

func oauth(token string) credentials.OAuth2 {
	return credentials.OAuth2{AccessToken: credentials.NewSecret(token)}
}

// slotTokens is the source a toolset with a slot builds with.
func slotTokens(t *testing.T, src credentials.Source, s common.Settings) (common.TokenSource, error) {
	t.Helper()
	if err := s.Validate("gmail"); err != nil {
		t.Fatal(err)
	}
	env := common.Env{LookupEnv: func(string) (string, bool) { return "env-token-never-sent", true }}
	env.Defaults()
	return env.TokensForSpec("gmail", s, src)
}

// Each call sends the token of the user it runs for, never the
// environment's.
func TestFromSourcePerCaller(t *testing.T) {
	t.Parallel()
	src := toolsettest.NewKeySource("gmail")
	src.Set("ann", oauth("ya29.ann"))
	src.Set("bob", oauth("ya29.bob"))
	tokens, err := slotTokens(t, src, common.Settings{})
	if err != nil {
		t.Fatal(err)
	}
	r := newRig(t, tokens, ok(`{}`))
	for _, user := range []string{"ann", "bob", "ann"} {
		if err := r.client.DoJSON(toolsettest.AsUser(t, user), common.Request{Path: "/x"}, nil); err != nil {
			t.Fatalf("as %s: %v", user, err)
		}
		if got := r.last.Load().Header.Get("Authorization"); got != "Bearer ya29."+user {
			t.Errorf("as %s: Authorization = %q", user, got)
		}
	}
}

// A user with no account connected is told to connect one, and Google is
// not called; a slot holding an API key says so.
func TestFromSourceNotConnected(t *testing.T) {
	t.Parallel()
	src := toolsettest.NewKeySource("gmail")
	src.SetKey("bob", "not-a-google-token")
	tokens, err := slotTokens(t, src, common.Settings{})
	if err != nil {
		t.Fatal(err)
	}
	r := newRig(t, tokens, ok(`{}`))
	err = r.client.DoJSON(toolsettest.AsUser(t, "carol"), common.Request{Path: "/x"}, nil)
	if !errors.Is(err, common.ErrNoToken) || !strings.Contains(err.Error(), "connect one") {
		t.Errorf("carol: err = %v, want ErrNoToken saying to connect", err)
	}
	if _, err := tokens.Token(toolsettest.AsUser(t, "bob")); !errors.Is(err, common.ErrNotOAuth) || strings.Contains(err.Error(), "not-a-google-token") {
		t.Errorf("bob: err = %v, want ErrNotOAuth without the value", err)
	}
	if n := r.hits.Load(); n != 0 {
		t.Errorf("Google got %d requests, want none", n)
	}
}

// A slot and access_token_env together are refused.
func TestSlotAndEnvRefused(t *testing.T) {
	t.Parallel()
	_, err := slotTokens(t, toolsettest.NewKeySource("gmail"), common.Settings{AccessTokenEnv: "MY_TOKEN"})
	if err == nil || !strings.Contains(err.Error(), "two places") {
		t.Errorf("err = %v, want the two-places refusal", err)
	}
}
