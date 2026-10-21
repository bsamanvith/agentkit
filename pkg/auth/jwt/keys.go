package jwt

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	jose "github.com/go-jose/go-jose/v4"

	"github.com/bsamanvith/agentkit/pkg/clock"
)

const (
	// fetchTimeout bounds one fetch of the keys or the OpenID configuration.
	fetchTimeout = 10 * time.Second
	// maxDocument is the largest JWKS or OpenID configuration read, in bytes.
	maxDocument = 1 << 20
	// minRSABits is the smallest RSA key used (RFC 7518 §3.3).
	minRSABits = 2048
	// maxRedirects caps the redirects one fetch follows.
	maxRedirects = 5
)

// keyCache holds the provider's keys and fetches them again when a token
// needs it, at most once per MinRefreshInterval.
type keyCache struct {
	client      *http.Client
	clock       clock.Clock
	log         *slog.Logger
	issuer      string
	jwksURL     string // configured; empty until discovered otherwise
	minInterval time.Duration
	maxAge      time.Duration

	fetching sync.Mutex // held by the one fetch in flight

	mu        sync.Mutex // guards the fields below
	keys      []jose.JSONWebKey
	expires   time.Time // when keys go stale
	attempted bool
	lastTry   time.Time // when the last fetch started
}

func (k *keyCache) init(a *Authenticator) {
	k.clock, k.log, k.issuer = a.clock, a.log, a.cfg.Issuer
	k.jwksURL, k.minInterval, k.maxAge = a.cfg.JWKSURL, a.cfg.MinRefreshInterval, a.cfg.MaxCacheAge
	// A redirect is held to what the configured URLs are (https, or http
	// on a loopback host): a provider redirecting to http would otherwise
	// let anyone on the path swap the keys. A client with its own policy
	// keeps it.
	c := *k.client
	if c.CheckRedirect == nil {
		c.CheckRedirect = checkRedirect
	}
	k.client = &c
}

// checkRedirect lets a fetch follow a redirect only to a URL [checkURL]
// accepts, and at most maxRedirects of them.
func checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= maxRedirects {
		return fmt.Errorf("more than %d redirects", maxRedirects)
	}
	if err := checkURL(req.URL.String()); err != nil {
		return fmt.Errorf("redirect: %w", err)
	}
	return nil
}

// verify checks the signature of jws with the key that kid names and that fits
// alg, fetching the keys again if none does or they are stale, and returns
// the payload.
func (k *keyCache) verify(ctx context.Context, jws *jose.JSONWebSignature, kid, alg string) ([]byte, error) {
	keys, fresh := k.candidates(kid, alg)
	if len(keys) == 0 || !fresh {
		// A failed or skipped fetch leaves the last good set.
		_ = k.refresh(ctx)
		keys, _ = k.candidates(kid, alg)
	}
	if len(keys) == 0 {
		return nil, errors.New("no key for the token's kid and algorithm")
	}
	for _, key := range keys {
		if payload, err := jws.Verify(key.Key); err == nil {
			return payload, nil
		}
	}
	return nil, errors.New("bad signature")
}

// candidates returns the keys that may have signed a token with kid and alg,
// and whether the set is fresh. With no kid, the set must have exactly one
// key.
func (k *keyCache) candidates(kid, alg string) ([]jose.JSONWebKey, bool) {
	k.mu.Lock()
	defer k.mu.Unlock()
	fresh := k.attempted && k.clock.Now().Before(k.expires)
	var out []jose.JSONWebKey
	if kid == "" {
		if len(k.keys) == 1 && fits(k.keys[0], alg) {
			out = append(out, k.keys[0])
		}
		return out, fresh
	}
	for _, key := range k.keys {
		if key.KeyID == kid && fits(key, alg) {
			out = append(out, key)
		}
	}
	return out, fresh
}

// fits reports whether key may verify a signature made with alg: its type
// (and curve) is alg's, and its own "alg", if any, is alg.
func fits(key jose.JSONWebKey, alg string) bool {
	if key.Algorithm != "" && key.Algorithm != alg {
		return false
	}
	switch pub := key.Key.(type) {
	case *rsa.PublicKey:
		return strings.HasPrefix(alg, "RS") || strings.HasPrefix(alg, "PS")
	case *ecdsa.PublicKey:
		switch alg {
		case "ES256":
			return pub.Curve == elliptic.P256()
		case "ES384":
			return pub.Curve == elliptic.P384()
		case "ES512":
			return pub.Curve == elliptic.P521()
		}
	case ed25519.PublicKey:
		return alg == "EdDSA"
	}
	return false
}

