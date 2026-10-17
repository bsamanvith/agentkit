package agentapi

import (
	"context"
	"errors"
	"net/http"
	"slices"

	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
	"github.com/bsamanvith/agentkit/pkg/auth"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

// ScopeAdmin is the scope that adds, reviews, changes and removes toolsets
// at run time ([ToolsetAdmin]). "*" grants it.
const ScopeAdmin = "agent:admin"

// Errors a [ToolsetAdmin] returns, checked with [errors.Is]; the API
// answers each with its own status. A message wrapped around one is shown
// to the caller, so it must never hold a credential.
var (
	// ErrToolsetExists: a toolset already has the name (409 toolset_exists).
	ErrToolsetExists = errors.New("a toolset has that name")
	// ErrToolsetNotFound: no toolset of the caller's tenant has the name
	// (404).
	ErrToolsetNotFound = errors.New("no such toolset")
	// ErrToolsetInvalid: the request cannot be carried out as it is (400
	// invalid_request): a bad name, URL or header, a refused address, too
	// many toolsets, an unknown tool.
	ErrToolsetInvalid = errors.New("invalid toolset request")
	// ErrToolsetConflict: the toolset or a tool changed since the caller
	// read it (409 conflict). Read it again and decide again.
	ErrToolsetConflict = errors.New("the toolset changed")
	// ErrServerUnreachable: the MCP server could not be reached or used
	// (422 server_unreachable).
	ErrServerUnreachable = errors.New("the MCP server could not be used")
	// ErrAuthRequired: the MCP server refused agentkit for want of a
	// credential (422 auth_required).
	ErrAuthRequired = errors.New("the MCP server needs a credential")
)

// ToolsetAdmin keeps the toolsets a tenant adds at run time: external MCP
// servers, added by URL, whose tools an admin reviews before agents may call
// them (package mcp/servers implements it). Every method acts in the tenant
// of ctx. by names who acts, for the record (a key's ID).
type ToolsetAdmin interface {
	// Add probes the server and adds it as a toolset, enabled, with every
	// tool new.
	Add(ctx context.Context, by string, req wire.AddToolsetRequest) (wire.ServerToolset, error)
	// List returns every toolset of the tenant, sorted by name.
	List(ctx context.Context) ([]wire.ServerToolset, error)
	// Get returns a toolset with the review of its tools.
	Get(ctx context.Context, name string) (wire.ServerToolset, error)
	// Review records decisions on tools.
	Review(ctx context.Context, name, by string, req wire.ReviewRequest) (wire.ServerToolset, error)
	// Update changes a toolset.
	Update(ctx context.Context, name string, req wire.UpdateToolsetRequest) (wire.ServerToolset, error)
	// Delete removes a toolset.
	Delete(ctx context.Context, name string) error
	// Tools returns the tools agents are offered now, under the server's
	// names: the approved ones the server lists unchanged.
	Tools(ctx context.Context, name string) ([]tool.Definition, error)
}

// WithToolsetAdmin lets admins (ScopeAdmin) add toolsets at run time,
// through POST /v1/toolsets and the endpoints under /v1/toolsets/{name}.
// GET /v1/toolsets lists them after the toolsets of [WithToolsets]. An
// agent names the ones it uses in [Agent.RuntimeToolsets].
func WithToolsetAdmin(a ToolsetAdmin) Option { return func(c *config) { c.admin = a } }

// keyOf names the request's credential, for the record: its key's ID, or
// "" on an open server.
func keyOf(r *http.Request) string {
	if p, ok := auth.From(r.Context()); ok {
		return p.Via.ID
	}
	return ""
}

// readJSON reads a request's body as one JSON object into v, answering 400
// or 413 and returning false when it cannot.
func (s *Server) readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	body, apiErr, code := s.readBody(w, r)
	if apiErr != nil {
		writeError(w, code, apiErr)
		return false
	}
	if err := decodeStrict(body, v); err != nil {
		writeError(w, http.StatusBadRequest, &Error{Code: CodeInvalidRequest, Message: "the body must be a JSON object: " + err.Error()})
		return false
	}
	return true
}

