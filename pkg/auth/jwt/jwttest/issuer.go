// Package jwttest has a fake identity provider for tests of code that takes
// JWT bearer tokens: an httptest server that publishes an OpenID
// configuration and a JWKS of keys made in the test, and signs tokens with
// them. Its keys can rotate and its endpoint can go down.
package jwttest

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"

	jose "github.com/go-jose/go-jose/v4"
)

// JWKSPath is where an [Issuer] serves its keys.
const JWKSPath = "/jwks.json"

// CloudflareCertsPath is where Cloudflare Access publishes a team's keys; an
// [Issuer] serves its keys there too, so it can stand in for a team domain.
const CloudflareCertsPath = "/cdn-cgi/access/certs"

// Issuer is a fake identity provider. Its methods are safe for concurrent
// use.
type Issuer struct {
	srv *httptest.Server

	mu        sync.Mutex
	keys      []key
	published map[string]bool // kids in the JWKS; a key may sign unpublished
	down      bool
	header    http.Header
	fetches   int
	next      int
}

type key struct {
	kid  string
	alg  string
	priv crypto.Signer
}

// NewIssuer starts an Issuer, closed when the test ends. It has no keys:
// add them with [Issuer.AddKey].
func NewIssuer(t testing.TB) *Issuer {
	t.Helper()
	i := &Issuer{published: map[string]bool{}, header: http.Header{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		if i.isDown() {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"issuer": i.URL(), "jwks_uri": i.JWKSURL()})
	})
	jwks := func(w http.ResponseWriter, r *http.Request) {
		i.mu.Lock()
		i.fetches++
		down := i.down
		var set jose.JSONWebKeySet
		for _, k := range i.keys {
			if i.published[k.kid] {
				set.Keys = append(set.Keys, jose.JSONWebKey{Key: k.priv.Public(), KeyID: k.kid, Algorithm: k.alg, Use: "sig"})
			}
		}
		for name, vs := range i.header {
			w.Header()[name] = vs
		}
		i.mu.Unlock()
		if down {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(set)
	}
	mux.HandleFunc("GET "+JWKSPath, jwks)
	mux.HandleFunc("GET "+CloudflareCertsPath, jwks)
	i.srv = httptest.NewServer(mux)
	t.Cleanup(i.srv.Close)
	return i
}

// URL is the issuer: the "iss" of its tokens, and where its OpenID
// configuration is.
func (i *Issuer) URL() string { return i.srv.URL }

// JWKSURL is where its keys are published.
func (i *Issuer) JWKSURL() string { return i.srv.URL + JWKSPath }

// Client is an HTTP client that reaches the server.
func (i *Issuer) Client() *http.Client { return i.srv.Client() }

