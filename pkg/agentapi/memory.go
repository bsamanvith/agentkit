package agentapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
	"github.com/bsamanvith/agentkit/pkg/auth"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

// Errors a [Memory] returns, checked with [errors.Is]; the API answers each
// with its own status and the error's message.
var (
	// ErrMemoryNotFound: nothing is at the path (404).
	ErrMemoryNotFound = errors.New("no such memory")
	// ErrMemoryInvalid: the path is not one memory has (400).
	ErrMemoryInvalid = errors.New("invalid memory path")
)

// Memory is an agent's memory (its context store, package memory) as the API
// shows it to the people it remembers things about: read it and delete from
// it. Every method acts for the tenant, and with scope "user" the user, of
// ctx.
type Memory interface {
	// Scope is wire.ScopeUser (each user's own memory) or
	// wire.ScopeTenant (one the tenant shares).
	Scope() string
	// View returns a file or a directory listing, with the memory's usage
	// and limits; Agent, V and Scope are the server's to fill.
	View(ctx context.Context, path string) (wire.MemoryView, error)
	// Delete removes a file, or a directory and everything in it.
	Delete(ctx context.Context, path string) error
}

// WithMemory serves GET and DELETE /v1/agents/{name}/memory for the agents
// of memories, by name. Viewing needs agent:read; deleting needs agent:run
// for the caller's own memory (scope user), and agent:admin for a
// tenant's shared one. An agent without memory answers 404.
func WithMemory(memories map[string]Memory) Option {
	return func(c *config) { c.memories = memories }
}

// memoryOf finds the memory of the request's agent, answering 404 for an
// agent the caller cannot see or that has none, and 403 for user-scoped
// memory and a request that is not a user's (an open server's included). It
// returns the request's tenant context, with the principal's user for
// user-scoped memory.
func (s *Server) memoryOf(w http.ResponseWriter, r *http.Request) (context.Context, Memory, bool) {
	name := r.PathValue("name")
	ctx, ok := s.scope(w, r)
	if !ok {
		return nil, nil, false
	}
	a, err := s.agentFor(ctx, name, 0, "")
	if err != nil && !errors.Is(err, errNoAgent) {
		s.internal(w, "reading agent "+quote(name), err)
		return nil, nil, false
	}
	if a == nil || !accessOf(r).agent(a) {
		writeError(w, http.StatusNotFound, &Error{Code: CodeNotFound, Message: "no agent " + quote(name)})
		return nil, nil, false
	}
	m := s.cfg.memories[name]
	if m == nil {
		writeError(w, http.StatusNotFound, &Error{Code: CodeNotFound, Message: "agent " + quote(name) + " keeps no memory"})
		return nil, nil, false
	}
	if m.Scope() == wire.ScopeUser {
		// The memory is keyed by the principal's user, always.
		p, _ := auth.From(r.Context())
		if p.User == "" {
			writeError(w, http.StatusForbidden, &Error{Code: CodeForbidden, Message: "agent " + quote(name) + " keeps each user's own memory, and this request's credential is not a user's"})
			return nil, nil, false
		}
		ctx = tenancy.WithUser(ctx, p.User)
	}
	return ctx, m, true
}

// memoryPath is the request's path query, by default the memory's root.
func memoryPath(r *http.Request) string {
	if p := r.URL.Query().Get("path"); p != "" {
		return p
	}
	return "/memories"
}

func (s *Server) getMemory(w http.ResponseWriter, r *http.Request) {
	ctx, m, ok := s.memoryOf(w, r)
	if !ok {
		return
	}
	v, err := m.View(ctx, memoryPath(r))
	if err != nil {
		s.memoryError(w, "viewing memory", err)
		return
	}
	v.V, v.Agent, v.Scope = WireVersion, r.PathValue("name"), m.Scope()
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) deleteMemory(w http.ResponseWriter, r *http.Request) {
	ctx, m, ok := s.memoryOf(w, r)
	if !ok {
		return
	}
	if p, ok := auth.From(r.Context()); ok && m.Scope() == wire.ScopeTenant && !p.HasScope(ScopeAdmin) {
		writeError(w, http.StatusForbidden, &Error{Code: CodeForbidden, Message: "agent " + quote(r.PathValue("name")) + "'s memory is the tenant's: deleting from it needs the scope " + quote(ScopeAdmin)})
		return
	}
	path := memoryPath(r)
	if err := m.Delete(ctx, path); err != nil {
		s.memoryError(w, "deleting memory", err)
		return
	}
	s.log.Info("memory deleted", "agent", r.PathValue("name"), "path", path, "by", keyOf(r))
	w.WriteHeader(http.StatusNoContent)
}

// memoryError answers a Memory's error.
func (s *Server) memoryError(w http.ResponseWriter, what string, err error) {
	switch {
	case errors.Is(err, ErrMemoryNotFound):
		writeError(w, http.StatusNotFound, &Error{Code: CodeNotFound, Message: err.Error()})
	case errors.Is(err, ErrMemoryInvalid):
		writeError(w, http.StatusBadRequest, &Error{Code: CodeInvalidRequest, Message: err.Error()})
	default:
		s.internal(w, what, err)
	}
}