// adminName finds the runtime toolset of a request's {name}, writing
// 404 when there is none the caller may see, or no admin.
func (s *Server) adminName(w http.ResponseWriter, r *http.Request) (string, bool) {
	name := r.PathValue("name")
	if s.cfg.admin == nil || s.toolsets[name] != nil || !accessOf(r).toolset(name) {
		writeError(w, http.StatusNotFound, &Error{Code: CodeNotFound, Message: "no toolset " + quote(name) + " added at run time"})
		return "", false
	}
	return name, true
}

func (s *Server) addToolset(w http.ResponseWriter, r *http.Request) {
	if s.cfg.admin == nil {
		writeError(w, http.StatusNotFound, &Error{Code: CodeNotFound, Message: "this server does not add toolsets at run time"})
		return
	}
	var req wire.AddToolsetRequest
	if !s.readJSON(w, r, &req) {
		return
	}
	switch {
	case s.toolsets[req.Name] != nil:
		writeError(w, http.StatusConflict, &Error{Code: CodeToolsetExists, Message: "the server's config has a toolset " + quote(req.Name)})
		return
	case !accessOf(r).toolset(req.Name):
		writeError(w, http.StatusForbidden, &Error{Code: CodeForbidden, Message: "this credential may not use a toolset named " + quote(req.Name)})
		return
	}
	ctx, ok := s.scope(w, r)
	if !ok {
		return
	}
	ts, err := s.cfg.admin.Add(ctx, keyOf(r), req)
	if err != nil {
		s.adminError(w, "adding toolset "+quote(req.Name), err)
		return
	}
	s.log.Info("toolset added", "toolset", ts.Name, "url", ts.URL, "by", keyOf(r))
	w.Header().Set("Location", "/v1/toolsets/"+ts.Name+"/review")
	writeJSON(w, http.StatusCreated, ts)
}

func (s *Server) getReview(w http.ResponseWriter, r *http.Request) {
	name, ok := s.adminName(w, r)
	if !ok {
		return
	}
	ctx, ok := s.scope(w, r)
	if !ok {
		return
	}
	ts, err := s.cfg.admin.Get(ctx, name)
	if err != nil {
		s.adminError(w, "reading toolset "+quote(name), err)
		return
	}
	writeJSON(w, http.StatusOK, ts)
}

func (s *Server) postReview(w http.ResponseWriter, r *http.Request) {
	name, ok := s.adminName(w, r)
	if !ok {
		return
	}
	var req wire.ReviewRequest
	if !s.readJSON(w, r, &req) {
		return
	}
	ctx, ok := s.scope(w, r)
	if !ok {
		return
	}
	ts, err := s.cfg.admin.Review(ctx, name, keyOf(r), req)
	if err != nil {
		s.adminError(w, "reviewing toolset "+quote(name), err)
		return
	}
	for _, d := range req.Tools {
		s.log.Info("toolset tool reviewed", "toolset", name, "tool", d.Name, "decision", d.Decision, "fingerprint", d.Fingerprint, "by", keyOf(r))
	}
	writeJSON(w, http.StatusOK, ts)
}

func (s *Server) patchToolset(w http.ResponseWriter, r *http.Request) {
	name, ok := s.adminName(w, r)
	if !ok {
		return
	}
	var req wire.UpdateToolsetRequest
	if !s.readJSON(w, r, &req) {
		return
	}
	ctx, ok := s.scope(w, r)
	if !ok {
		return
	}
	ts, err := s.cfg.admin.Update(ctx, name, req)
	if err != nil {
		s.adminError(w, "changing toolset "+quote(name), err)
		return
	}
	s.log.Info("toolset changed", "toolset", name, "enabled", ts.Enabled, "by", keyOf(r))
	writeJSON(w, http.StatusOK, ts)
}

