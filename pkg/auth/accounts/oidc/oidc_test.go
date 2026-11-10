package oidc_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/oauth2"

	"github.com/bsamanvith/agentkit/pkg/auth/accounts"
	"github.com/bsamanvith/agentkit/pkg/auth/accounts/oidc"
	"github.com/bsamanvith/agentkit/pkg/auth/jwt/jwttest"
	"github.com/bsamanvith/agentkit/pkg/clock/clocktest"
)

var t0 = time.Date(2026, 11, 10, 6, 0, 0, 0, time.UTC)

// provider is a fake OpenID provider: the issuer's keys from jwttest, and
// a token endpoint that checks the code, the PKCE verifier and the client,
// and answers with an ID token the test shapes.
type provider struct {
	t      *testing.T
	iss    *jwttest.Issuer
	kid    string
	tokens *httptest.Server

	mu        sync.Mutex
	codes     map[string]string // code -> S256 challenge
	claims    func(c map[string]any)
	lastForm  url.Values
	lastNonce string
}

func newProvider(t *testing.T) *provider {
	p := &provider{t: t, iss: jwttest.NewIssuer(t), codes: map[string]string{}}
	p.kid = p.iss.AddKey(t, "RS256")
	p.tokens = httptest.NewServer(http.HandlerFunc(p.token))
	t.Cleanup(p.tokens.Close)
	return p
}

func (p *provider) token(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	p.mu.Lock()
	defer p.mu.Unlock()
	p.lastForm = r.PostForm
	challenge, ok := p.codes[r.PostForm.Get("code")]
	delete(p.codes, r.PostForm.Get("code"))
	if !ok || r.PostForm.Get("client_secret") != "secret" || r.PostForm.Get("client_id") != "client-1" ||
		oauth2.S256ChallengeFromVerifier(r.PostForm.Get("code_verifier")) != challenge {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
		return
	}
	c := map[string]any{
		"iss": p.iss.URL(), "aud": "client-1", "sub": "1098765", "exp": t0.Add(time.Hour).Unix(), "iat": t0.Unix(),
		"email": "Lakshmi@Gmail.com", "email_verified": true, "name": "Lakshmi Devi", "nonce": p.lastNonce,
	}
	if p.claims != nil {
		p.claims(c)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "at", "token_type": "Bearer", "expires_in": 3600, "id_token": p.iss.Sign(p.t, p.kid, c)})
}

// authorize plays the provider's sign-in page: it reads the
// authorization URL and returns a code for it.
func (p *provider) authorize(authURL string) (state, code string) {
	u, err := url.Parse(authURL)
	if err != nil {
		p.t.Fatal(err)
	}
	q := u.Query()
	if q.Get("code_challenge_method") != "S256" || q.Get("response_type") != "code" || !strings.Contains(q.Get("scope"), "openid") {
		p.t.Fatalf("authorization URL lacks PKCE or openid: %s", authURL)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	code = "code-" + q.Get("state")[:8]
	p.codes[code] = q.Get("code_challenge")
	p.lastNonce = q.Get("nonce")
	return q.Get("state"), code
}

func (p *provider) lane(t *testing.T) *oidc.Lane {
	cfg := oidc.Config{
		Name: "google", Issuer: p.iss.URL(), ClientID: "client-1", ClientSecret: "secret",
		AuthURL: "https://accounts.example/auth", TokenURL: p.tokens.URL, JWKSURL: p.iss.JWKSURL(),
		RedirectURL: "http://127.0.0.1:8090/auth/google/callback",
	}
	l, err := oidc.New(cfg, oidc.WithClock(clocktest.NewFake(t0)))
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func flow() accounts.Flow {
	return accounts.Flow{State: "state-abcdefgh", Verifier: strings.Repeat("v", 50), Nonce: "nonce-1"}
}

func TestSignInGivesTheVerifiedIdentity(t *testing.T) {
	p := newProvider(t)
	l := p.lane(t)
	f := flow()
	_, code := p.authorize(l.AuthCodeURL(f))
	id, err := l.Finish(context.Background(), f, code)
	if err != nil {
		t.Fatal(err)
	}
	want := accounts.Identity{Lane: "google", Subject: "1098765", Email: "lakshmi@gmail.com", EmailVerified: true, Name: "Lakshmi Devi"}
	if id != want {
		t.Errorf("identity = %+v, want %+v", id, want)
	}
}

func TestBadIDTokensAreRefused(t *testing.T) {
	for name, edit := range map[string]func(c map[string]any){
		"another nonce":       func(c map[string]any) { c["nonce"] = "someone-elses" },
		"no nonce":            func(c map[string]any) { delete(c, "nonce") },
		"another audience":    func(c map[string]any) { c["aud"] = "client-2" },
		"another issuer":      func(c map[string]any) { c["iss"] = "https://evil.example" },
		"expired":             func(c map[string]any) { c["exp"] = t0.Add(-time.Hour).Unix() },
		"no subject":          func(c map[string]any) { delete(c, "sub") },
		"another azp":         func(c map[string]any) { c["azp"] = "client-2" },
		"several aud, no azp": func(c map[string]any) { c["aud"] = []string{"client-1", "client-2"} },
	} {
		t.Run(name, func(t *testing.T) {
			p := newProvider(t)
			p.claims = edit
			l := p.lane(t)
			f := flow()
			_, code := p.authorize(l.AuthCodeURL(f))
			if id, err := l.Finish(context.Background(), f, code); err == nil {
				t.Errorf("accepted: %+v", id)
			}
		})
	}
}

func TestTheVerifierAndCodeMustMatch(t *testing.T) {
	p := newProvider(t)
	l := p.lane(t)
	f := flow()
	_, code := p.authorize(l.AuthCodeURL(f))
	other := f
	other.Verifier = strings.Repeat("w", 50)
	_, err := l.Finish(context.Background(), other, code)
	if err == nil || !strings.Contains(err.Error(), "invalid_grant") {
		t.Errorf("a wrong verifier: %v, want the provider's invalid_grant", err)
	}
	if strings.Contains(err.Error(), code) {
		t.Errorf("the error holds the code: %v", err)
	}
}

func TestAnUnverifiedEmailIsNotMarkedVerified(t *testing.T) {
	p := newProvider(t)
	p.claims = func(c map[string]any) { c["email_verified"] = false }
	l := p.lane(t)
	f := flow()
	_, code := p.authorize(l.AuthCodeURL(f))
	id, err := l.Finish(context.Background(), f, code)
	if err != nil || id.EmailVerified {
		t.Errorf("identity %+v, %v: want an unverified email", id, err)
	}
}

func TestGooglePreset(t *testing.T) {
	c := oidc.Google("id", "secret", "https://farm.example/auth/google/callback")
	if c.AuthURL != "https://accounts.google.com/o/oauth2/v2/auth" || c.TokenURL != "https://oauth2.googleapis.com/token" || c.Issuer != oidc.GoogleIssuer {
		t.Errorf("Google endpoints = %+v", c)
	}
	l, err := oidc.New(c)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(l.AuthCodeURL(flow()))
	q := u.Query()
	if q.Get("prompt") != "select_account" || q.Get("access_type") != "" || q.Get("nonce") != "nonce-1" || q.Get("scope") != "openid email profile" {
		t.Errorf("Google authorization URL = %s", u)
	}
	if _, err := oidc.New(oidc.Google("", "", "https://x/cb")); err == nil {
		t.Error("no client: no error")
	}
	if _, err := oidc.New(oidc.Google("id", "s", "http://farm.example/cb")); err == nil {
		t.Error("a plain-http redirect off localhost: no error")
	}
}
