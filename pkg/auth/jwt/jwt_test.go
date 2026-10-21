package jwt_test

import (
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"

	"github.com/bsamanvith/agentkit/pkg/auth"
	"github.com/bsamanvith/agentkit/pkg/auth/authtest"
	"github.com/bsamanvith/agentkit/pkg/auth/jwt"
	"github.com/bsamanvith/agentkit/pkg/auth/jwt/jwttest"
)

func TestValidTokenGivesMappedPrincipal(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		cfg  func(*jwt.Config)
		alg  string // the key's, default ES256
		over map[string]any
		path string
		want auth.Principal
	}{
		{
			name: "default claims",
			want: auth.Principal{User: "ann", Scopes: []string{"agent:run", "agent:read", "mcp:crm"}, Toolsets: []string{"crm"}},
		},
		{
			name: "scp array, aud array",
			over: map[string]any{"scope": nil, "scp": []string{"agent:read", "mcp:gmail", "mcp:crm"}, "aud": []string{"https://other.example", audience}},
			want: auth.Principal{User: "ann", Scopes: []string{"agent:read", "mcp:gmail", "mcp:crm"}, Toolsets: []string{"gmail", "crm"}},
		},
		{
			name: "both scope and scp, duplicates once",
			over: map[string]any{"scope": "a b", "scp": []string{"b", "c"}},
			want: auth.Principal{User: "ann", Scopes: []string{"a", "b", "c"}},
		},
		{
			name: "patterns in claims are dropped",
			over: map[string]any{"scope": "* admin:* mcp:* mcp:crm-? agent:run [a] x\\y"},
			want: auth.Principal{User: "ann", Scopes: []string{"agent:run"}},
		},
		{
			name: "fixed tenant: the claim is not read",
			cfg:  func(c *jwt.Config) { c.Tenant = "globex" },
			over: map[string]any{"tenant": "acme"},
			want: auth.Principal{Tenant: mustTenant(t, "globex"), User: "ann", Scopes: []string{"agent:run", "agent:read", "mcp:crm"}, Toolsets: []string{"crm"}},
		},
		{
			name: "fixed tenant: no claim needed",
			cfg:  func(c *jwt.Config) { c.Tenant = "globex" },
			over: map[string]any{"tenant": nil},
			want: auth.Principal{Tenant: mustTenant(t, "globex"), User: "ann", Scopes: []string{"agent:run", "agent:read", "mcp:crm"}, Toolsets: []string{"crm"}},
		},
		{
			name: "claim names configured",
			cfg: func(c *jwt.Config) {
				c.TenantClaim, c.UserClaim, c.ScopesClaim, c.ToolsetsClaim, c.ToolsetScopePrefix = "org", "email", "permissions", "toolsets", "tools/"
			},
			over: map[string]any{"org": "globex", "email": "bo@globex.example", "permissions": []string{"agent:run", "tools/gmail"}, "toolsets": "search crm"},
			want: auth.Principal{Tenant: mustTenant(t, "globex"), User: "bo@globex.example", Scopes: []string{"agent:run", "tools/gmail"}, Toolsets: []string{"gmail", "search", "crm"}},
		},
		{
			name: "no sub: no user",
			over: map[string]any{"sub": nil, "scope": nil},
			want: auth.Principal{},
		},
		{
			name: "aud is the MCP endpoint asked for (RFC 8707)",
			over: map[string]any{"aud": audience + "/mcp/crm"},
			path: "/mcp/crm",
			want: auth.Principal{User: "ann", Scopes: []string{"agent:run", "agent:read", "mcp:crm"}, Toolsets: []string{"crm"}},
		},
		{
			name: "aud is a prefix of the path asked for",
			over: map[string]any{"aud": audience + "/mcp/"},
			path: "/mcp/crm",
			want: auth.Principal{User: "ann", Scopes: []string{"agent:run", "agent:read", "mcp:crm"}, Toolsets: []string{"crm"}},
		},
		{
			name: "no nbf, no iat",
			over: map[string]any{"nbf": nil, "iat": nil},
			want: auth.Principal{User: "ann", Scopes: []string{"agent:run", "agent:read", "mcp:crm"}, Toolsets: []string{"crm"}},
		},
		{
			name: "RS256",
			alg:  "RS256",
			want: auth.Principal{User: "ann", Scopes: []string{"agent:run", "agent:read", "mcp:crm"}, Toolsets: []string{"crm"}},
		},
		{
			name: "EdDSA when allowed",
			cfg:  func(c *jwt.Config) { c.Algorithms = []string{"EdDSA"} },
			alg:  "EdDSA",
			want: auth.Principal{User: "ann", Scopes: []string{"agent:run", "agent:read", "mcp:crm"}, Toolsets: []string{"crm"}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			mods := []func(*jwt.Config){}
			if tc.cfg != nil {
				mods = append(mods, tc.cfg)
			}
			r := newRig(t, mods...)
			kid := r.kid
			if tc.alg != "" {
				kid = r.iss.AddKey(t, tc.alg)
			}
			tok := r.track(r.iss.Sign(t, kid, r.claims(tc.over)))
			path := tc.path
			if path == "" {
				path = "/v1/runs"
			}
			want := tc.want
			if want.Tenant.ID() == "" {
				want.Tenant = mustTenant(t, "acme")
			}
			want.Via = auth.Credential{Kind: jwt.KindJWT, ID: r.iss.URL()}
			res := r.do(tok, path)
			if res.status != http.StatusOK || !authtest.Equal(res.p, want) {
				t.Errorf("got %d %+v, want 200 %+v", res.status, res.p, want)
			}
		})
	}
}