// refresh fetches the keys, unless a fetch started less than MinRefreshInterval
// ago. Concurrent callers wait for the one fetch in flight and then find the
// interval not yet passed, so a burst makes one fetch.
func (k *keyCache) refresh(ctx context.Context) error {
	k.fetching.Lock()
	defer k.fetching.Unlock()
	k.mu.Lock()
	now := k.clock.Now()
	if k.attempted && now.Sub(k.lastTry) < k.minInterval {
		k.mu.Unlock()
		return nil
	}
	k.attempted, k.lastTry = true, now
	k.mu.Unlock()

	// A request that ends does not end the fetch the next requests share.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), fetchTimeout)
	defer cancel()
	keys, ttl, err := k.fetch(ctx)
	if err != nil {
		k.log.LogAttrs(ctx, slog.LevelWarn, "auth/jwt: fetching the keys failed; keeping the last good set", slog.String("issuer", k.issuer), slog.String("error", err.Error()))
		return err
	}
	k.mu.Lock()
	k.keys, k.expires = keys, now.Add(ttl)
	k.mu.Unlock()
	k.log.LogAttrs(ctx, slog.LevelDebug, "auth/jwt: fetched the keys", slog.String("issuer", k.issuer), slog.Int("keys", len(keys)), slog.Duration("ttl", ttl))
	return nil
}

// fetch gets the JWKS (discovering its URL first if need be), and returns
// its usable keys and how long they may be used.
func (k *keyCache) fetch(ctx context.Context) ([]jose.JSONWebKey, time.Duration, error) {
	if k.jwksURL == "" {
		u, err := k.discover(ctx)
		if err != nil {
			return nil, 0, err
		}
		k.jwksURL = u // only fetch writes it, under k.fetching
	}
	body, header, err := k.get(ctx, k.jwksURL)
	if err != nil {
		return nil, 0, fmt.Errorf("jwks: %w", err)
	}
	var set struct {
		Keys []json.RawMessage `json:"keys"`
	}
	if err := json.Unmarshal(body, &set); err != nil {
		return nil, 0, errors.New("jwks: not a JWK set")
	}
	var keys []jose.JSONWebKey
	for _, raw := range set.Keys {
		// One key go-jose cannot read (a new type, a bad certificate) is
		// skipped, not the whole set.
		var key jose.JSONWebKey
		if err := key.UnmarshalJSON(raw); err != nil {
			continue
		}
		if key, ok := usable(key); ok {
			keys = append(keys, key)
		}
	}
	if len(keys) == 0 {
		return nil, 0, errors.New("jwks: no usable signing key")
	}
	return keys, k.ttl(header), nil
}

// usable returns the public half of a signing key, and false for a key that
// is not one: a symmetric key, an encryption key, an RSA key under 2048 bits.
func usable(key jose.JSONWebKey) (jose.JSONWebKey, bool) {
	if key.Use != "" && key.Use != "sig" {
		return key, false
	}
	if !key.IsPublic() {
		key = key.Public()
	}
	if !key.Valid() {
		return key, false
	}
	switch pub := key.Key.(type) {
	case *rsa.PublicKey:
		return key, pub.N.BitLen() >= minRSABits
	case *ecdsa.PublicKey, ed25519.PublicKey:
		return key, true
	}
	return key, false
}

// ttl is how long a set fetched with these headers may be used: its
// Cache-Control max-age, or Expires, within [minInterval, maxAge]; maxAge
// when neither is given.
func (k *keyCache) ttl(h http.Header) time.Duration {
	ttl := k.maxAge
	cc := strings.ToLower(h.Get("Cache-Control"))
	found := false
	for d := range strings.SplitSeq(cc, ",") {
		d = strings.TrimSpace(d)
		switch {
		case d == "no-store" || d == "no-cache":
			return k.minInterval
		case strings.HasPrefix(d, "max-age="):
			if n, err := strconv.ParseInt(strings.Trim(d[len("max-age="):], `"`), 10, 64); err == nil && n >= 0 {
				ttl, found = time.Duration(min(n, int64(k.maxAge/time.Second)))*time.Second, true
			}
		}
	}
	if !found {
		if exp, err := http.ParseTime(h.Get("Expires")); err == nil {
			date, err := http.ParseTime(h.Get("Date"))
			if err != nil {
				date = k.clock.Now()
			}
			ttl = exp.Sub(date)
		}
	}
	return min(max(ttl, k.minInterval), k.maxAge)
}

// discover reads the jwks_uri of the issuer's OpenID configuration, whose
// issuer must be ours.
func (k *keyCache) discover(ctx context.Context) (string, error) {
	body, _, err := k.get(ctx, strings.TrimSuffix(k.issuer, "/")+"/.well-known/openid-configuration")
	if err != nil {
		return "", fmt.Errorf("openid configuration: %w", err)
	}
	var doc struct {
		Issuer  string `json:"issuer"`
		JWKSURI string `json:"jwks_uri"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return "", errors.New("openid configuration: not a JSON object")
	}
	if doc.Issuer != k.issuer {
		return "", errors.New("openid configuration: its issuer is not the configured one")
	}
	if err := checkURL(doc.JWKSURI); err != nil {
		return "", fmt.Errorf("openid configuration: jwks_uri: %w", err)
	}
	return doc.JWKSURI, nil
}

// get fetches url and returns its body, at most maxDocument bytes.
func (k *keyCache) get(ctx context.Context, url string) ([]byte, http.Header, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := k.client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxDocument+1))
	if err != nil {
		return nil, nil, err
	}
	if len(body) > maxDocument {
		return nil, nil, fmt.Errorf("larger than %d bytes", maxDocument)
	}
	return body, resp.Header, nil
}