func (s *Server) deleteToolset(w http.ResponseWriter, r *http.Request) {
	name, ok := s.adminName(w, r)
	if !ok {
		return
	}
	ctx, ok := s.scope(w, r)
	if !ok {
		return
	}
	if err := s.cfg.admin.Delete(ctx, name); err != nil {
		s.adminError(w, "removing toolset "+quote(name), err)
		return
	}
	s.log.Info("toolset removed", "toolset", name, "by", keyOf(r))
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

// adminError answers a [ToolsetAdmin]'s error with its status.
func (s *Server) adminError(w http.ResponseWriter, what string, err error) {
	for _, m := range []struct {
		err    error
		status int
		code   string
	}{
		{ErrToolsetNotFound, http.StatusNotFound, CodeNotFound},
		{ErrToolsetExists, http.StatusConflict, CodeToolsetExists},
		{ErrToolsetConflict, http.StatusConflict, CodeConflict},
		{ErrToolsetInvalid, http.StatusBadRequest, CodeInvalidRequest},
		{ErrAuthRequired, http.StatusUnprocessableEntity, CodeAuthRequired},
		{ErrServerUnreachable, http.StatusUnprocessableEntity, CodeServerUnreachable},
	} {
		if errors.Is(err, m.err) {
			writeError(w, m.status, &Error{Code: m.code, Message: err.Error()})
			return
		}
	}
	s.internal(w, what, err)
}

// runtimeToolsets lists the toolsets added at run time the caller may see,
// for GET /v1/toolsets.
func (s *Server) runtimeToolsets(ctx context.Context, acc access) ([]wire.Toolset, error) {
	if s.cfg.admin == nil {
		return nil, nil
	}
	all, err := s.cfg.admin.List(ctx)
	if err != nil {
		return nil, err
	}
	var out []wire.Toolset
	for _, ts := range all {
		if s.toolsets[ts.Name] != nil || !acc.toolset(ts.Name) {
			continue
		}
		v := wire.Toolset{
			Name: ts.Name, Type: ts.Type, Status: ts.Status, Reason: ts.Reason, Runtime: true,
			Credential: wire.Credential{Status: wire.CredentialNotNeeded},
		}
		if ts.Auth == wire.AuthHeaders {
			v.Credential.Status = wire.CredentialConfigured
		}
		for _, t := range ts.Tools {
			if t.Status == wire.ToolApproved {
				v.ToolCount++
			}
		}
		if !ts.Enabled {
			v.ToolCount = 0
		}
		out = append(out, v)
	}
	return out, nil
}

// runtimeTools answers GET /v1/toolsets/{name}/tools for a toolset added
// at run time; false when there is none of that name.
func (s *Server) runtimeTools(w http.ResponseWriter, r *http.Request, name string) bool {
	if s.cfg.admin == nil {
		return false
	}
	ctx, ok := s.scope(w, r)
	if !ok {
		return true
	}
	defs, err := s.cfg.admin.Tools(ctx, name)
	switch {
	case errors.Is(err, ErrToolsetNotFound):
		return false
	case err != nil:
		s.log.Warn("listing a toolset's tools", "toolset", name, "err", err)
		writeJSON(w, http.StatusOK, wire.ToolList{V: WireVersion, Toolset: name, Status: wire.ToolsetUnavailable, Tools: []wire.Tool{}})
		return true
	}
	out := wire.ToolList{V: WireVersion, Toolset: name, Status: wire.ToolsetReady, Tools: []wire.Tool{}}
	for _, d := range defs {
		out.Tools = append(out.Tools, wireTool(d))
	}
	writeJSON(w, http.StatusOK, out)
	return true
}

// runtimeAgentToolset describes a toolset added at run time that an agent
// names, with its tools as the agent calls them.
func (s *Server) runtimeAgentToolset(ctx context.Context, name string) wire.AgentToolset {
	at := wire.AgentToolset{Name: name, Status: wire.ToolsetNotAdded, Tools: []string{}}
	if s.cfg.admin == nil {
		return at
	}
	defs, err := s.cfg.admin.Tools(ctx, name)
	switch {
	case errors.Is(err, ErrToolsetNotFound):
		return at
	case err != nil:
		at.Status = wire.ToolsetUnavailable
		return at
	}
	at.Status = wire.ToolsetReady
	for _, d := range defs {
		at.Tools = append(at.Tools, name+ToolPrefixSeparator+d.Name)
	}
	slices.Sort(at.Tools)
	return at
}
