package agentapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
)

// Errors an [AgentRegistry] returns, checked with [errors.Is] (or
// [errors.As] for a [*DefinitionError]); the API answers each with its own
// status. A message wrapped around one is shown to the caller.
var (
	// ErrAgentNotFound: the tenant has no stored agent of that name, or no
	// such version of it (404).
	ErrAgentNotFound = errors.New("no such agent")
	// ErrAgentReadOnly: the agent comes from the server's config file and
	// cannot be changed through the API (409 read_only).
	ErrAgentReadOnly = errors.New("the agent is read-only")
	// ErrAgentConflict: the agent changed since the caller read it, or
	// exists when the caller meant to create it (409 conflict).
	ErrAgentConflict = errors.New("the agent changed")
	// ErrVersionPublished: the version was published, now or before, and
	// is kept; only drafts are deleted (409 version_published).
	ErrVersionPublished = errors.New("the version was published")
	// ErrAgentInvalid: the request cannot be carried out as it is (400
	// invalid_request): a bad name or version number.
	ErrAgentInvalid = errors.New("invalid agent request")
)

// DefinitionError refuses an agent definition, field by field (400
// invalid_definition). Nothing was saved.
type DefinitionError struct {
	Fields []wire.FieldError
}

func (e *DefinitionError) Error() string {
	var b strings.Builder
	b.WriteString("invalid agent definition")
	for i, f := range e.Fields {
		if i == 0 {
			b.WriteString(": ")
		} else {
			b.WriteString("; ")
		}
		if f.Field != "" {
			b.WriteString(f.Field + ": ")
		}
		b.WriteString(f.Message)
	}
	return b.String()
}

// AgentRegistry keeps the agents a tenant defines through the API (T075,
// package registry implements it): each save is a new version, validated in
// full, and the published version is what new runs of the agent use. Every
// method acts in the tenant of ctx. The agents of the server's config file
// (given to [NewAgents]) are not in it: they are read-only.
type AgentRegistry interface {
	// Agent returns the tenant's stored agent at version, or at its
	// published version for 0, built to run, with [Agent.Version] set. An
	// agent with no such version (or none published) is
	// [ErrAgentNotFound].
	Agent(ctx context.Context, name string, version int64) (Agent, error)
	// Agents returns the tenant's agents that have a published version,
	// built at it, sorted by name.
	Agents(ctx context.Context) ([]Agent, error)
	// Save validates the definition and stores it as the agent's next
	// version, by the key named by; it publishes it too when req says so.
	Save(ctx context.Context, by, name string, req wire.SaveAgentRequest) (wire.AgentVersion, error)
	// Versions lists the agent's versions, oldest first.
	Versions(ctx context.Context, name string) (wire.AgentVersionList, error)
	// Version returns one version of the agent.
	Version(ctx context.Context, name string, version int64) (wire.AgentVersion, error)
	// Publish makes version the one new runs use.
	Publish(ctx context.Context, name string, version int64) (wire.AgentVersionList, error)
	// DeleteVersion removes a draft that was never published.
	DeleteVersion(ctx context.Context, name string, version int64) error
}

// WithAgentRegistry lets admins (ScopeAdmin) define agents through the API:
// POST /v1/agents/{name}/versions saves a version, POST
// /v1/agents/{name}/publish publishes one, and the endpoints under
// /v1/agents/{name}/versions list, read and delete them. GET /v1/agents
// lists the tenant's published agents after those of [NewAgents], which are
// read-only, and a run of a stored agent records the version it runs
// (GET /v1/runs/{id} "agent_version").
func WithAgentRegistry(r AgentRegistry) Option { return func(c *config) { c.registry = r } }

