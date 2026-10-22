package agentapi

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/bsamanvith/agentkit/pkg/auth"
)

// The scopes a principal (package auth) needs: [ScopeRead] for the API's GET
// endpoints, [ScopeRun] for the others, but [ScopeAdmin] for those that
// manage toolsets. "*" grants all three.
const (
	ScopeRead = "agent:read"
	ScopeRun  = "agent:run"
)

// tokenKeyID names the key [WithToken] makes, in logs.
const tokenKeyID = "api-token"

// authenticator is the server's: the one given, or the token as one key of
// the default tenant with every scope and toolset; nil for an open server.
func authenticator(cfg config) (auth.Authenticator, error) {
	switch {
	case cfg.authn != nil && cfg.token != "":
		return nil, fmt.Errorf("%w: give WithToken or WithAuthenticator, not both", ErrInvalidConfig)
	case cfg.authn != nil:
		return cfg.authn, nil
	case cfg.token == "":
		return nil, nil
	case len(cfg.token) < MinTokenLength:
		return nil, fmt.Errorf("%w: the token must be at least %d bytes", ErrInvalidConfig, MinTokenLength)
	}
	e, err := auth.KeyEntry(auth.APIKey{ID: tokenKeyID, Tenant: DefaultTenant, Scopes: []string{"*"}, Toolsets: []string{"*"}}, cfg.token)
	if err != nil {
		return nil, fmt.Errorf("%w: the token: %w", ErrInvalidConfig, errors.Unwrap(err))
	}
	return auth.NewAPIKeys([]auth.APIKey{e})
}

// writeAuthError writes the middleware's failures in the API's error format.
func writeAuthError(w http.ResponseWriter, _ *http.Request, status int) {
	if status == http.StatusUnauthorized {
		writeError(w, status, &Error{Code: CodeUnauthorized, Message: "missing or wrong bearer token"})
		return
	}
	writeError(w, status, &Error{Code: CodeInternal, Message: "authenticating the request failed; see the server log"})
}

// scopeOf is the scope an endpoint needs: none for signing in and out and
// saying who the caller is, [ScopeAdmin] to add, review, change or remove
// toolsets and for the agent registry's endpoints, else reading for GET and
// running for the rest.
func scopeOf(pattern string) string {
	switch {
	case isAuthRoute(pattern), isHookRoute(pattern):
		return ""
	case pattern == "POST /v1/toolsets" || strings.HasPrefix(pattern, "PATCH /v1/toolsets/") ||
		strings.HasPrefix(pattern, "DELETE /v1/toolsets/") || strings.HasSuffix(pattern, "/v1/toolsets/{name}/review"),
		pattern == "GET /v1/webhooks/secret", strings.Contains(pattern, " /v1/triggers"), pattern == "GET /v1/mcp-registry/search":
		return ScopeAdmin
	case strings.Contains(pattern, " /v1/agents/{name}/versions") || strings.HasSuffix(pattern, " /v1/agents/{name}/publish"):
		// The agent registry: its versions hold whole definitions.
		return ScopeAdmin
	case strings.HasPrefix(pattern, http.MethodGet+" "):
		return ScopeRead
	}
	return ScopeRun
}

// requireScope answers 403 forbidden to a request whose principal does not
// hold scope. A request with no principal (an open server) passes, as does
// every request when scope is "".
func requireScope(scope string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if p, ok := auth.From(r.Context()); ok && scope != "" && !p.HasScope(scope) {
			writeError(w, http.StatusForbidden, &Error{Code: CodeForbidden, Message: "this credential does not have the scope " + quote(scope)})
			return
		}
		next(w, r)
	}
}

// access is what a request's principal may use. With no principal (an open
// server, or one with a token) it may use everything.
type access struct {
	p  auth.Principal
	ok bool
}

func accessOf(r *http.Request) access {
	p, ok := auth.From(r.Context())
	return access{p, ok}
}

// toolset reports whether the principal may use the toolset name.
func (a access) toolset(name string) bool { return !a.ok || a.p.AllowsToolset(name) }

// agent reports whether the principal may use ag: whether it may use every
// toolset of the agent, those added at run time too, whose runs offer the
// model all of their tools.
func (a access) agent(ag *agent) bool {
	for _, name := range slices.Concat(ag.Toolsets, ag.RuntimeToolsets) {
		if !a.toolset(name) {
			return false
		}
	}
	return true
}