func TestRejectedTokens(t *testing.T) {
	t.Parallel()
	r := newRig(t, func(c *jwt.Config) { c.Algorithms = []string{"ES256", "RS256", "PS256"} })
	ed := r.iss.AddKey(t, "EdDSA")    // published, but EdDSA is not allowed
	weak := r.iss.AddWeakRSAKey(t)    // published, 1024 bits
	other := r.iss.AddKey(t, "ES256") // signs, but not in the JWKS
	r.iss.Unpublish(other)
	rsa := r.iss.AddKey(t, "RS256")
	c := r.claims(nil)
	hdr := map[string]any{"alg": "ES256", "kid": r.kid, "typ": "JWT"}

	// The public key's bytes, as JWK and as PEM: what an attacker would use
	// as an HMAC secret.
	var jwk jose.JSONWebKey
	if err := json.Unmarshal(r.iss.PublicJWK(t, rsa), &jwk); err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(jwk.Key)
	if err != nil {
		t.Fatal(err)
	}
	pemKey := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})

	cases := map[string]string{
		"bad signature (another key, our kid)": r.iss.SignAs(t, other, r.kid, c),
		"bad signature (bytes changed)":        swapSig(r.iss.Sign(t, r.kid, c), r.iss.Sign(t, r.kid, with(c, "sub", "bob"))),
		"alg none":                             jwttest.Compact(with(hdr, "alg", "none"), c, nil),
		"alg none, with a signature":           jwttest.Compact(with(hdr, "alg", "none"), c, []byte("sig")),
		"alg None":                             jwttest.Compact(with(hdr, "alg", "None"), c, nil),
		"HS256 with the public JWK":            hs256(map[string]any{"alg": "HS256", "kid": rsa, "typ": "JWT"}, c, r.iss.PublicJWK(t, rsa)),
		"HS256 with the public PEM":            hs256(map[string]any{"alg": "HS256", "kid": rsa, "typ": "JWT"}, c, pemKey),
		"HS256 with the public PEM, no kid":    hs256(map[string]any{"alg": "HS256", "typ": "JWT"}, c, pemKey),
		"wrong iss":                            r.token(map[string]any{"iss": "https://evil.example"}),
		"iss with a trailing slash":            r.token(map[string]any{"iss": r.iss.URL() + "/"}),
		"no iss":                               r.token(map[string]any{"iss": nil}),
		"aud not ours":                         r.token(map[string]any{"aud": "https://other.example"}),
		"aud ours as a host prefix":            r.token(map[string]any{"aud": audience + ".evil.example"}),
		"aud ours as a host prefix, our path":  r.token(map[string]any{"aud": audience + ".evil.example/mcp/crm"}),
		"aud ours as a port prefix, our path":  r.token(map[string]any{"aud": audience + ":8443/mcp/crm"}),
		"aud for another MCP endpoint":         r.token(map[string]any{"aud": audience + "/mcp/gmail"}),
		"aud list without ours":                r.token(map[string]any{"aud": []string{"a", "b"}}),
		"no aud":                               r.token(map[string]any{"aud": nil}),
		"aud not a string":                     r.token(map[string]any{"aud": 7}),
		"expired beyond the leeway":            r.token(map[string]any{"exp": t0.Add(-61 * time.Second).Unix()}),
		"nbf beyond the leeway":                r.token(map[string]any{"nbf": t0.Add(61 * time.Second).Unix()}),
		"iat beyond the leeway":                r.token(map[string]any{"iat": t0.Add(61 * time.Second).Unix()}),
		"no exp":                               r.token(map[string]any{"exp": nil}),
		"exp a string":                         r.token(map[string]any{"exp": fmt.Sprint(t0.Add(time.Hour).Unix())}),
		"exp huge":                             r.token(map[string]any{"exp": 1e300}),
		"unknown kid after a refresh":          r.iss.SignAs(t, other, "no-such-kid", c),
		"no kid":                               r.iss.SignAs(t, r.kid, "", c), // the set has several keys
		"an algorithm not allowed (EdDSA)":     r.iss.Sign(t, ed, c),
		"an RSA key under 2048 bits":           r.iss.Sign(t, weak, c),
		"an algorithm the key's alg is not":    r.iss.SignAlg(t, rsa, "PS256", c), // the JWKS says RS256
		"kid of a key of another type":         r.iss.SignAs(t, other, rsa, c),
		"no tenant claim":                      r.token(map[string]any{"tenant": nil}),
		"tenant not a string":                  r.token(map[string]any{"tenant": []string{"acme"}}),
		"invalid tenant id":                    r.token(map[string]any{"tenant": "../globex"}),
		"empty tenant id":                      r.token(map[string]any{"tenant": ""}),
		"user with a control character":        r.token(map[string]any{"sub": "ann\nadmin"}),
		"scope not a string or list":           r.token(map[string]any{"scope": 1}),
		"claims not an object":                 r.track(signRaw(t, r, []byte(`["iss"]`))),
		"claims with trailing data":            r.track(signRaw(t, r, append(mustJSON(t, c), []byte(`{}`)...))),
	}
	for name, tok := range cases {
		r.track(tok)
		res := r.do(tok, "/mcp/crm")
		wantInvalid(t, name, res)
		if _, err := r.a.Authenticate(bearer(tok)); !errors.Is(err, auth.ErrInvalidCredentials) {
			t.Errorf("%s: Authenticate = %v, want ErrInvalidCredentials", name, err)
		}
	}
	// The same claims with our key pass: each case fails for its one reason.
	if res := r.do(r.token(nil), "/mcp/crm"); res.status != http.StatusOK {
		t.Fatalf("a valid token: %d", res.status)
	}
}

