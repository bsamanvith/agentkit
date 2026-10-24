// Package oauthtest has a fake OAuth 2.1 authorization server and a fake
// protected MCP resource, strict about what the MCP authorization spec
// requires: PKCE S256, the resource parameter, single-use codes, registered
// redirect URIs, and client ID metadata documents fetched and checked.
package oauthtest

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// AuthServerOptions configures a [AuthServer].
type AuthServerOptions struct {
	// CIMD advertises client_id_metadata_document_supported.
	CIMD bool
	// DCR advertises a registration endpoint.
	DCR bool
	// Resource is the only resource the server issues tokens for.
	Resource string
	// Scopes supported; offline_access makes it issue refresh tokens.
	Scopes []string
	// TTL is expires_in for access tokens; zero leaves expires_in out.
	TTL time.Duration
	// IssParam advertises and sends iss on the redirect (RFC 9207).
	IssParam bool
	// WrongIssuer serves metadata naming another issuer.
	WrongIssuer bool
	// NoPKCE leaves code_challenge_methods_supported out.
	NoPKCE bool
	// Clients are pre-registered: id to secret.
	Clients map[string]string
	// Extra is added to every token response.
	Extra map[string]any
	// HTTPClient fetches client metadata documents.
	HTTPClient *http.Client
	// OIDC serves the metadata at OpenID Connect's well-known location
	// only (/.well-known/openid-configuration), not RFC 8414's.
	OIDC bool
	// OnRegister, if set, is called in each dynamic registration request
	// before it is answered; a test can hold the request there.
	OnRegister func()
}

// AuthServer is a fake authorization server.
type AuthServer struct {
	*httptest.Server
	opts AuthServerOptions

	mu            sync.Mutex
	formAuth      bool // the latest token request sent its client in the form
	clients       map[string]client
	codes         map[string]grant
	access        map[string]grant
	refresh       map[string]grant
	Registrations int
	Refreshes     int
	CIMDFetches   int
	// Seen is the query of every authorization request.
	Seen []url.Values
}

type client struct {
	secret   string
	redirect []string
}

type grant struct {
	clientID, redirect, challenge, resource, scope string
	expires                                        time.Time
}

// NewAuthServer starts one; it is closed when the test ends.
func NewAuthServer(t testing.TB, opts AuthServerOptions) *AuthServer {
	a := &AuthServer{
		opts:    opts,
		clients: map[string]client{},
		codes:   map[string]grant{},
		access:  map[string]grant{},
		refresh: map[string]grant{},
	}
	for id, sec := range opts.Clients {
		a.clients[id] = client{secret: sec}
	}
	mux := http.NewServeMux()
	if opts.OIDC {
		mux.HandleFunc("GET /.well-known/openid-configuration", a.metadata)
	} else {
		mux.HandleFunc("GET /.well-known/oauth-authorization-server", a.metadata)
	}
	mux.HandleFunc("GET /authorize", a.authorize)
	mux.HandleFunc("POST /token", a.token)
	mux.HandleFunc("POST /register", a.register)
	a.Server = httptest.NewServer(mux)
	t.Cleanup(a.Close)
	return a
}

func (a *AuthServer) metadata(w http.ResponseWriter, _ *http.Request) {
	iss := a.URL
	if a.opts.WrongIssuer {
		iss = "http://127.0.0.1:1/other"
	}
	m := map[string]any{
		"issuer":                                         iss,
		"authorization_endpoint":                         a.URL + "/authorize",
		"token_endpoint":                                 a.URL + "/token",
		"response_types_supported":                       []string{"code"},
		"scopes_supported":                               a.opts.Scopes,
		"token_endpoint_auth_methods_supported":          []string{"client_secret_basic", "none"},
		"client_id_metadata_document_supported":          a.opts.CIMD,
		"authorization_response_iss_parameter_supported": a.opts.IssParam,
	}
	if !a.opts.NoPKCE {
		m["code_challenge_methods_supported"] = []string{"S256"}
	}
	if a.opts.DCR {
		m["registration_endpoint"] = a.URL + "/register"
	}
	writeJSON(w, http.StatusOK, m)
}

