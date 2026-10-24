package toolsettest

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/credentials"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

// KeySource is a credentials.Source for testing a keyed connector with a
// credential slot of scope user: each user (tenancy.User of the call's
// context) has their own credential, set with [KeySource.Set]. A user with
// none gets the not-connected error the real sources return, naming
// Toolset. Get counts calls, so a test can check a key is read per call.
// It does not implement HTTPClient, which connectors that send the key
// themselves do not use.
type KeySource struct {
	// Toolset names the toolset in the not-connected error.
	Toolset string

	mu    sync.Mutex
	creds map[string]credentials.Credential
	gets  int
}

// NewKeySource returns a source with no credentials.
func NewKeySource(toolset string) *KeySource {
	return &KeySource{Toolset: toolset, creds: map[string]credentials.Credential{}}
}

// SetKey gives user the API key key.
func (s *KeySource) SetKey(user, key string) {
	s.Set(user, credentials.APIKey{Key: credentials.NewSecret(key)})
}

// Set gives user the credential c; nil disconnects them.
func (s *KeySource) Set(user string, c credentials.Credential) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c == nil {
		delete(s.creds, user)
		return
	}
	s.creds[user] = c
}

// Gets is how many times Get was called.
func (s *KeySource) Gets() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.gets
}

// Get returns the credential of ctx's user.
func (s *KeySource) Get(ctx context.Context) (credentials.Credential, error) {
	user, _ := tenancy.User(ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gets++
	c, ok := s.creds[user]
	if !ok {
		return nil, &credentials.NotConnectedError{Toolset: s.Toolset}
	}
	return c, nil
}

// HTTPClient is not implemented.
func (s *KeySource) HTTPClient(context.Context, *http.Client, ...credentials.AuthOption) (*http.Client, error) {
	return nil, errors.New("toolsettest.KeySource: HTTPClient is not implemented")
}

// AsUser returns a context of t's that acts for user in tenant "acme", as a
// signed-in user's call does: what a [KeySource] reads.
func AsUser(t testing.TB, user string) context.Context {
	t.Helper()
	tn, err := tenancy.NewTenant("acme")
	if err != nil {
		t.Fatal(err)
	}
	return tenancy.WithUser(tenancy.With(t.Context(), tn), user)
}