func TestNotAJWTIsNoCredentials(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	for _, tok := range []string{"0123456789abcdef0123456789abcdef", "a.b", "a.b.c.d", ".b.c", "a.b!.c", "dev-token-0123456789"} {
		if _, err := r.a.Authenticate(bearer(tok)); !errors.Is(err, auth.ErrNoCredentials) {
			t.Errorf("%q: got %v, want ErrNoCredentials", tok, err)
		}
	}
	// So in a chain an API key reaches the API keys.
	const key = "0123456789abcdef0123456789abcdef"
	e, err := auth.KeyEntry(auth.APIKey{ID: "ci", Tenant: "globex", Scopes: []string{"*"}}, key)
	if err != nil {
		t.Fatal(err)
	}
	keys, err := auth.NewAPIKeys([]auth.APIKey{e})
	if err != nil {
		t.Fatal(err)
	}
	chain := auth.Chain(r.a, keys)
	if p, err := chain.Authenticate(bearer(key)); err != nil || p.Tenant.ID() != "globex" {
		t.Errorf("an API key through Chain(jwt, keys): %+v, %v", p, err)
	}
	if p, err := chain.Authenticate(bearer(r.token(nil))); err != nil || p.Tenant.ID() != "acme" {
		t.Errorf("a JWT through Chain(jwt, keys): %+v, %v", p, err)
	}
	// A bad JWT stops the chain: it is not tried as a key.
	if _, err := chain.Authenticate(bearer(r.token(map[string]any{"iss": "x"}))); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Errorf("a bad JWT through the chain: %v, want ErrInvalidCredentials", err)
	}
	if _, err := r.a.Authenticate(httptest.NewRequest(http.MethodGet, "/", nil)); !errors.Is(err, auth.ErrNoCredentials) {
		t.Errorf("no header: %v, want ErrNoCredentials", err)
	}
}

