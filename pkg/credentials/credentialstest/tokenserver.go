package credentialstest

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
)

// TokenServer is a fake OAuth2 authorization server (RFC 6749) on an
// httptest server, for tests of connecting accounts, refreshing and client
// credentials. Its token endpoint (/token) answers the authorization_code
// grant (with PKCE, RFC 7636: S256 only, the verifier checked against the
// challenge), the refresh_token grant and the client_credentials grant,
// authenticates the client (basic auth or form), rotates refresh tokens and
// counts requests. Its authorization endpoint (/authorize) consents at once:
// it redirects to the redirect_uri with a code (single use, bound to the
// client, the redirect_uri and the challenge) and the state. Its zero options
// issue tokens that last an hour and keep a used refresh token valid.
type TokenServer struct {
	// URL is the token endpoint: the server's URL and /token.
	URL string
	// AuthURL is the authorization endpoint: the server's URL and
	// /authorize.
	AuthURL string
	// ClientID and ClientSecret are the client it accepts.
	ClientID, ClientSecret string

	srv *httptest.Server

	mu       sync.Mutex
	opts     TokenServerOptions
	refresh  map[string]bool // refresh tokens: valid or used up
	codes    map[string]authCode
	clients  map[string]string
	n        int
	requests int
	grants   map[string]int
}

// TokenServerOptions change what a [TokenServer] answers.
type TokenServerOptions struct {
	// Prefix starts every token issued (a test's canary, say).
	Prefix string
	// ExpiresIn is the expires_in of each token issued (default 3600).
	ExpiresIn int
	// RevokeUsed makes a refresh token single use: a second refresh with
	// it is invalid_grant, as a server with refresh token rotation and
	// reuse detection answers.
	RevokeUsed bool
	// KeepRefreshToken answers a refresh without a new refresh token.
	KeepRefreshToken bool
	// Hook, if set, runs first on each request to /token, under no lock:
	// it may block, write to a vault, change the form, or answer itself
	// (returning true).
	Hook func(w http.ResponseWriter, r *http.Request, form url.Values) bool
	// Deny makes /authorize answer the redirect_uri with
	// error=access_denied, as when the user declines.
	Deny bool
}

// authCode is an authorization code /authorize issued.
type authCode struct {
	clientID, redirectURI, challenge, scope string
}

// NewTokenServer starts a token server, closed when the test ends.
func NewTokenServer(t testing.TB, opts TokenServerOptions) *TokenServer {
	t.Helper()
	s := &TokenServer{
		ClientID: "test-client", ClientSecret: "test-client-secret",
		opts: opts, refresh: map[string]bool{}, clients: map[string]string{}, grants: map[string]int{},
		codes: map[string]authCode{},
	}
	if s.opts.ExpiresIn == 0 {
		s.opts.ExpiresIn = 3600
	}
	s.clients[s.ClientID] = s.ClientSecret
	s.srv = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.srv.Close)
	s.URL = s.srv.URL + "/token"
	s.AuthURL = s.srv.URL + "/authorize"
	return s
}

// SetOptions replaces the options.
func (s *TokenServer) SetOptions(o TokenServerOptions) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if o.ExpiresIn == 0 {
		o.ExpiresIn = 3600
	}
	s.opts = o
}

// AddRefreshToken makes rt a valid refresh token.
func (s *TokenServer) AddRefreshToken(rt string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refresh[rt] = true
}

// AddClient accepts another client.
func (s *TokenServer) AddClient(id, secret string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clients[id] = secret
}

// Valid reports whether rt is a refresh token the server would accept.
func (s *TokenServer) Valid(rt string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.refresh[rt]
}

// Requests is how many requests reached /token.
func (s *TokenServer) Requests() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests
}

// Grants is how many requests of grant type grant reached /token.
func (s *TokenServer) Grants(grant string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.grants[grant]
}

// TokenError answers an RFC 6749 §5.2 error.
func TokenError(w http.ResponseWriter, status int, code, description string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code, "error_description": description})
}

