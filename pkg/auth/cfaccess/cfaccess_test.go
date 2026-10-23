package cfaccess_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/auth"
	"github.com/bsamanvith/agentkit/pkg/auth/authtest"
	"github.com/bsamanvith/agentkit/pkg/auth/cfaccess"
	"github.com/bsamanvith/agentkit/pkg/auth/jwt/jwttest"
	"github.com/bsamanvith/agentkit/pkg/clock/clocktest"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

// aud is the Access application's audience tag every rig expects.
const aud = "4714c1358e65fe4b408ad6d432a5f878f08194bdb4752441fd56faefa9b2b6f2"

var t0 = time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)

// rig is an Authenticator over a fake team domain (an httptest server that
// publishes one RS256 key at /cdn-cgi/access/certs) and a fake clock.
type rig struct {
	t   *testing.T
	iss *jwttest.Issuer
	kid string
	clk *clocktest.Fake
	a   *cfaccess.Authenticator
}

func newRig(t *testing.T, mods ...func(*cfaccess.Config)) *rig {
	t.Helper()
	iss := jwttest.NewIssuer(t)
	r := &rig{t: t, iss: iss, kid: iss.AddKey(t, "RS256"), clk: clocktest.NewFake(t0)}
	cfg := cfaccess.Config{TeamDomain: iss.URL(), AUD: aud, Tenant: "default"}
	for _, m := range mods {
		m(&cfg)
	}
	a, err := cfaccess.New(cfg, cfaccess.WithClock(r.clk), cfaccess.WithHTTPClient(iss.Client()))
	if err != nil {
		t.Fatal(err)
	}
	r.a = a
	return r
}

// claims are an Access assertion's claims for owner@example.com, changed by
// over: a nil value removes the claim.
func (r *rig) claims(over map[string]any) map[string]any {
	now := r.clk.Now().Unix()
	c := map[string]any{
		"iss": r.iss.URL(), "aud": []string{aud}, "sub": "6f1b8c1e-0b0e-4d1c-9a0e-6e1b5f3c2a10",
		"email": "owner@example.com", "type": "app", "identity_nonce": "n",
		"iat": now, "nbf": now, "exp": now + 3600,
	}
	for k, v := range over {
		if v == nil {
			delete(c, k)
		} else {
			c[k] = v
		}
	}
	return c
}

func (r *rig) assertion(over map[string]any) string {
	return r.iss.Sign(r.t, r.kid, r.claims(over))
}

func request(assertion string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/v1/runs", nil)
	if assertion != "" {
		req.Header.Set(cfaccess.Header, assertion)
	}
	return req
}

func TestValidAssertionGivesTheOwner(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	p, err := r.a.Authenticate(request(r.assertion(nil)))
	if err != nil {
		t.Fatal(err)
	}
	want := auth.Principal{
		Tenant: mustTenant(t, "default"), User: "owner@example.com",
		Scopes: []string{"*"}, Toolsets: []string{"*"},
		Via: auth.Credential{Kind: cfaccess.KindCFAccess, ID: r.iss.URL()},
	}
	if !authtest.Equal(p, want) {
		t.Errorf("got %+v, want %+v", p, want)
	}
}

func TestConfiguredScopesAndToolsets(t *testing.T) {
	t.Parallel()
	r := newRig(t, func(c *cfaccess.Config) {
		c.Tenant, c.Scopes, c.Toolsets = "acme", []string{"agent:run"}, []string{"crm"}
	})
	// Scope claims in the assertion change nothing.
	p, err := r.a.Authenticate(request(r.assertion(map[string]any{"scope": "agent:admin mcp:gmail"})))
	if err != nil {
		t.Fatal(err)
	}
	if p.Tenant.ID() != "acme" || !p.HasScope("agent:run") || p.HasScope("agent:admin") || !p.AllowsToolset("crm") || p.AllowsToolset("gmail") {
		t.Errorf("got %+v", p)
	}
}

func TestRefusedAssertions(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	other := jwttest.NewIssuer(t)
	otherKid := other.AddKey(t, "RS256")
	esKid := r.iss.AddKey(t, "ES256")
	now := t0.Unix()
	for name, tok := range map[string]string{
		"wrong aud":          r.assertion(map[string]any{"aud": []string{"another-app"}}),
		"no aud":             r.assertion(map[string]any{"aud": nil}),
		"wrong iss":          r.assertion(map[string]any{"iss": "https://evil.cloudflareaccess.com"}),
		"expired":            r.assertion(map[string]any{"exp": now - 120}),
		"no exp":             r.assertion(map[string]any{"exp": nil}),
		"not valid yet":      r.assertion(map[string]any{"nbf": now + 120}),
		"no email":           r.assertion(map[string]any{"email": nil, "common_name": "abc.access"}),
		"another team's key": other.Sign(t, otherKid, r.claims(nil)),
		"ES256, not RS256":   r.iss.Sign(t, esKid, r.claims(nil)),
		"not a JWT":          "not-a-jwt",
		"tampered":           r.assertion(nil) + "A",
	} {
		_, err := r.a.Authenticate(request(tok))
		if !errors.Is(err, auth.ErrInvalidCredentials) {
			t.Errorf("%s: got %v, want invalid credentials", name, err)
		}
		if err != nil && strings.Contains(err.Error(), "owner@example.com") {
			t.Errorf("%s: the error holds the email: %v", name, err)
		}
	}
}

func TestLeewayOnTimes(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	now := t0.Unix()
	if _, err := r.a.Authenticate(request(r.assertion(map[string]any{"exp": now - 30, "nbf": now + 30}))); err != nil {
		t.Errorf("within the leeway: %v", err)
	}
}