func TestKeyRotation(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	if res := r.do(r.token(nil), "/v1/runs"); res.status != http.StatusOK {
		t.Fatalf("first token: %d", res.status)
	}
	if n := r.iss.Fetches(); n != 1 {
		t.Fatalf("fetches after the first token = %d, want 1", n)
	}
	// Tokens of a known key fetch nothing.
	for range 5 {
		r.do(r.token(nil), "/v1/runs")
	}
	if n := r.iss.Fetches(); n != 1 {
		t.Fatalf("fetches after known keys = %d, want 1", n)
	}

	r.clk.Advance(jwt.DefaultMinRefreshInterval)
	next := r.iss.AddKey(t, "ES256")
	tok := r.track(r.iss.Sign(t, next, r.claims(nil)))
	if res := r.do(tok, "/v1/runs"); res.status != http.StatusOK {
		t.Fatalf("the first token of the new key: %d %q", res.status, res.challenge)
	}
	if n := r.iss.Fetches(); n != 2 {
		t.Fatalf("fetches after the new kid = %d, want 2", n)
	}
	if res := r.do(tok, "/v1/runs"); res.status != http.StatusOK || r.iss.Fetches() != 2 {
		t.Fatalf("the new key again: %d, fetches %d", res.status, r.iss.Fetches())
	}

	// A burst of 100 unknown kids, concurrently, within the interval: none
	// fetches, as the last fetch was just now.
	stranger := r.iss.AddKey(t, "ES256")
	r.iss.Unpublish(stranger)
	burst := func() {
		var wg sync.WaitGroup
		for i := range 100 {
			wg.Go(func() {
				tok := r.track(r.iss.SignAs(t, stranger, fmt.Sprintf("fake-%d", i), r.claims(nil)))
				if _, err := r.a.Authenticate(bearer(tok)); !errors.Is(err, auth.ErrInvalidCredentials) {
					t.Errorf("unknown kid %d: %v", i, err)
				}
			})
		}
		wg.Wait()
	}
	burst()
	if n := r.iss.Fetches(); n != 2 {
		t.Errorf("fetches after 100 unknown kids right after a fetch = %d, want 2", n)
	}
	// Once the interval has passed, a burst makes exactly one fetch.
	r.clk.Advance(jwt.DefaultMinRefreshInterval)
	burst()
	if n := r.iss.Fetches(); n != 3 {
		t.Errorf("fetches after 100 unknown kids after the interval = %d, want 3", n)
	}
	// One second short of the interval: still none.
	r.clk.Advance(jwt.DefaultMinRefreshInterval - time.Second)
	burst()
	if n := r.iss.Fetches(); n != 3 {
		t.Errorf("fetches within the interval = %d, want 3", n)
	}

	// A key taken out of the set stops working once the set is fetched
	// again.
	r.iss.Unpublish(r.kid)
	r.clk.Advance(jwt.DefaultMaxCacheAge)
	if res := r.do(r.token(nil), "/v1/runs"); res.status != http.StatusUnauthorized {
		t.Errorf("a token of a key taken out of the set: %d, want 401", res.status)
	}
}

