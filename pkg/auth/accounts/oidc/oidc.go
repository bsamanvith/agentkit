// Package oidc is the sign-in lane of an OpenID Connect provider: the
// authorization code flow with PKCE (S256) and a nonce, and the ID token
// verified against the provider's keys by pkg/auth/jwt (signature, issuer,
// audience the client id, times; then nonce, azp and the subject here).
// [Google] fills in Google's endpoints from the shared provider presets
// (pkg/credentials/providers), which pkg/mcp/oauth's Google profile also
// starts from.
//
// The identity is the token's "sub" (stable for the person's account at
// the provider, unlike an email address), with its email, email_verified
// and name for the person to see. Only the ID token is kept from the
// exchange, and only long enough to read it: the lane asks for no API
// access and keeps no tokens.
package oidc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"golang.org/x/oauth2"

	"github.com/bsamanvith/agentkit/pkg/auth/accounts"
	"github.com/bsamanvith/agentkit/pkg/auth/jwt"
	"github.com/bsamanvith/agentkit/pkg/clock"
	"github.com/bsamanvith/agentkit/pkg/credentials/providers"
)

// Config is one provider's lane.
type Config struct {
	// Name is the lane's name ("google").
	Name string
	// Issuer is the provider's issuer, exactly as its ID tokens say it.
	Issuer string
	// ClientID and ClientSecret are the client the operator registered
	// with the provider, as a web application.
	ClientID, ClientSecret string
	// AuthURL, TokenURL and JWKSURL are the provider's endpoints.
	AuthURL, TokenURL, JWKSURL string
	// RedirectURL is the lane's callback: the public URL of the app, the
	// accounts handler's prefix, then /{name}/callback. It is registered
	// with the provider.
	RedirectURL string
	// Scopes asked for; "openid" is always added. Default: email, profile.
	Scopes []string
	// AuthParams are added to the authorization URL, such as Google's
	// prompt=select_account.
	AuthParams map[string]string
}

// GoogleIssuer and GoogleJWKSURL are Google's.
const (
	GoogleIssuer  = "https://accounts.google.com"
	GoogleJWKSURL = "https://www.googleapis.com/oauth2/v3/certs"
)

// Google returns the "google" lane's config for a client registered in the
// Google Cloud console (an OAuth client of type "Web application", with
// redirectURL as an authorized redirect URI). Google always shows the
// account chooser, so a shared phone does not sign in as whoever used it
// last.
func Google(clientID, clientSecret, redirectURL string) Config {
	p := providers.Presets()["google"]
	return Config{
		Name: "google", Issuer: GoogleIssuer, ClientID: clientID, ClientSecret: clientSecret,
		AuthURL: p.AuthURL, TokenURL: p.TokenURL, JWKSURL: GoogleJWKSURL, RedirectURL: redirectURL,
		Scopes: []string{"email", "profile"}, AuthParams: map[string]string{"prompt": "select_account"},
	}
}

// Option configures [New].
type Option func(*options)

type options struct {
	client *http.Client
	clock  clock.Clock
}

// WithHTTPClient sets the client for the token endpoint and the keys
// (default: one with a 15 s timeout).
func WithHTTPClient(c *http.Client) Option { return func(o *options) { o.client = c } }

// WithClock sets the clock of the ID token's times.
func WithClock(c clock.Clock) Option { return func(o *options) { o.clock = c } }

// Lane is an OpenID Connect provider's sign-in lane: an
// accounts.RedirectLane.
type Lane struct {
	cfg    Config
	oauth  *oauth2.Config
	verify *jwt.Authenticator
	client *http.Client
}

var _ accounts.RedirectLane = (*Lane)(nil)

// New returns the lane of cfg. It fetches nothing until the first sign-in.
func New(cfg Config, opts ...Option) (*Lane, error) {
	o := options{client: &http.Client{Timeout: 15 * time.Second}, clock: clock.Real()}
	for _, opt := range opts {
		opt(&o)
	}
	switch {
	case cfg.Name == "":
		return nil, errors.New("oidc: no lane name")
	case cfg.ClientID == "" || cfg.ClientSecret == "":
		return nil, fmt.Errorf("oidc %s: a client id and secret are needed (register a web client with the provider)", cfg.Name)
	case cfg.AuthURL == "" || cfg.TokenURL == "" || cfg.JWKSURL == "":
		return nil, fmt.Errorf("oidc %s: the auth, token and keys URLs are needed", cfg.Name)
	case !strings.HasPrefix(cfg.RedirectURL, "https://") && !strings.HasPrefix(cfg.RedirectURL, "http://127.0.0.1") && !strings.HasPrefix(cfg.RedirectURL, "http://localhost"):
		return nil, fmt.Errorf("oidc %s: the redirect URL must be https (or http on localhost)", cfg.Name)
	}
	v, err := jwt.New(jwt.Config{Issuer: cfg.Issuer, Audience: cfg.ClientID, JWKSURL: cfg.JWKSURL},
		jwt.WithHTTPClient(o.client), jwt.WithClock(o.clock))
	if err != nil {
		return nil, fmt.Errorf("oidc %s: %w", cfg.Name, err)
	}
	scopes := []string{"openid"}
	for _, s := range cfg.Scopes {
		if s != "openid" {
			scopes = append(scopes, s)
		}
	}
	if len(cfg.Scopes) == 0 {
		scopes = append(scopes, "email", "profile")
	}
	return &Lane{
		cfg: cfg, verify: v, client: o.client,
		oauth: &oauth2.Config{
			ClientID: cfg.ClientID, ClientSecret: cfg.ClientSecret, RedirectURL: cfg.RedirectURL, Scopes: scopes,
			Endpoint: oauth2.Endpoint{AuthURL: cfg.AuthURL, TokenURL: cfg.TokenURL, AuthStyle: oauth2.AuthStyleInParams},
		},
	}, nil
}

