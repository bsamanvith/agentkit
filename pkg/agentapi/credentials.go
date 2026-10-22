package agentapi

import (
	"context"
	"errors"
	"net/http"
	"slices"

	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
	"github.com/bsamanvith/agentkit/pkg/auth"
)

// ScopeCredentialsAdmin is the scope that sets, deletes and connects a
// tenant's shared credentials (scope "tenant" of a slot). A user sets and
// connects their own (scope "user") with [ScopeRun] alone. "*" grants it.
const ScopeCredentialsAdmin = "credentials:admin"

// Errors a [Credentials] returns, checked with [errors.Is]; the API answers
// each with its own status. A message wrapped around one is shown to the
// caller, so it must never hold a credential's value.
var (
	// ErrCredentialNotFound: no credential is stored there (404).
	ErrCredentialNotFound = errors.New("no such credential")
	// ErrCredentialInvalid: the request cannot be carried out as it is
	// (400 invalid_request): a missing field, an unknown kind, a refused
	// token URL or return URL, a slot without an OAuth2 app.
	ErrCredentialInvalid = errors.New("invalid credential request")
	// ErrCredentialForbidden: the request's tenant may not keep
	// credentials here at all (403 forbidden), such as the tenant reserved
	// for the connect flows' state.
	ErrCredentialForbidden = errors.New("credentials are not kept for this tenant")
)

// Credentials keeps the credentials of a tenant's slots, which toolsets use
// (package credentials/connect implements it over the vault). Every method
// acts in the tenant of ctx; user is "" for the tenant's shared credential
// (scope "tenant") and the user's id for their own (scope "user"). Who may
// do what is checked before: a method is only called for a slot and scope
// the server knows, by a caller allowed to.
type Credentials interface {
	// Scopes returns the scopes ("tenant", "user") the server's toolsets
	// use slot with; none for a slot the server does not know.
	Scopes(slot string) []string
	// List returns every slot and scope the server knows, as user sees
	// them: the tenant's shared credentials, and user's own ones (none
	// when user is ""). It never holds a value.
	List(ctx context.Context, user string) ([]wire.CredentialInfo, error)
	// Put stores a credential, replacing the one there.
	Put(ctx context.Context, user, slot string, req wire.PutCredentialRequest) (wire.CredentialInfo, error)
	// Delete removes the credential ([ErrCredentialNotFound] if none).
	Delete(ctx context.Context, user, slot string) error
	// Connect starts connecting an account to slot with OAuth2: it
	// returns the provider's page to send the browser to. The callback
	// stores the tokens for user in the tenant of ctx.
	Connect(ctx context.Context, user, slot string, req wire.ConnectRequest) (wire.ConnectResponse, error)
	// Status is the credential's status for GET /v1/toolsets: one of
	// wire.CredentialConnected, CredentialNotConnected and
	// CredentialNeedsReconnect.
	Status(ctx context.Context, user, slot string) (string, error)
}

// WithCredentials serves GET /v1/credentials, PUT and DELETE
// /v1/credentials/{slot} and POST /v1/credentials/{slot}/connect over c,
// and gives each toolset whose [Toolset.Credential] names a slot the
// caller's status in GET /v1/toolsets. Without it those endpoints answer
// 404.
func WithCredentials(c Credentials) Option { return func(cfg *config) { cfg.credentials = c } }

// userOf is the request's user: its principal's, "" for none.
func userOf(r *http.Request) string {
	if p, ok := auth.From(r.Context()); ok {
		return p.User
	}
	return ""
}

// credentialsOn answers 404 when the server keeps no credentials.
func (s *Server) credentialsOn(w http.ResponseWriter) bool {
	if s.cfg.credentials == nil {
		writeError(w, http.StatusNotFound, &Error{Code: CodeNotFound, Message: "this server keeps no credentials"})
		return false
	}
	return true
}

// credentialOwner resolves whose credential a request about slot is: ""
// for the tenant's (which needs [ScopeCredentialsAdmin]), the caller's user
// for their own. scope may be "" when the server uses slot with one scope.
// It writes the answer and returns false when the request may not go on.
func (s *Server) credentialOwner(w http.ResponseWriter, r *http.Request, slot, scope string) (string, bool) {
	scopes := s.cfg.credentials.Scopes(slot)
	switch {
	case len(scopes) == 0:
		writeError(w, http.StatusNotFound, &Error{Code: CodeNotFound, Message: "no toolset of this server uses a credential slot " + quote(slot)})
		return "", false
	case scope == "" && len(scopes) == 1:
		scope = scopes[0]
	case scope == "":
		writeError(w, http.StatusBadRequest, &Error{Code: CodeInvalidRequest, Message: "slot " + quote(slot) + " is used with scope tenant and with scope user: say which (scope)"})
		return "", false
	case !slices.Contains(scopes, scope):
		writeError(w, http.StatusBadRequest, &Error{Code: CodeInvalidRequest, Message: "slot " + quote(slot) + " is not used with scope " + quote(scope)})
		return "", false
	}
	p, authenticated := auth.From(r.Context())
	if scope == wire.ScopeTenant {
		if authenticated && !p.HasScope(ScopeCredentialsAdmin) {
			writeError(w, http.StatusForbidden, &Error{Code: CodeForbidden, Message: "the tenant's credential of slot " + quote(slot) + " needs the scope " + quote(ScopeCredentialsAdmin)})
			return "", false
		}
		return "", true
	}
	if p.User == "" {
		writeError(w, http.StatusForbidden, &Error{Code: CodeForbidden, Message: "slot " + quote(slot) + " holds each user's own credential, and this request's credential is not a user's"})
		return "", false
	}
	return p.User, true
}

