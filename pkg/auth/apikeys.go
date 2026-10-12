package auth

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"path"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

// MinKeyLength is the shortest API key [KeyEntry] accepts, in bytes.
const MinKeyLength = 16

// hashPrefix starts every configured key hash.
const hashPrefix = "sha256:"

// APIKey is the configuration of one API key: the hash of the key and the
// principal it grants. The key itself is never configured: make the hash with
// [HashKey] (or build the entry with [KeyEntry]).
type APIKey struct {
	// ID names the entry in logs and errors: 1 to 64 letters, digits, '.',
	// '_' or '-', starting with a letter or digit.
	ID string `json:"id"`
	// Hash is "sha256:" and the hex SHA-256 of the key.
	Hash string `json:"hash"`
	// Tenant is the id of the tenant the key acts for (see tenancy.NewTenant).
	Tenant string `json:"tenant"`
	// User is the user the key acts as, if any: at most 256 bytes of UTF-8
	// with no control characters.
	User string `json:"user,omitempty"`
	// Scopes and Toolsets are those of the principal (see [Principal]); each
	// must be a well-formed path.Match pattern.
	Scopes   []string `json:"scopes,omitempty"`
	Toolsets []string `json:"toolsets,omitempty"`
}

// ConfigError is an API key entry that [NewAPIKeys] or [KeyEntry] refused.
// Its message names the entry, never its key or hash.
type ConfigError struct {
	ID  string // the entry's id, "" when it has none
	Err error
}

func (e *ConfigError) Error() string { return fmt.Sprintf("auth: API key %q: %v", e.ID, e.Err) }

func (e *ConfigError) Unwrap() error { return e.Err }

// HashKey returns the hash of key in the form [APIKey.Hash] takes.
func HashKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hashPrefix + hex.EncodeToString(sum[:])
}

// KeyEntry returns e with its Hash set from key, for a key given as a secret
// rather than as configuration (an environment variable). The key must be at
// least [MinKeyLength] bytes of visible ASCII, and e must have no Hash yet.
// The key is not kept.
func KeyEntry(e APIKey, key string) (APIKey, error) {
	switch {
	case e.Hash != "":
		return APIKey{}, &ConfigError{ID: e.ID, Err: errors.New("has both a key and a hash")}
	case len(key) < MinKeyLength:
		return APIKey{}, &ConfigError{ID: e.ID, Err: fmt.Errorf("key is %d bytes, want at least %d", len(key), MinKeyLength)}
	case !visibleASCII(key):
		return APIKey{}, &ConfigError{ID: e.ID, Err: errors.New("key has a byte outside visible ASCII, so no request could carry it")}
	}
	e.Hash = HashKey(key)
	return e, nil
}

// APIKeys is an [Authenticator] of bearer keys ("Authorization: Bearer
// <key>"), each granting the principal of its [APIKey] entry. A request with
// no Authorization header has no credentials; any other is invalid unless its
// token is a configured key. It is safe for concurrent use.
type APIKeys struct {
	digests    [][sha256.Size]byte
	principals []Principal
}

// NewAPIKeys returns the authenticator of the given entries. It fails, with a
// [*ConfigError] naming the entry, on a missing or duplicate id, a malformed
// or duplicate hash, an invalid tenant or user, or a malformed pattern. It
// needs at least one entry.
func NewAPIKeys(entries []APIKey) (*APIKeys, error) {
	if len(entries) == 0 {
		return nil, errors.New("auth: no API keys")
	}
	k := &APIKeys{}
	ids := make(map[string]bool, len(entries))
	for i, e := range entries {
		d, p, err := parseEntry(e)
		if err == nil && ids[e.ID] {
			err = errors.New("duplicate id")
		}
		if err == nil {
			if j := k.lookup(d, subtle.ConstantTimeCompare); j >= 0 {
				err = fmt.Errorf("same key as entry %q", k.principals[j].Via.ID)
			}
		}
		if err != nil {
			if e.ID == "" {
				err = fmt.Errorf("entry %d: %w", i, err)
			}
			return nil, &ConfigError{ID: e.ID, Err: err}
		}
		ids[e.ID] = true
		k.digests = append(k.digests, d)
		k.principals = append(k.principals, p)
	}
	return k, nil
}

func parseEntry(e APIKey) ([sha256.Size]byte, Principal, error) {
	var d [sha256.Size]byte
	if !validID(e.ID) {
		return d, Principal{}, errors.New("id must be 1 to 64 letters, digits, '.', '_' or '-', starting with a letter or digit")
	}
	hexDigest, ok := strings.CutPrefix(e.Hash, hashPrefix)
	if !ok || len(hexDigest) != hex.EncodedLen(sha256.Size) {
		return d, Principal{}, errors.New(`hash must be "sha256:" and 64 hex digits (see auth.HashKey)`)
	}
	if _, err := hex.Decode(d[:], []byte(hexDigest)); err != nil {
		return d, Principal{}, errors.New(`hash must be "sha256:" and 64 hex digits (see auth.HashKey)`)
	}
	t, err := tenancy.NewTenant(e.Tenant)
	if err != nil {
		return d, Principal{}, err
	}
	if len(e.User) > 256 || !utf8.ValidString(e.User) || strings.ContainsFunc(e.User, unicode.IsControl) {
		return d, Principal{}, errors.New("user must be at most 256 bytes of UTF-8 with no control characters")
	}
	for _, pat := range append(append([]string(nil), e.Scopes...), e.Toolsets...) {
		if _, err := path.Match(pat, ""); err != nil || pat == "" {
			return d, Principal{}, fmt.Errorf("scope or toolset %q is not a valid pattern", pat)
		}
	}
	p := Principal{Tenant: t, User: e.User, Scopes: e.Scopes, Toolsets: e.Toolsets, Via: Credential{Kind: KindAPIKey, ID: e.ID}}
	return d, p.Clone(), nil
}

// validID reports whether id is 1 to 64 letters, digits, '.', '_' or '-',
// starting with a letter or digit: safe in a log line.
func validID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for i := range len(id) {
		c := id[i]
		alnum := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
		if !alnum && (i == 0 || c != '.' && c != '_' && c != '-') {
			return false
		}
	}
	return true
}

// Authenticate returns the principal of the request's bearer key.
func (k *APIKeys) Authenticate(r *http.Request) (Principal, error) {
	token, err := BearerToken(r.Header)
	if err != nil {
		return Principal{}, err
	}
	i := k.lookup(sha256.Sum256([]byte(token)), subtle.ConstantTimeCompare)
	if i < 0 {
		return Principal{}, fmt.Errorf("%w: unknown API key", ErrInvalidCredentials)
	}
	return k.principals[i].Clone(), nil
}

// lookup returns the index of the entry whose digest is d, or -1. It compares
// d with every entry, whichever matches, using eq (subtle.ConstantTimeCompare;
// a parameter so that a test can count the comparisons), so the time taken
// says nothing about which entry matched or how much of a digest did.
func (k *APIKeys) lookup(d [sha256.Size]byte, eq func(x, y []byte) int) int {
	found := -1
	for i := range k.digests {
		found = subtle.ConstantTimeSelect(eq(k.digests[i][:], d[:]), i, found)
	}
	return found
}

// String describes k without its hashes.
func (k *APIKeys) String() string { return fmt.Sprintf("auth.APIKeys(%d keys)", len(k.digests)) }

// GoString describes k without its hashes, for %#v.
func (k *APIKeys) GoString() string { return k.String() }