func TestJWKSEndpointDown(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	if res := r.do(r.token(nil), "/v1/runs"); res.status != http.StatusOK {
		t.Fatalf("first token: %d", res.status)
	}
	r.iss.SetDown(true)
	next := r.iss.AddKey(t, "ES256")
	// Long past the cache age: the set is stale, every fetch fails.
	for range 3 {
		r.clk.Advance(2 * jwt.DefaultMaxCacheAge)
		if res := r.do(r.token(nil), "/v1/runs"); res.status != http.StatusOK {
			t.Errorf("a cached key with the provider down: %d, want 200", res.status)
		}
		wantInvalid(t, "a new key with the provider down", r.do(r.track(r.iss.Sign(t, next, r.claims(nil))), "/v1/runs"))
	}
	if !strings.Contains(r.logs.String(), "keeping the last good set") {
		t.Errorf("the failed fetch was not logged: %s", r.logs.String())
	}
	if err := r.a.Refresh(t.Context()); err != nil {
		t.Errorf("Refresh within the interval = %v, want nil (skipped)", err)
	}
	r.clk.Advance(jwt.DefaultMinRefreshInterval)
	if err := r.a.Refresh(t.Context()); err == nil {
		t.Errorf("Refresh with the provider down = nil, want an error")
	}

	// Back up: the new key works after the interval.
	r.iss.SetDown(false)
	r.clk.Advance(jwt.DefaultMinRefreshInterval)
	if res := r.do(r.track(r.iss.Sign(t, next, r.claims(nil))), "/v1/runs"); res.status != http.StatusOK {
		t.Errorf("the new key with the provider back: %d", res.status)
	}

	// Down from the start: 401, never 500.
	r2 := newRig(t)
	r2.iss.SetDown(true)
	wantInvalid(t, "provider down from the start", r2.do(r2.token(nil), "/v1/runs"))
}