// Challenge is the PKCE S256 challenge of verifier (RFC 7636 §4.2).
func Challenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// authorize issues a code and sends the browser back to the client, or
// answers 400 to a request it cannot send back (an unknown client, no
// redirect_uri).
func (s *TokenServer) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	back, err := url.Parse(q.Get("redirect_uri"))
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, known := s.clients[q.Get("client_id")]; !known || err != nil || !back.IsAbs() {
		http.Error(w, "unknown client or no redirect_uri", http.StatusBadRequest)
		return
	}
	answer := back.Query()
	answer.Set("state", q.Get("state"))
	switch {
	case s.opts.Deny:
		answer.Set("error", "access_denied")
	case q.Get("response_type") != "code":
		answer.Set("error", "unsupported_response_type")
	case q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "":
		answer.Set("error", "invalid_request") // PKCE S256 is required
	default:
		s.n++
		code := fmt.Sprintf("code-%d", s.n)
		s.codes[code] = authCode{clientID: q.Get("client_id"), redirectURI: q.Get("redirect_uri"), challenge: q.Get("code_challenge"), scope: q.Get("scope")}
		answer.Set("code", code)
	}
	back.RawQuery = answer.Encode()
	http.Redirect(w, r, back.String(), http.StatusFound)
}

func (s *TokenServer) serve(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/authorize" && r.Method == http.MethodGet {
		s.authorize(w, r)
		return
	}
	if r.URL.Path != "/token" || r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		TokenError(w, http.StatusBadRequest, "invalid_request", "bad form")
		return
	}
	form := r.PostForm
	s.mu.Lock()
	s.requests++
	s.grants[form.Get("grant_type")]++
	hook := s.opts.Hook
	s.mu.Unlock()
	if hook != nil && hook(w, r, form) {
		return
	}
	id, secret, ok := r.BasicAuth()
	if !ok {
		id, secret = form.Get("client_id"), form.Get("client_secret")
	} else {
		id, _ = url.QueryUnescape(id)
		secret, _ = url.QueryUnescape(secret)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if want, known := s.clients[id]; !known || want != secret || secret == "" {
		TokenError(w, http.StatusUnauthorized, "invalid_client", "unknown client")
		return
	}
	s.n++
	resp := map[string]any{
		"access_token": fmt.Sprintf("%sat-%d", s.opts.Prefix, s.n),
		"token_type":   "Bearer",
		"expires_in":   s.opts.ExpiresIn,
	}
	switch form.Get("grant_type") {
	case "refresh_token":
		rt := form.Get("refresh_token")
		if !s.refresh[rt] {
			TokenError(w, http.StatusBadRequest, "invalid_grant", "refresh token "+rt+" is not valid")
			return
		}
		if s.opts.RevokeUsed {
			s.refresh[rt] = false
		}
		if !s.opts.KeepRefreshToken {
			next := fmt.Sprintf("%srt-%d", s.opts.Prefix, s.n)
			s.refresh[next] = true
			resp["refresh_token"] = next
		}
	case "authorization_code":
		c, ok := s.codes[form.Get("code")]
		delete(s.codes, form.Get("code")) // single use, even when refused
		switch {
		case !ok || c.clientID != id || c.redirectURI != form.Get("redirect_uri"):
			TokenError(w, http.StatusBadRequest, "invalid_grant", "unknown code, or another client or redirect_uri")
			return
		case form.Get("code_verifier") == "" || Challenge(form.Get("code_verifier")) != c.challenge:
			TokenError(w, http.StatusBadRequest, "invalid_grant", "the code_verifier does not match the code_challenge")
			return
		}
		next := fmt.Sprintf("%srt-%d", s.opts.Prefix, s.n)
		s.refresh[next] = true
		resp["refresh_token"] = next
		if c.scope != "" {
			resp["scope"] = c.scope
		}
	case "client_credentials":
	default:
		TokenError(w, http.StatusBadRequest, "unsupported_grant_type", form.Get("grant_type"))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}