// Name implements accounts.Lane.
func (l *Lane) Name() string { return l.cfg.Name }

// Kind implements accounts.Lane.
func (*Lane) Kind() accounts.Kind { return accounts.KindRedirect }

// AuthCodeURL implements accounts.RedirectLane.
func (l *Lane) AuthCodeURL(f accounts.Flow) string {
	opts := []oauth2.AuthCodeOption{oauth2.S256ChallengeOption(f.Verifier), oauth2.SetAuthURLParam("nonce", f.Nonce)}
	for k, v := range l.cfg.AuthParams {
		opts = append(opts, oauth2.SetAuthURLParam(k, v))
	}
	return l.oauth.AuthCodeURL(f.State, opts...)
}

// Finish implements accounts.RedirectLane. Its errors never hold a token
// or a code.
func (l *Lane) Finish(ctx context.Context, f accounts.Flow, code string) (accounts.Identity, error) {
	if code == "" {
		return accounts.Identity{}, errors.New("oidc: the provider sent no code")
	}
	ctx = context.WithValue(ctx, oauth2.HTTPClient, l.client)
	tok, err := l.oauth.Exchange(ctx, code, oauth2.VerifierOption(f.Verifier))
	if err != nil {
		var re *oauth2.RetrieveError
		if errors.As(err, &re) {
			return accounts.Identity{}, fmt.Errorf("oidc %s: the token endpoint refused the code: %s", l.cfg.Name, re.ErrorCode)
		}
		return accounts.Identity{}, fmt.Errorf("oidc %s: exchanging the code failed", l.cfg.Name)
	}
	raw, _ := tok.Extra("id_token").(string)
	if raw == "" {
		return accounts.Identity{}, fmt.Errorf("oidc %s: no ID token in the token response", l.cfg.Name)
	}
	c, err := l.verify.Verify(ctx, raw)
	if err != nil {
		return accounts.Identity{}, fmt.Errorf("oidc %s: the ID token: %w", l.cfg.Name, err)
	}
	var claims struct {
		Sub           string          `json:"sub"`
		Nonce         string          `json:"nonce"`
		Azp           string          `json:"azp"`
		Aud           json.RawMessage `json:"aud"`
		Email         string          `json:"email"`
		EmailVerified json.RawMessage `json:"email_verified"`
		Name          string          `json:"name"`
	}
	b, _ := json.Marshal(c)
	if err := json.Unmarshal(b, &claims); err != nil {
		return accounts.Identity{}, fmt.Errorf("oidc %s: the ID token's claims are malformed", l.cfg.Name)
	}
	switch {
	case claims.Nonce == "" || claims.Nonce != f.Nonce:
		return accounts.Identity{}, fmt.Errorf("oidc %s: the ID token's nonce is not this sign-in's", l.cfg.Name)
	case claims.Sub == "" || len(claims.Sub) > 255:
		return accounts.Identity{}, fmt.Errorf("oidc %s: the ID token has no usable subject", l.cfg.Name)
	case claims.Azp != "" && claims.Azp != l.cfg.ClientID:
		return accounts.Identity{}, fmt.Errorf("oidc %s: the ID token was issued to another client (azp)", l.cfg.Name)
	case strings.HasPrefix(strings.TrimSpace(string(claims.Aud)), "[") && claims.Azp == "":
		// Several audiences: OpenID Connect Core 3.1.3.7 wants azp.
		var auds []string
		if json.Unmarshal(claims.Aud, &auds) == nil && len(auds) > 1 {
			return accounts.Identity{}, fmt.Errorf("oidc %s: an ID token for several audiences without azp", l.cfg.Name)
		}
	}
	// Google sends email_verified as a boolean; some providers as a string.
	verified := string(claims.EmailVerified) == "true" || string(claims.EmailVerified) == `"true"`
	id := accounts.Identity{Lane: l.cfg.Name, Subject: claims.Sub, Name: strings.TrimSpace(claims.Name)}
	if e, err := accounts.NormalizeEmail(claims.Email); err == nil {
		id.Email, id.EmailVerified = e, verified
	}
	return id, nil
}