// agentFor finds the agent of name: one of the server's, or the tenant's
// stored agent (at version, or its published one for 0), as it runs on
// model (canonical; "" for its default). An agent the server does not run
// is [errNoAgent].
func (s *Server) agentFor(ctx context.Context, name string, version int64, model string) (*agent, error) {
	if a := s.agents[name]; a != nil {
		return s.withModel(ctx, a, model)
	}
	if s.cfg.registry == nil || !validName.MatchString(name) {
		return nil, fmt.Errorf("%w: %q", errNoAgent, name)
	}
	a, err := s.cfg.registry.Agent(ctx, name, version)
	if errors.Is(err, ErrAgentNotFound) {
		return nil, fmt.Errorf("%w: %w", errNoAgent, err)
	}
	if err != nil {
		return nil, err
	}
	if (a.Harness == nil) == (a.Runner == nil) || a.Name != name {
		return nil, fmt.Errorf("agent registry: agent %q came back as %q, with a harness and a runner or neither", name, a.Name)
	}
	return s.withModel(ctx, &agent{Agent: a}, model)
}

// storedAgents are the tenant's published agents, after the server's.
func (s *Server) storedAgents(ctx context.Context) ([]*agent, error) {
	if s.cfg.registry == nil {
		return nil, nil
	}
	as, err := s.cfg.registry.Agents(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*agent, 0, len(as))
	for _, a := range as {
		if s.agents[a.Name] == nil {
			out = append(out, &agent{Agent: a})
		}
	}
	return out, nil
}

// registryName checks a request's {name} for the registry endpoints,
// answering 404 when the server keeps no agents, 409 for an agent of the
// config file when write is set, and 400 for a bad name.
func (s *Server) registryName(w http.ResponseWriter, r *http.Request, write bool) (string, bool) {
	name := r.PathValue("name")
	switch {
	case s.cfg.registry == nil:
		writeError(w, http.StatusNotFound, &Error{Code: CodeNotFound, Message: "this server keeps no agent definitions"})
		return "", false
	case !validName.MatchString(name):
		writeError(w, http.StatusBadRequest, &Error{Code: CodeInvalidRequest, Message: "agent name " + quote(name) + " must match " + validName.String()})
		return "", false
	case write && s.agents[name] != nil:
		writeError(w, http.StatusConflict, readOnly(name))
		return "", false
	}
	return name, true
}

func readOnly(name string) *Error {
	return &Error{Code: CodeReadOnly, Message: "agent " + quote(name) + " comes from the server's config file, and is read-only: change it there"}
}

// versionOf reads a request's {version}: a positive number.
func versionOf(w http.ResponseWriter, r *http.Request) (int64, bool) {
	v, err := strconv.ParseInt(r.PathValue("version"), 10, 64)
	if err != nil || v < 1 {
		writeError(w, http.StatusBadRequest, &Error{Code: CodeInvalidRequest, Message: "version " + quote(r.PathValue("version")) + " is not a positive number"})
		return 0, false
	}
	return v, true
}

func (s *Server) saveAgent(w http.ResponseWriter, r *http.Request) {
	name, ok := s.registryName(w, r, true)
	if !ok {
		return
	}
	var req wire.SaveAgentRequest
	if !s.readJSON(w, r, &req) {
		return
	}
	// An admin defines only agents it could run: every toolset the
	// definition names must be one its key may use.
	var uses struct {
		Toolsets        []string `json:"toolsets"`
		RuntimeToolsets []string `json:"runtime_toolsets"`
	}
	_ = json.Unmarshal(req.Definition, &uses) // the registry reports a malformed definition
	acc := accessOf(r)
	for _, ts := range slices.Concat(uses.Toolsets, uses.RuntimeToolsets) {
		if !acc.toolset(ts) {
			writeError(w, http.StatusForbidden, &Error{Code: CodeForbidden, Message: "this credential may not use toolset " + quote(ts)})
			return
		}
	}
	ctx, ok := s.scope(w, r)
	if !ok {
		return
	}
	v, err := s.cfg.registry.Save(ctx, keyOf(r), name, req)
	if err != nil {
		s.registryError(w, "saving agent "+quote(name), err)
		return
	}
	s.log.Info("agent saved", "agent", name, "version", v.Version, "published", v.Published, "by", keyOf(r))
	w.Header().Set("Location", "/v1/agents/"+name+"/versions/"+strconv.FormatInt(v.Version, 10))
	writeJSON(w, http.StatusCreated, v)
}

