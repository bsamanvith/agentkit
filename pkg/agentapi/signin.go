package agentapi

import (
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
	"github.com/bsamanvith/agentkit/pkg/auth"
	"github.com/bsamanvith/agentkit/pkg/auth/login"
)

// Sign-in error codes: [wire.CodeCSRF] and [wire.CodeTooManyAttempts].
const (
	CodeCSRF            = wire.CodeCSRF
	CodeTooManyAttempts = wire.CodeTooManyAttempts
)

// ScopeAdminAgents is the scope that reads agents' system prompts, in GET
// /v1/agents/{name}. Admins have it ("admin:*"), as does "*".
const ScopeAdminAgents = "admin:agents"

// maxLoginBody bounds a sign-in's body, in bytes.
const maxLoginBody = 4 << 10

// WithLogin adds password sign-in for the console (package auth/login):
// POST /v1/auth/login and /v1/auth/logout, and the sessions' cookies as a
// credential after those of [WithAuthenticator] or [WithToken] (a request
// with a bearer token is judged by it, whatever its cookies). A request a
// session authenticates with a method other than GET, HEAD or OPTIONS must
// also pass [login.Login.CheckCSRF], else 403 [CodeCSRF].
func WithLogin(l *login.Login) Option { return func(c *config) { c.login = l } }

// isAuthRoute reports whether pattern is one of the /v1/auth/ endpoints,
// which any principal may call.
func isAuthRoute(pattern string) bool {
	switch pattern {
	case "GET /v1/auth/config", "POST /v1/auth/login", "POST /v1/auth/logout", "GET /v1/auth/me":
		return true
	}
	return false
}

// public reports whether a request needs no credentials: how to sign in, and
// signing in.
func public(r *http.Request) bool {
	return r.Method == http.MethodGet && r.URL.Path == "/v1/auth/config" ||
		r.Method == http.MethodPost && r.URL.Path == "/v1/auth/login"
}

// authConfig answers GET /v1/auth/config: the sign-in methods.
func (s *Server) authConfig(w http.ResponseWriter, _ *http.Request) {
	m := []string{}
	if s.cfg.login != nil {
		m = append(m, wire.AuthPassword)
	}
	if s.bearer != nil {
		m = append(m, wire.AuthToken)
	}
	writeJSON(w, http.StatusOK, wire.AuthConfig{V: WireVersion, Methods: m})
}

// signIn answers POST /v1/auth/login: 204 with the session cookie.
func (s *Server) signIn(w http.ResponseWriter, r *http.Request) {
	if s.cfg.login == nil {
		writeError(w, http.StatusNotFound, &Error{Code: CodeNotFound, Message: "this server has no password sign-in"})
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxLoginBody))
	var req wire.LoginRequest
	if err == nil {
		err = decodeStrict(body, &req)
	}
	// The decoder's errors are not shown: they could quote the password.
	if err != nil {
		writeError(w, http.StatusBadRequest, &Error{Code: CodeInvalidRequest, Message: `the body must be {"username": "...", "password": "..."}, at most 4 KiB`})
		return
	}
	if req.Username == "" || req.Password == "" {
		writeError(w, http.StatusBadRequest, &Error{Code: CodeInvalidRequest, Message: "a username and a password are required"})
		return
	}
	c, err := s.cfg.login.SignIn(r, req.Username, req.Password)
	var throttled *login.ThrottledError
	switch {
	case err == nil:
		http.SetCookie(w, c)
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, login.ErrBadLogin):
		writeError(w, http.StatusUnauthorized, &Error{Code: CodeUnauthorized, Message: "wrong username or password"})
	case errors.As(err, &throttled):
		w.Header().Set("Retry-After", strconv.Itoa(throttled.RetryAfterSeconds()))
		writeError(w, http.StatusTooManyRequests, &Error{Code: CodeTooManyAttempts, Message: "too many failed sign-ins; try again later", Retryable: true})
	case errors.Is(err, login.ErrCSRF):
		writeError(w, http.StatusForbidden, &Error{Code: CodeCSRF, Message: "a sign-in must come from this server's own pages"})
	default:
		s.internal(w, "signing in", err)
	}
}

// signOut answers POST /v1/auth/logout: 204, the session ended and its
// cookie cleared.
func (s *Server) signOut(w http.ResponseWriter, r *http.Request) {
	if s.cfg.login == nil {
		writeError(w, http.StatusNotFound, &Error{Code: CodeNotFound, Message: "this server has no password sign-in"})
		return
	}
	c, err := s.cfg.login.SignOut(r)
	if err != nil {
		s.internal(w, "signing out", err)
		return
	}
	http.SetCookie(w, c)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

// me answers GET /v1/auth/me.
func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	p, ok := auth.From(r.Context())
	if !ok { // an open server: everything, as the default tenant
		writeJSON(w, http.StatusOK, wire.Me{V: WireVersion, Tenant: DefaultTenant, Roles: []string{}, Scopes: []string{"*"}})
		return
	}
	m := wire.Me{V: WireVersion, Username: p.User, Tenant: p.Tenant.ID(), Roles: []string{}, Scopes: p.Scopes, Credential: p.Via.Kind}
	if m.Scopes == nil {
		m.Scopes = []string{}
	}
	if p.Via.Kind == login.KindSession && s.cfg.login != nil {
		info, err := s.cfg.login.Session(r)
		if err != nil {
			s.authFailed(w, r, err)
			return
		}
		m.Roles, m.CSRF = info.Roles, info.CSRF
	}
	writeJSON(w, http.StatusOK, m)
}

// csrf answers 403 [CodeCSRF] to an unsafe request a session authenticated
// that fails [login.Login.CheckCSRF]. It runs after authentication.
func (s *Server) csrf(next http.Handler) http.Handler {
	if s.cfg.login == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p, ok := auth.From(r.Context()); ok && p.Via.Kind == login.KindSession {
			if err := s.cfg.login.CheckCSRF(r); err != nil {
				s.authFailed(w, r, err)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// authFailed answers a session check that failed after the request was
// authenticated: a CSRF failure, or a session that ended in between.
func (s *Server) authFailed(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, login.ErrCSRF):
		s.log.Info("auth: cross-site request refused", "method", r.Method, "path", r.URL.Path, "reason", err.Error())
		writeError(w, http.StatusForbidden, &Error{Code: CodeCSRF, Message: "a request with a session cookie needs this server's Origin and the CSRF token of /v1/auth/me in " + login.CSRFHeader})
	case errors.Is(err, auth.ErrInvalidCredentials), errors.Is(err, auth.ErrNoCredentials):
		writeAuthError(w, r, http.StatusUnauthorized)
	default:
		s.internal(w, "reading the session", err)
	}
}