func TestLeewayBoundaries(t *testing.T) {
	t.Parallel()
	const leeway = 60 * time.Second
	// Each case: the claim's time relative to the clock, whether it passes.
	for _, tc := range []struct {
		name  string
		claim string
		at    time.Duration // claim time minus now
		ok    bool
	}{
		{"exp just inside the leeway", "exp", -leeway + time.Second, true},
		{"exp exactly at the leeway", "exp", -leeway, false},
		{"exp now", "exp", 0, true},
		{"nbf exactly at the leeway", "nbf", leeway, true},
		{"nbf just beyond the leeway", "nbf", leeway + time.Second, false},
		{"iat exactly at the leeway", "iat", leeway, true},
		{"iat just beyond the leeway", "iat", leeway + time.Second, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t)
			now := r.clk.Now()
			over := map[string]any{tc.claim: now.Add(tc.at).Unix()}
			if tc.claim != "exp" {
				over["iat"], over["nbf"] = now.Unix(), now.Unix()
				over[tc.claim] = now.Add(tc.at).Unix()
			}
			res := r.do(r.token(over), "/v1/runs")
			if got := res.status == http.StatusOK; got != tc.ok {
				t.Errorf("%s at now%+v: accepted = %v, want %v", tc.claim, tc.at, got, tc.ok)
			}
		})
	}
	// The same, moving the clock instead of the claims: exp at t0+1h.
	r := newRig(t)
	tok := r.token(map[string]any{"exp": t0.Add(time.Hour).Unix()})
	r.clk.Advance(time.Hour + leeway - time.Nanosecond)
	if res := r.do(tok, "/v1/runs"); res.status != http.StatusOK {
		t.Errorf("1ns before exp+leeway: %d, want 200", res.status)
	}
	r.clk.Advance(time.Nanosecond)
	wantInvalid(t, "at exp+leeway", r.do(tok, "/v1/runs"))

	// A fractional exp counts to the nanosecond.
	r2 := newRig(t)
	tok = r2.token(map[string]any{"exp": float64(t0.Unix()) + 0.5})
	r2.clk.Advance(leeway + 499*time.Millisecond)
	if res := r2.do(tok, "/v1/runs"); res.status != http.StatusOK {
		t.Errorf("before a fractional exp+leeway: %d, want 200", res.status)
	}
	r2.clk.Advance(time.Millisecond)
	wantInvalid(t, "at a fractional exp+leeway", r2.do(tok, "/v1/runs"))

	// A configured leeway.
	r3 := newRig(t, func(c *jwt.Config) { c.Leeway = 5 * time.Second })
	wantInvalid(t, "exp 5s ago with a 5s leeway", r3.do(r3.token(map[string]any{"exp": t0.Add(-5 * time.Second).Unix()}), "/v1/runs"))
	if res := r3.do(r3.token(map[string]any{"exp": t0.Add(-4 * time.Second).Unix()}), "/v1/runs"); res.status != http.StatusOK {
		t.Errorf("exp 4s ago with a 5s leeway: %d", res.status)
	}
}

func TestCacheHeaders(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, header, value string
		ttl                 time.Duration // when the set goes stale
	}{
		{"no header: the max age", "", "", jwt.DefaultMaxCacheAge},
		{"max-age honored", "Cache-Control", "public, max-age=300", 300 * time.Second},
		{"max-age under the interval: the interval", "Cache-Control", "max-age=5", jwt.DefaultMinRefreshInterval},
		{"max-age over the max age: the max age", "Cache-Control", "max-age=86400", jwt.DefaultMaxCacheAge},
		{"no-store: the interval", "Cache-Control", "no-store", jwt.DefaultMinRefreshInterval},
		{"Expires, from the response's Date", "Expires", t0.Add(10 * time.Minute).Format(http.TimeFormat), 10 * time.Minute},
		{"malformed max-age: the max age", "Cache-Control", "max-age=soon", jwt.DefaultMaxCacheAge},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t)
			if tc.header != "" {
				r.iss.SetHeader(tc.header, tc.value)
			}
			// The provider's clock is 5 minutes behind ours: Expires counts
			// from its Date.
			r.iss.SetHeader("Date", t0.Add(-5*time.Minute).Format(http.TimeFormat))
			if tc.header == "Expires" {
				r.iss.SetHeader("Expires", t0.Add(5*time.Minute).Format(http.TimeFormat))
			}
			if err := r.a.Refresh(t.Context()); err != nil {
				t.Fatal(err)
			}
			r.clk.Advance(tc.ttl - time.Second)
			r.do(r.token(map[string]any{"exp": r.clk.Now().Add(time.Hour).Unix()}), "/v1/runs")
			if n := r.iss.Fetches(); n != 1 {
				t.Errorf("1s before the set goes stale: %d fetches, want 1", n)
			}
			r.clk.Advance(time.Second)
			r.do(r.token(map[string]any{"exp": r.clk.Now().Add(time.Hour).Unix()}), "/v1/runs")
			if n := r.iss.Fetches(); n != 2 {
				t.Errorf("when the set goes stale: %d fetches, want 2", n)
			}
		})
	}
}