func (s *Server) listCredentials(w http.ResponseWriter, r *http.Request) {
	if !s.credentialsOn(w) {
		return
	}
	ctx, ok := s.scope(w, r)
	if !ok {
		return
	}
	list, err := s.cfg.credentials.List(ctx, userOf(r))
	if err != nil {
		s.credentialsError(w, "listing credentials", err)
		return
	}
	if list == nil {
		list = []wire.CredentialInfo{}
	}
	writeJSON(w, http.StatusOK, wire.CredentialList{V: WireVersion, Credentials: list})
}

func (s *Server) putCredential(w http.ResponseWriter, r *http.Request) {
	if !s.credentialsOn(w) {
		return
	}
	var req wire.PutCredentialRequest
	if !s.readJSON(w, r, &req) {
		return
	}
	slot := r.PathValue("slot")
	user, ok := s.credentialOwner(w, r, slot, req.Scope)
	if !ok {
		return
	}
	ctx, ok := s.scope(w, r)
	if !ok {
		return
	}
	info, err := s.cfg.credentials.Put(ctx, user, slot, req)
	if err != nil {
		s.credentialsError(w, "storing credential "+quote(slot), err)
		return
	}
	s.log.Info("credential stored", "slot", slot, "scope", info.Scope, "kind", info.Kind, "by", keyOf(r))
	writeJSON(w, http.StatusOK, info)
}

func (s *Server) deleteCredential(w http.ResponseWriter, r *http.Request) {
	if !s.credentialsOn(w) {
		return
	}
	slot := r.PathValue("slot")
	user, ok := s.credentialOwner(w, r, slot, r.URL.Query().Get("scope"))
	if !ok {
		return
	}
	ctx, ok := s.scope(w, r)
	if !ok {
		return
	}
	if err := s.cfg.credentials.Delete(ctx, user, slot); err != nil {
		s.credentialsError(w, "deleting credential "+quote(slot), err)
		return
	}
	s.log.Info("credential deleted", "slot", slot, "by", keyOf(r))
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) connectCredential(w http.ResponseWriter, r *http.Request) {
	if !s.credentialsOn(w) {
		return
	}
	var req wire.ConnectRequest
	if !s.readJSON(w, r, &req) {
		return
	}
	slot := r.PathValue("slot")
	user, ok := s.credentialOwner(w, r, slot, req.Scope)
	if !ok {
		return
	}
	ctx, ok := s.scope(w, r)
	if !ok {
		return
	}
	resp, err := s.cfg.credentials.Connect(ctx, user, slot, req)
	if err != nil {
		s.credentialsError(w, "connecting credential "+quote(slot), err)
		return
	}
	resp.V = WireVersion
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) credentialsError(w http.ResponseWriter, what string, err error) {
	switch {
	case errors.Is(err, ErrCredentialNotFound):
		writeError(w, http.StatusNotFound, &Error{Code: CodeNotFound, Message: err.Error()})
	case errors.Is(err, ErrCredentialInvalid):
		writeError(w, http.StatusBadRequest, &Error{Code: CodeInvalidRequest, Message: err.Error()})
	case errors.Is(err, ErrCredentialForbidden):
		writeError(w, http.StatusForbidden, &Error{Code: CodeForbidden, Message: err.Error()})
	default:
		s.internal(w, what, err)
	}
}

// slotStatus is the caller's status of a toolset's credential slot, for
// GET /v1/toolsets: as given when the server keeps no credentials.
func (s *Server) slotStatus(ctx context.Context, r *http.Request, c wire.Credential) wire.Credential {
	if c.Slot == "" || s.cfg.credentials == nil {
		return c
	}
	user := ""
	if c.Scope == wire.ScopeUser {
		if user = userOf(r); user == "" {
			c.Status = wire.CredentialNotConnected
			return c
		}
	}
	st, err := s.cfg.credentials.Status(ctx, user, c.Slot)
	if err != nil {
		s.log.Warn("reading a toolset's credential status", "slot", c.Slot, "err", err)
		return c
	}
	c.Status = st
	return c
}