func (a *AuthServer) register(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RedirectURIs []string `json:"redirect_uris"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.RedirectURIs) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_client_metadata"})
		return
	}
	if a.opts.OnRegister != nil {
		a.opts.OnRegister()
	}
	a.mu.Lock()
	a.Registrations++
	id, sec := "dcr-"+rand.Text(), rand.Text()
	a.clients[id] = client{secret: sec, redirect: req.RedirectURIs}
	a.mu.Unlock()
	writeJSON(w, http.StatusCreated, map[string]any{
		"client_id": id, "client_secret": sec, "redirect_uris": req.RedirectURIs,
		"token_endpoint_auth_method": "client_secret_basic",
	})
}

// clientFor resolves a client_id: a registered one, or a CIMD URL fetched now.
func (a *AuthServer) clientFor(id string) (client, bool) {
	a.mu.Lock()
	c, ok := a.clients[id]
	a.mu.Unlock()
	if ok {
		return c, true
	}
	if !a.opts.CIMD || !strings.HasPrefix(id, "https://") || a.opts.HTTPClient == nil {
		return client{}, false
	}
	resp, err := a.opts.HTTPClient.Get(id)
	if err != nil {
		return client{}, false
	}
	defer resp.Body.Close()
	var doc struct {
		ClientID     string   `json:"client_id"`
		RedirectURIs []string `json:"redirect_uris"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&doc) != nil || doc.ClientID != id {
		return client{}, false
	}
	a.mu.Lock()
	a.CIMDFetches++
	a.mu.Unlock()
	return client{redirect: doc.RedirectURIs}, true
}

// authorize grants at once, as if the user signed in and consented, and
// redirects with the code; or answers 400 if the request is not one the spec
// allows.
func (a *AuthServer) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	a.mu.Lock()
	a.Seen = append(a.Seen, q)
	a.mu.Unlock()
	c, ok := a.clientFor(q.Get("client_id"))
	redirect := q.Get("redirect_uri")
	switch {
	case !ok:
		http.Error(w, "unknown client", http.StatusBadRequest)
		return
	case len(c.redirect) > 0 && !slices.Contains(c.redirect, redirect):
		http.Error(w, "redirect_uri not registered", http.StatusBadRequest)
		return
	case q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "":
		http.Error(w, "PKCE S256 required", http.StatusBadRequest)
		return
	case a.resource() != "" && q.Get("resource") != a.resource():
		http.Error(w, "wrong resource", http.StatusBadRequest)
		return
	}
	code := rand.Text()
	a.mu.Lock()
	a.codes[code] = grant{clientID: q.Get("client_id"), redirect: redirect, challenge: q.Get("code_challenge"), resource: q.Get("resource"), scope: q.Get("scope")}
	a.mu.Unlock()
	back := url.Values{"code": {code}, "state": {q.Get("state")}}
	if a.opts.IssParam {
		back.Set("iss", a.URL)
	}
	http.Redirect(w, r, redirect+"?"+back.Encode(), http.StatusFound)
}

func (a *AuthServer) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		tokenErr(w, "invalid_request")
		return
	}
	id, sec, basic := r.BasicAuth()
	a.mu.Lock()
	a.formAuth = !basic
	a.mu.Unlock()
	if !basic {
		id, sec = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
	}
	c, ok := a.clientFor(id)
	if !ok || c.secret != sec {
		tokenErr(w, "invalid_client")
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	var g grant
	switch r.PostForm.Get("grant_type") {
	case "authorization_code":
		g, ok = a.codes[r.PostForm.Get("code")]
		delete(a.codes, r.PostForm.Get("code")) // single use
		sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
		if !ok || g.clientID != id || g.redirect != r.PostForm.Get("redirect_uri") ||
			base64.RawURLEncoding.EncodeToString(sum[:]) != g.challenge ||
			r.PostForm.Get("resource") != g.resource {
			tokenErr(w, "invalid_grant")
			return
		}
	case "refresh_token":
		g, ok = a.refresh[r.PostForm.Get("refresh_token")]
		if !ok || g.clientID != id {
			tokenErr(w, "invalid_grant")
			return
		}
		a.Refreshes++
	default:
		tokenErr(w, "unsupported_grant_type")
		return
	}
	at := "at-" + rand.Text()
	if a.opts.TTL > 0 {
		g.expires = time.Now().Add(a.opts.TTL)
	}
	a.access[at] = g
	resp := map[string]any{"access_token": at, "token_type": "Bearer", "scope": g.scope}
	if a.opts.TTL > 0 {
		resp["expires_in"] = int(a.opts.TTL.Seconds())
	}
	if slices.Contains(strings.Fields(g.scope), "offline_access") || r.PostForm.Get("grant_type") == "refresh_token" {
		rt := "rt-" + rand.Text()
		a.refresh[rt] = g
		resp["refresh_token"] = rt
	}
	for k, v := range a.opts.Extra {
		resp[k] = v
	}
	writeJSON(w, http.StatusOK, resp)
}

