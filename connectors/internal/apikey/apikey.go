// Package apikey is where a keyed connector's API key comes from on each
// call: the caller's own credential when the toolset's spec names a
// credential slot (a user connects their key, T045), else one key read from
// the environment when the toolset was built. Exa and Parallel use it; the
// rules are docs/CONNECTORS.md's "Per-tenant credentials".
package apikey

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/bsamanvith/agentkit/pkg/credentials"
)

// ErrNotAPIKey reports a slot that holds another kind of credential (an
// OAuth2 token, a user name and password) than the API key the connector
// sends.
var ErrNotAPIKey = errors.New("the connected credential is not an API key")

// Key gives the API key of one call. The zero Key has none.
type Key struct {
	static string
	src    credentials.Source
}

// Static is the one key every call uses, as read from the environment.
func Static(key string) Key { return Key{static: key} }

// FromSource is the key of the caller of each call, from src: the tenant's
// or the user's, as the spec's credential scope says.
func FromSource(src credentials.Source) Key { return Key{src: src} }

// PerCaller reports whether the key comes from the caller's credential.
func (k Key) PerCaller() bool { return k.src != nil }

// Get returns the call's key. With a source, a caller with no key is the
// source's [credentials.ErrNotConnected] (its message names the toolset and
// nothing else), returned as it is so the model can say what to connect.
func (k Key) Get(ctx context.Context) (string, error) {
	if k.src == nil {
		return k.static, nil
	}
	c, err := k.src.Get(ctx)
	if err != nil {
		return "", err
	}
	key, ok := c.(credentials.APIKey)
	if !ok {
		return "", fmt.Errorf("%w (it is %s): connect an API key instead", ErrNotAPIKey, c.Kind())
	}
	v := strings.TrimSpace(key.Key.Reveal())
	if v == "" || strings.ContainsAny(v, "\r\n") {
		return "", fmt.Errorf("%w: the connected key is empty or has a line break", ErrNotAPIKey)
	}
	return v, nil
}