func (s *Server) listVersions(w http.ResponseWriter, r *http.Request) {
	name, ok := s.registryName(w, r, false)
	if !ok {
		return
	}
	if s.agents[name] != nil {
		writeJSON(w, http.StatusOK, wire.AgentVersionList{V: WireVersion, Name: name, ReadOnly: true, Versions: []wire.AgentVersion{}})
		return
	}
	ctx, ok := s.scope(w, r)
	if !ok {
		return
	}
	l, err := s.cfg.registry.Versions(ctx, name)
	if err != nil {
		s.registryError(w, "listing agent "+quote(name)+"'s versions", err)
		return
	}
	writeJSON(w, http.StatusOK, l)
}

func (s *Server) getVersion(w http.ResponseWriter, r *http.Request) {
	name, ok := s.registryName(w, r, false)
	if !ok {
		return
	}
	version, ok := versionOf(w, r)
	if !ok {
		return
	}
	ctx, ok := s.scope(w, r)
	if !ok {
		return
	}
	v, err := s.cfg.registry.Version(ctx, name, version)
	if err != nil {
		s.registryError(w, "reading agent "+quote(name)+"'s version", err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) deleteVersion(w http.ResponseWriter, r *http.Request) {
	name, ok := s.registryName(w, r, true)
	if !ok {
		return
	}
	version, ok := versionOf(w, r)
	if !ok {
		return
	}
	ctx, ok := s.scope(w, r)
	if !ok {
		return
	}
	if err := s.cfg.registry.DeleteVersion(ctx, name, version); err != nil {
		s.registryError(w, "deleting agent "+quote(name)+"'s version", err)
		return
	}
	s.log.Info("agent version deleted", "agent", name, "version", version, "by", keyOf(r))
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) publishAgent(w http.ResponseWriter, r *http.Request) {
	name, ok := s.registryName(w, r, true)
	if !ok {
		return
	}
	var req wire.PublishRequest
	if !s.readJSON(w, r, &req) {
		return
	}
	if req.Version < 1 {
		writeError(w, http.StatusBadRequest, &Error{Code: CodeInvalidRequest, Message: "version must be a positive number"})
		return
	}
	ctx, ok := s.scope(w, r)
	if !ok {
		return
	}
	l, err := s.cfg.registry.Publish(ctx, name, req.Version)
	if err != nil {
		s.registryError(w, "publishing agent "+quote(name), err)
		return
	}
	s.log.Info("agent published", "agent", name, "version", req.Version, "by", keyOf(r))
	writeJSON(w, http.StatusOK, l)
}

// registryError answers an [AgentRegistry]'s error with its status.
func (s *Server) registryError(w http.ResponseWriter, what string, err error) {
	var def *DefinitionError
	if errors.As(err, &def) {
		writeError(w, http.StatusBadRequest, &Error{Code: CodeInvalidDefinition, Message: def.Error(), Fields: slices.Clone(def.Fields)})
		return
	}
	for _, m := range []struct {
		err    error
		status int
		code   string
	}{
		{ErrAgentNotFound, http.StatusNotFound, CodeNotFound},
		{ErrAgentReadOnly, http.StatusConflict, CodeReadOnly},
		{ErrAgentConflict, http.StatusConflict, CodeConflict},
		{ErrVersionPublished, http.StatusConflict, CodeVersionPublished},
		{ErrAgentInvalid, http.StatusBadRequest, CodeInvalidRequest},
	} {
		if errors.Is(err, m.err) {
			writeError(w, m.status, &Error{Code: m.code, Message: err.Error()})
			return
		}
	}
	s.internal(w, what, err)
}