func TestDiscovery(t *testing.T) {
	t.Parallel()
	r := newRig(t, func(c *jwt.Config) { c.JWKSURL = "" })
	if res := r.do(r.token(nil), "/v1/runs"); res.status != http.StatusOK {
		t.Fatalf("with the JWKS URL discovered: %d", res.status)
	}
	if n := r.iss.Fetches(); n != 1 {
		t.Errorf("fetches = %d, want 1", n)
	}

	// An OpenID configuration naming another issuer is not used.
	liar := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"issuer": r.iss.URL(), "jwks_uri": r.iss.JWKSURL()})
	}))
	t.Cleanup(liar.Close)
	a, err := jwt.New(jwt.Config{Issuer: liar.URL, Audience: audience}, jwt.WithClock(r.clk))
	if err != nil {
		t.Fatal(err)
	}
	tok := r.track(r.iss.Sign(t, r.kid, r.claims(map[string]any{"iss": liar.URL})))
	if _, err := a.Authenticate(bearer(tok)); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Errorf("an OpenID configuration of another issuer: %v, want ErrInvalidCredentials", err)
	}
	if err := a.Refresh(t.Context()); err != nil {
		t.Errorf("Refresh right after a failed fetch = %v, want nil (skipped)", err)
	}
}

// TestEndpointAudienceNeedsACleanPath: a token for one MCP endpoint holds on
// that endpoint's paths, not on a path that only starts with it as text.
func TestEndpointAudienceNeedsACleanPath(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	tok := r.token(map[string]any{"aud": audience + "/mcp/crm"})
	for p, want := range map[string]int{
		"/mcp/crm":          http.StatusOK,
		"/mcp/crm/":         http.StatusOK,
		"/mcp/crm/x":        http.StatusOK,
		"/mcp/crm/../gmail": http.StatusUnauthorized,
		"/mcp/crm/./x":      http.StatusUnauthorized,
		"/mcp/crm//x":       http.StatusUnauthorized,
	} {
		if res := r.do(tok, p); res.status != want {
			t.Errorf("%s: %d, want %d", p, res.status, want)
		}
	}
	// The configured audience itself holds whatever the path.
	if res := r.do(r.token(nil), "/mcp/crm/../gmail"); res.status != http.StatusOK {
		t.Errorf("the configured audience on an unclean path: %d, want 200", res.status)
	}
}