// SetResource sets the only resource the server issues tokens for, once the
// resource's URL is known.
func (a *AuthServer) SetResource(r string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.opts.Resource = r
}

// Valid reports whether tok is a live access token for resource.
func (a *AuthServer) Valid(tok, resource string) (scope string, ok bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	g, ok := a.access[tok]
	if !ok || (a.opts.Resource != "" && g.resource != a.opts.Resource) || resource == "" {
		return "", false
	}
	return g.scope, true
}

// RevokeAccess makes every access token invalid, as if they expired early.
func (a *AuthServer) RevokeAccess() {
	a.mu.Lock()
	defer a.mu.Unlock()
	clear(a.access)
}

// RevokeRefresh makes every refresh token invalid: refreshes get invalid_grant.
func (a *AuthServer) RevokeRefresh() {
	a.mu.Lock()
	defer a.mu.Unlock()
	clear(a.refresh)
}

// Counts returns Registrations, Refreshes and CIMDFetches.
func (a *AuthServer) Counts() (registrations, refreshes, cimdFetches int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.Registrations, a.Refreshes, a.CIMDFetches
}

// LastTokenAuthInParams reports whether the latest token request sent its
// client's id and secret in the form rather than in HTTP basic auth.
func (a *AuthServer) LastTokenAuthInParams() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.formAuth
}

// LastAuthorize returns the query of the latest authorization request.
func (a *AuthServer) LastAuthorize() url.Values {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.Seen) == 0 {
		return nil
	}
	return a.Seen[len(a.Seen)-1]
}

func (a *AuthServer) resource() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.opts.Resource
}

func tokenErr(w http.ResponseWriter, code string) {
	writeJSON(w, http.StatusBadRequest, map[string]string{"error": code})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// ResourceOptions configures [Protect].
type ResourceOptions struct {
	// NoHeader leaves resource_metadata out of the 401, so clients must find
	// the metadata at the well-known location.
	NoHeader bool
	// Scopes names scopes in the 401 challenge, and in the protected
	// resource metadata unless MetadataScopes is set.
	Scopes []string
	// MetadataScopes, if set, is the metadata's scopes_supported instead.
	MetadataScopes []string
	// RequireScope answers 403 insufficient_scope to a token without it.
	RequireScope string
}

// Protect serves next at path on a new server, behind bearer tokens from as,
// with its protected resource metadata at the well-known location. It
// returns the server; the resource URL is its URL + path.
func Protect(t testing.TB, as *AuthServer, path string, next http.Handler, opts ResourceOptions) *httptest.Server {
	var srv *httptest.Server
	mux := http.NewServeMux()
	metaScopes := opts.Scopes
	if opts.MetadataScopes != nil {
		metaScopes = opts.MetadataScopes
	}
	prm := func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"resource":              srv.URL + path,
			"authorization_servers": []string{as.URL},
			"scopes_supported":      metaScopes,
		})
	}
	mux.HandleFunc("GET /.well-known/oauth-protected-resource"+path, prm)
	mux.Handle(path, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		scope, valid := as.Valid(tok, srv.URL+path)
		if !ok || !valid {
			ch := `Bearer error="invalid_token"`
			if !opts.NoHeader {
				ch += fmt.Sprintf(`, resource_metadata="%s/.well-known/oauth-protected-resource%s"`, srv.URL, path)
			}
			if len(opts.Scopes) > 0 {
				ch += fmt.Sprintf(`, scope="%s"`, strings.Join(opts.Scopes, " "))
			}
			w.Header().Set("WWW-Authenticate", ch)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if opts.RequireScope != "" && !slices.Contains(strings.Fields(scope), opts.RequireScope) {
			w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer error="insufficient_scope", scope="%s"`, opts.RequireScope))
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	}))
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}