// AddKey makes a key for alg (RS256, RS384, RS512, PS256, ES256, ES384,
// ES512 or EdDSA), publishes it, and returns its kid.
func (i *Issuer) AddKey(t testing.TB, alg string) string {
	t.Helper()
	priv, err := newKey(alg)
	if err != nil {
		t.Fatal(err)
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	i.next++
	kid := "key-" + strconv.Itoa(i.next)
	i.keys = append(i.keys, key{kid: kid, alg: alg, priv: priv})
	i.published[kid] = true
	return kid
}

// AddWeakRSAKey makes an RS256 key of 1024 bits, too short for RFC 7518,
// publishes it, and returns its kid.
func (i *Issuer) AddWeakRSAKey(t testing.TB) string {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	i.next++
	kid := "key-" + strconv.Itoa(i.next)
	i.keys = append(i.keys, key{kid: kid, alg: "RS256", priv: priv})
	i.published[kid] = true
	return kid
}

// Unpublish takes the key out of the JWKS; it still signs.
func (i *Issuer) Unpublish(kid string) {
	i.mu.Lock()
	defer i.mu.Unlock()
	delete(i.published, kid)
}

func newKey(alg string) (crypto.Signer, error) {
	switch alg {
	case "RS256", "RS384", "RS512", "PS256", "PS384", "PS512":
		return rsa.GenerateKey(rand.Reader, 2048)
	case "ES384":
		return ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	case "ES512":
		return ecdsa.GenerateKey(elliptic.P521(), rand.Reader)
	case "EdDSA":
		_, priv, err := ed25519.GenerateKey(rand.Reader)
		return priv, err
	default:
		return ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	}
}

// Sign returns a compact JWT of claims signed with the key kid, its "kid"
// header set to kid. See [Issuer.SignAs] to set another header kid.
func (i *Issuer) Sign(t testing.TB, kid string, claims map[string]any) string {
	t.Helper()
	return i.SignAs(t, kid, kid, claims)
}

// SignAs is Sign with the header kid set to headerKID ("" for none).
func (i *Issuer) SignAs(t testing.TB, kid, headerKID string, claims map[string]any) string {
	t.Helper()
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	return i.SignPayload(t, kid, headerKID, payload)
}

// SignPayload is SignAs with any payload, claims or not.
func (i *Issuer) SignPayload(t testing.TB, kid, headerKID string, payload []byte) string {
	t.Helper()
	k, ok := i.key(kid)
	if !ok {
		t.Fatalf("jwttest: no key %q", kid)
	}
	return sign(t, k, k.alg, headerKID, payload)
}

// SignAlg is Sign with another algorithm of the key's type than the one the
// JWKS names for it (PS256 with an RS256 key).
func (i *Issuer) SignAlg(t testing.TB, kid, alg string, claims map[string]any) string {
	t.Helper()
	k, ok := i.key(kid)
	if !ok {
		t.Fatalf("jwttest: no key %q", kid)
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	return sign(t, k, alg, kid, payload)
}

func sign(t testing.TB, k key, alg, headerKID string, payload []byte) string {
	t.Helper()
	opts := (&jose.SignerOptions{}).WithType("JWT")
	if headerKID != "" {
		opts = opts.WithHeader("kid", headerKID)
	}
	s, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.SignatureAlgorithm(alg), Key: k.priv}, opts)
	if err != nil {
		t.Fatal(err)
	}
	obj, err := s.Sign(payload)
	if err != nil {
		t.Fatal(err)
	}
	tok, err := obj.CompactSerialize()
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

// PublicJWK returns the JSON of the public key kid, as the JWKS has it: what
// an attacker can read and try to use as an HMAC secret.
func (i *Issuer) PublicJWK(t testing.TB, kid string) []byte {
	t.Helper()
	k, ok := i.key(kid)
	if !ok {
		t.Fatalf("jwttest: no key %q", kid)
	}
	b, err := json.Marshal(jose.JSONWebKey{Key: k.priv.Public(), KeyID: k.kid, Algorithm: k.alg, Use: "sig"})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func (i *Issuer) key(kid string) (key, bool) {
	i.mu.Lock()
	defer i.mu.Unlock()
	for _, k := range i.keys {
		if k.kid == kid {
			return k, true
		}
	}
	return key{}, false
}

// SetDown makes the server answer 503 (down) or serve again.
func (i *Issuer) SetDown(down bool) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.down = down
}

func (i *Issuer) isDown() bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.down
}

// SetHeader sets a header of the JWKS responses, such as Cache-Control.
func (i *Issuer) SetHeader(name, value string) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.header.Set(name, value)
}

// Fetches is how many times the JWKS was requested, failures included.
func (i *Issuer) Fetches() int {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.fetches
}

// Compact returns a token built from the given header, claims and
// signature, signed by nobody: for tokens no key could make, such as
// alg "none".
func Compact(header, claims map[string]any, sig []byte) string {
	h, _ := json.Marshal(header)
	c, _ := json.Marshal(claims)
	enc := base64.RawURLEncoding
	return enc.EncodeToString(h) + "." + enc.EncodeToString(c) + "." + enc.EncodeToString(sig)
}