// TestRedirectsHeldToTheURLRule:a fetch follows a redirect only to what a
// configured URL may be, so a provider's redirect to plain http on another
// host does not hand the keys to whoever is on the path.
func TestRedirectsHeldToTheURLRule(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, to string
		ok       bool
	}{
		{"to http on another host", "http://keys.example.com/jwks", false},
		{"to https on another host", "https://keys.example.com/jwks", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t)
			redirector := httptest.NewServer(http.RedirectHandler(tc.to, http.StatusFound))
			t.Cleanup(redirector.Close)
			// keys.example.com serves the issuer's JWKS, whatever the scheme.
			client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.URL.Host == "keys.example.com" {
					req = req.Clone(req.Context())
					req.URL.Scheme, req.URL.Host, req.URL.Path = "http", strings.TrimPrefix(r.iss.URL(), "http://"), jwttest.JWKSPath
				}
				return http.DefaultTransport.RoundTrip(req)
			})}
			a, err := jwt.New(jwt.Config{Issuer: r.iss.URL(), Audience: audience, JWKSURL: redirector.URL + "/jwks"},
				jwt.WithClock(r.clk), jwt.WithHTTPClient(client))
			if err != nil {
				t.Fatal(err)
			}
			_, err = a.Authenticate(bearer(r.token(nil)))
			if tc.ok && err != nil {
				t.Errorf("Authenticate = %v, want a principal", err)
			}
			if !tc.ok && !errors.Is(err, auth.ErrInvalidCredentials) {
				t.Errorf("Authenticate = %v, want ErrInvalidCredentials", err)
			}
		})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestConfigErrors(t *testing.T) {
	t.Parallel()
	ok := jwt.Config{Issuer: "https://id.example", Audience: audience}
	if _, err := jwt.New(ok); err != nil {
		t.Fatalf("a good config: %v", err)
	}
	for name, mod := range map[string]func(*jwt.Config){
		"no issuer":                 func(c *jwt.Config) { c.Issuer = "" },
		"http issuer, not loopback": func(c *jwt.Config) { c.Issuer = "http://id.example" },
		"issuer not absolute":       func(c *jwt.Config) { c.Issuer = "id.example" },
		"issuer with user info":     func(c *jwt.Config) { c.Issuer = "https://u:p@id.example" },
		"no audience":               func(c *jwt.Config) { c.Audience = "" },
		"http JWKS, not loopback":   func(c *jwt.Config) { c.JWKSURL = "http://id.example/jwks" },
		"alg none":                  func(c *jwt.Config) { c.Algorithms = []string{"none"} },
		"alg HS256":                 func(c *jwt.Config) { c.Algorithms = []string{"RS256", "HS256"} },
		"alg unknown":               func(c *jwt.Config) { c.Algorithms = []string{"rs256"} },
		"negative leeway":           func(c *jwt.Config) { c.Leeway = -time.Second },
		"leeway over 5m":            func(c *jwt.Config) { c.Leeway = 6 * time.Minute },
		"refresh interval under 1s": func(c *jwt.Config) { c.MinRefreshInterval = time.Millisecond },
		"max age under interval":    func(c *jwt.Config) { c.MaxCacheAge = time.Second },
		"invalid fixed tenant":      func(c *jwt.Config) { c.Tenant = "../x" },
	} {
		c := ok
		mod(&c)
		var ce *jwt.ConfigError
		if _, err := jwt.New(c); !errors.As(err, &ce) {
			t.Errorf("%s: got %v, want a *ConfigError", name, err)
		}
	}
	for _, loop := range []string{"http://127.0.0.1:9", "http://localhost:9", "http://[::1]:9"} {
		if _, err := jwt.New(jwt.Config{Issuer: loop, Audience: audience}); err != nil {
			t.Errorf("issuer %s: %v", loop, err)
		}
	}
}

// TestConformance runs the shared suite, with two tenants' tokens.
func TestConformance(t *testing.T) {
	t.Parallel()
	authtest.RunAuthenticator(t, func(t *testing.T) authtest.Subject {
		r := newRig(t)
		via := auth.Credential{Kind: jwt.KindJWT, ID: r.iss.URL()}
		return authtest.Subject{
			Authenticator: r.a,
			Credentials: []authtest.Credential{
				{
					Name: "acme's ann", Secret: r.token(nil), Apply: authtest.Bearer,
					Want: auth.Principal{Tenant: mustTenant(t, "acme"), User: "ann", Scopes: []string{"agent:run", "agent:read", "mcp:crm"}, Toolsets: []string{"crm"}, Via: via},
				},
				{
					Name: "globex's service", Secret: r.token(map[string]any{"tenant": "globex", "sub": nil, "scope": "mcp:gmail"}), Apply: authtest.Bearer,
					Want: auth.Principal{Tenant: mustTenant(t, "globex"), Scopes: []string{"mcp:gmail"}, Toolsets: []string{"gmail"}, Via: via},
				},
			},
		}
	})
}

func bearer(tok string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/v1/runs", nil)
	r.Header.Set("Authorization", "Bearer "+tok)
	return r
}

// swapSig returns a's header and claims with b's signature.
func swapSig(a, b string) string {
	return a[:strings.LastIndexByte(a, '.')] + b[strings.LastIndexByte(b, '.'):]
}

// signRaw signs payload, which need not be claims, with the rig's key.
func signRaw(t *testing.T, r *rig, payload []byte) string {
	t.Helper()
	return r.iss.SignPayload(t, r.kid, r.kid, payload)
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