func TestNoHeaderIsNoCredentials(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	req := request("")
	// A bearer token is not an assertion: it is left to the next authenticator.
	req.Header.Set("Authorization", "Bearer "+r.assertion(nil))
	if _, err := r.a.Authenticate(req); !errors.Is(err, auth.ErrNoCredentials) || errors.Is(err, auth.ErrInvalidCredentials) {
		t.Errorf("got %v, want no credentials", err)
	}
	req = request(r.assertion(nil))
	req.Header.Add(cfaccess.Header, r.assertion(nil))
	if _, err := r.a.Authenticate(req); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Errorf("two headers: got %v, want invalid credentials", err)
	}
}

func TestEmails(t *testing.T) {
	t.Parallel()
	r := newRig(t, func(c *cfaccess.Config) { c.Emails = []string{"Owner@Example.com"} })
	if _, err := r.a.Authenticate(request(r.assertion(nil))); err != nil {
		t.Errorf("listed email (another case): %v", err)
	}
	_, err := r.a.Authenticate(request(r.assertion(map[string]any{"email": "intruder@example.com"})))
	if !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Errorf("unlisted email: got %v, want invalid credentials", err)
	}
}

func TestKeysAreCached(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	for range 5 {
		if _, err := r.a.Authenticate(request(r.assertion(nil))); err != nil {
			t.Fatal(err)
		}
	}
	if n := r.iss.Fetches(); n != 1 {
		t.Errorf("%d fetches of the certs, want 1", n)
	}
	// A rotated key is found by fetching again, once the minimum interval
	// has passed.
	r.clk.Advance(2 * time.Minute)
	kid := r.iss.AddKey(t, "RS256")
	if _, err := r.a.Authenticate(request(r.iss.Sign(t, kid, r.claims(nil)))); err != nil {
		t.Errorf("rotated key: %v", err)
	}
}

func TestRequire(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	public := func(req *http.Request) bool { return req.URL.Path == "/healthz" }
	var reached int
	h := r.a.Require(public)(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		reached++
		if req.Header.Get("Authorization") != "Bearer k" {
			t.Errorf("the handler lost the request's Authorization header")
		}
	}))
	send := func(method, path, assertion string, header map[string]string) int {
		req := httptest.NewRequest(method, "https://agents.example.com"+path, nil)
		req.Header.Set("Authorization", "Bearer k")
		if assertion != "" {
			req.Header.Set(cfaccess.Header, assertion)
		}
		for k, v := range header {
			req.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w.Code
	}
	good := r.assertion(nil)
	for _, c := range []struct {
		name, method, path, assertion string
		header                        map[string]string
		want                          int
	}{
		{"no assertion", "GET", "/v1/runs", "", nil, http.StatusUnauthorized},
		{"bad assertion", "GET", "/v1/runs", r.assertion(map[string]any{"aud": "x"}), nil, http.StatusUnauthorized},
		{"public path", "GET", "/healthz", "", nil, http.StatusOK},
		{"valid", "GET", "/v1/runs", good, nil, http.StatusOK},
		{"valid POST, no browser headers", "POST", "/v1/runs", good, nil, http.StatusOK},
		{"valid POST, same origin", "POST", "/v1/runs", good, map[string]string{"Sec-Fetch-Site": "same-origin"}, http.StatusOK},
		{"valid POST, cross-site", "POST", "/v1/runs", good, map[string]string{"Sec-Fetch-Site": "cross-site"}, http.StatusForbidden},
		{"valid POST, other Origin", "POST", "/v1/runs", good, map[string]string{"Origin": "https://evil.example"}, http.StatusForbidden},
	} {
		before := reached
		if got := send(c.method, c.path, c.assertion, c.header); got != c.want {
			t.Errorf("%s: got %d, want %d", c.name, got, c.want)
		}
		if ok := reached > before; ok != (c.want == http.StatusOK) {
			t.Errorf("%s: handler reached = %v", c.name, ok)
		}
	}
}

func TestNewRefuses(t *testing.T) {
	t.Parallel()
	for name, cfg := range map[string]cfaccess.Config{
		"no team":         {AUD: aud, Tenant: "default"},
		"no aud":          {TeamDomain: "myteam", Tenant: "default"},
		"no tenant":       {TeamDomain: "myteam", AUD: aud},
		"http team":       {TeamDomain: "http://myteam.cloudflareaccess.com", AUD: aud, Tenant: "default"},
		"team with path":  {TeamDomain: "https://myteam.cloudflareaccess.com/x", AUD: aud, Tenant: "default"},
		"team with slash": {TeamDomain: "myteam.cloudflareaccess.com/x", AUD: aud, Tenant: "default"},
		"empty email":     {TeamDomain: "myteam", AUD: aud, Tenant: "default", Emails: []string{" "}},
	} {
		if _, err := cfaccess.New(cfg); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestTeamDomainForms(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"myteam":                               "https://myteam.cloudflareaccess.com",
		"myteam.cloudflareaccess.com":          "https://myteam.cloudflareaccess.com",
		"MyTeam.CloudflareAccess.com":          "https://myteam.cloudflareaccess.com",
		"https://myteam.cloudflareaccess.com":  "https://myteam.cloudflareaccess.com",
		"https://myteam.cloudflareaccess.com/": "https://myteam.cloudflareaccess.com",
	} {
		a, err := cfaccess.New(cfaccess.Config{TeamDomain: in, AUD: aud, Tenant: "default"})
		if err != nil {
			t.Errorf("%s: %v", in, err)
			continue
		}
		if a.Issuer() != want {
			t.Errorf("%s: issuer %q, want %q", in, a.Issuer(), want)
		}
	}
}

func mustTenant(t testing.TB, id string) tenancy.Tenant {
	t.Helper()
	tn, err := tenancy.NewTenant(id)
	if err != nil {
		t.Fatal(err)
	}
	return tn
}
