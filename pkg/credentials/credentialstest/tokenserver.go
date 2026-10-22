package credentialstest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
)

// TokenServer is a fake OAuth2 token endpoint (RFC 6749 §3.2) on an
// httptest server, for tests of refreshing and client credentials: it
// answers the refresh_token and client_credentials grants at /token,
// authenticates the client (basic auth or form), rotates refresh tokens and
// counts requests. Its zero options issue tokens that last an hour and keep
// a used refresh token valid.
type TokenServer struct {
	// URL is the token endpoint: the server's URL and /token.
	URL string
	// ClientID and ClientSecret are the client it accepts.
	ClientID, ClientSecret string

	srv *httptest.Server

	mu       sync.Mutex
	opts     TokenServerOptions
	refresh  map[string]bool // refresh tokens: valid or used up
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
	// Hook, if set, runs first on each request, under no lock: it may
	// block, write to a vault, or answer itself (returning true).
	Hook func(w http.ResponseWriter, r *http.Request, form url.Values) bool
}

// NewTokenServer starts a token server, closed when the test ends.
func NewTokenServer(t testing.TB, opts TokenServerOptions) *TokenServer {
	t.Helper()
	s := &TokenServer{
		ClientID: "test-client", ClientSecret: "test-client-secret",
		opts: opts, refresh: map[string]bool{}, clients: map[string]string{}, grants: map[string]int{},
	}
	if s.opts.ExpiresIn == 0 {
		s.opts.ExpiresIn = 3600
	}
	s.clients[s.ClientID] = s.ClientSecret
	s.srv = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.srv.Close)
	s.URL = s.srv.URL + "/token"
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

func (s *TokenServer) serve(w http.ResponseWriter, r *http.Request) {
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
	case "client_credentials":
	default:
		TokenError(w, http.StatusBadRequest, "unsupported_grant_type", form.Get("grant_type"))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}
