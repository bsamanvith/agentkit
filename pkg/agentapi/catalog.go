package agentapi

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

// DefaultAgent is the name of the agent [New] serves, and of the agent a run
// gets when it names none and the server has several.
const DefaultAgent = "default"

// Conversation listing limits: ?limit= defaults to DefaultListLimit and may
// be at most MaxListLimit.
const (
	DefaultListLimit = 20
	MaxListLimit     = 100
)

// titleRunes is the length of a conversation's title, in characters.
const titleRunes = 80

// validName is the rule for agent names: the tool-name rule.
var validName = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

// Agent is one agent the server runs: its harness, which runs it, and what
// GET /v1/agents says about it.
type Agent struct {
	// Name identifies the agent in requests; it must match
	// ^[a-zA-Z0-9_-]{1,64}$ and be unique.
	Name        string
	Description string
	// Harness runs the agent: its model, tools, system prompt and budgets.
	Harness *harness.Harness
	// Model describes the model, for clients (the harness has the model).
	Model wire.Model
	// System is the system prompt the harness was built with, shown by
	// GET /v1/agents/{name}.
	System string
	// Toolsets name the toolsets (given with [WithToolsets]) whose tools the
	// harness has, and PrefixToolNames says whether they are named
	// "<toolset>__<tool>" ([MergeTools]); GET /v1/agents lists the names.
	Toolsets        []string
	PrefixToolNames bool
	// Skills and Memory describe the agent's skills and long-term memory.
	Skills []string
	Memory bool
}

// Toolset is a toolset of the server, as GET /v1/toolsets lists it.
type Toolset struct {
	// Name and Type are the toolset's config name and its connector type.
	Name, Type string
	// Tools is the toolset's registry; nil for a skipped toolset.
	Tools tool.Registry
	// Skipped says why an optional toolset was skipped; empty when it was
	// built. Exactly one of Tools and Skipped is set.
	Skipped string
	// MCPPath is where the server serves the toolset over MCP ("/mcp/demo"),
	// empty if it does not.
	MCPPath string
	// Credential says where the toolset's credential comes from. It must
	// never hold a value.
	Credential Credential
}

// WithToolsets lists the server's toolsets in GET /v1/toolsets, and names
// the ones agents use. Without it the server lists none.
func WithToolsets(sets ...Toolset) Option {
	return func(c *config) { c.toolsets = slices.Clone(sets) }
}

// WithTenant sets how the server tells which tenant a request acts for
// (default: every request is the same tenant). A conversation, and its runs,
// are visible only to requests of the tenant that started it: to others they
// do not exist (404), and GET /v1/conversations lists only the caller's.
// Authentication middleware passes the tenant of the request's principal.
func WithTenant(f func(*http.Request) string) Option { return func(c *config) { c.tenant = f } }

// NewAgents returns the agent API for several agents. A run names its agent
// (POST /v1/runs "agent"), or gets the default one: the only agent, else the
// one named [DefaultAgent]. It fails with [ErrInvalidConfig] for no agents,
// a nil harness, an invalid or duplicate name, an agent naming a toolset
// [WithToolsets] does not give, or an option with an unusable value.
func NewAgents(agents []Agent, opts ...Option) (*Server, error) {
	cfg := newConfig(opts)
	if len(agents) == 0 {
		return nil, fmt.Errorf("%w: no agents", ErrInvalidConfig)
	}
	sets := map[string]*Toolset{}
	for i := range cfg.toolsets {
		ts := &cfg.toolsets[i]
		switch {
		case !validName.MatchString(ts.Name):
			return nil, fmt.Errorf("%w: toolset name %q must match %s", ErrInvalidConfig, ts.Name, validName)
		case sets[ts.Name] != nil:
			return nil, fmt.Errorf("%w: toolset %q given twice", ErrInvalidConfig, ts.Name)
		case (ts.Tools == nil) == (ts.Skipped == ""):
			return nil, fmt.Errorf("%w: toolset %q needs either its tools or why it was skipped", ErrInvalidConfig, ts.Name)
		}
		sets[ts.Name] = ts
	}
	byName := map[string]*agent{}
	order := make([]*agent, 0, len(agents))
	for _, a := range agents {
		switch {
		case a.Harness == nil:
			return nil, fmt.Errorf("%w: agent %q: nil harness", ErrInvalidConfig, a.Name)
		case !validName.MatchString(a.Name):
			return nil, fmt.Errorf("%w: agent name %q must match %s", ErrInvalidConfig, a.Name, validName)
		case byName[a.Name] != nil:
			return nil, fmt.Errorf("%w: two agents named %q", ErrInvalidConfig, a.Name)
		}
		for _, name := range a.Toolsets {
			if sets[name] == nil {
				return nil, fmt.Errorf("%w: agent %q uses toolset %q, which WithToolsets does not give", ErrInvalidConfig, a.Name, name)
			}
		}
		a.Toolsets, a.Skills = slices.Clone(a.Toolsets), slices.Clone(a.Skills)
		ag := &agent{Agent: a}
		byName[a.Name] = ag
		order = append(order, ag)
	}
	var def *agent
	switch {
	case len(order) == 1:
		def = order[0]
	case byName[DefaultAgent] != nil:
		def = byName[DefaultAgent]
	}
	return newServer(cfg, byName, order, def, sets)
}

// agent is an agent of the server.
type agent struct {
	Agent
}

// resolveAgent picks the agent of a new conversation.
func (s *Server) resolveAgent(name string) (*agent, *Error, int) {
	if name == "" {
		if s.defaultAgent == nil {
			return nil, &Error{Code: CodeInvalidRequest, Message: "name an agent (\"agent\"); this server runs " + strings.Join(s.agentNames(), ", ")}, http.StatusBadRequest
		}
		return s.defaultAgent, nil, 0
	}
	a := s.agents[name]
	if a == nil {
		return nil, &Error{Code: CodeNotFound, Message: "no agent " + quote(name) + "; this server runs " + strings.Join(s.agentNames(), ", ")}, http.StatusNotFound
	}
	return a, nil, 0
}

func (s *Server) agentNames() []string {
	names := make([]string, len(s.agentOrder))
	for i, a := range s.agentOrder {
		names[i] = a.Name
	}
	return names
}

func (s *Server) listAgents(w http.ResponseWriter, r *http.Request) {
	out := wire.AgentList{V: WireVersion, Agents: make([]wire.Agent, 0, len(s.agentOrder))}
	for _, a := range s.agentOrder {
		v, err := s.agentView(r, a)
		if err != nil {
			s.internal(w, "listing the agents' tools", err)
			return
		}
		out.Agents = append(out.Agents, v)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) getAgent(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	a := s.agents[name]
	if a == nil {
		writeError(w, http.StatusNotFound, &Error{Code: CodeNotFound, Message: "no agent " + quote(name)})
		return
	}
	v, err := s.agentView(r, a)
	if err != nil {
		s.internal(w, "listing the agent's tools", err)
		return
	}
	// TODO(T040): the system prompt is for admins only; return it to every
	// caller until the API has roles.
	v.System = a.System
	writeJSON(w, http.StatusOK, v)
}

// agentView describes an agent, without its system prompt.
func (s *Server) agentView(r *http.Request, a *agent) (wire.Agent, error) {
	v := wire.Agent{
		V: WireVersion, Name: a.Name, Description: a.Description, Default: a == s.defaultAgent,
		Model: a.Model, Toolsets: make([]wire.AgentToolset, 0, len(a.Toolsets)),
		Skills: append([]string{}, a.Skills...), Memory: a.Memory,
	}
	for _, name := range a.Toolsets {
		ts := s.toolsets[name]
		at := wire.AgentToolset{Name: name, Status: wire.ToolsetSkipped, Tools: []string{}}
		if ts.Tools != nil {
			defs, err := ts.Tools.List(r.Context())
			if err != nil {
				return v, fmt.Errorf("toolset %q: %w", name, err)
			}
			at.Status = wire.ToolsetReady
			for _, d := range defs {
				if a.PrefixToolNames {
					d.Name = name + ToolPrefixSeparator + d.Name
				}
				at.Tools = append(at.Tools, d.Name)
			}
		}
		v.Toolsets = append(v.Toolsets, at)
	}
	return v, nil
}

func (s *Server) listToolsets(w http.ResponseWriter, r *http.Request) {
	out := wire.ToolsetList{V: WireVersion, Toolsets: make([]wire.Toolset, 0, len(s.cfg.toolsets))}
	for _, ts := range s.cfg.toolsets {
		v := wire.Toolset{Name: ts.Name, Type: ts.Type, Status: wire.ToolsetSkipped, Reason: ts.Skipped, MCPPath: ts.MCPPath, Credential: ts.Credential}
		if ts.Tools != nil {
			defs, err := ts.Tools.List(r.Context())
			if err != nil {
				s.internal(w, "listing toolset "+ts.Name, err)
				return
			}
			v.Status, v.ToolCount = wire.ToolsetReady, len(defs)
		}
		out.Toolsets = append(out.Toolsets, v)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) listTools(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	ts := s.toolsets[name]
	if ts == nil {
		writeError(w, http.StatusNotFound, &Error{Code: CodeNotFound, Message: "no toolset " + quote(name)})
		return
	}
	out := wire.ToolList{V: WireVersion, Toolset: name, Status: wire.ToolsetSkipped, Tools: []wire.Tool{}}
	if ts.Tools != nil {
		defs, err := ts.Tools.List(r.Context())
		if err != nil {
			s.internal(w, "listing toolset "+name, err)
			return
		}
		out.Status = wire.ToolsetReady
		for _, d := range defs {
			out.Tools = append(out.Tools, wire.Tool{Name: d.Name, Description: d.Description, InputSchema: d.InputSchema, OutputSchema: d.OutputSchema})
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// listConversations serves GET /v1/conversations: the caller's
// conversations, most recently started first. Pages follow the order of
// creation, so conversations started while a client pages land before its
// first page: it sees every older one exactly once.
func (s *Server) listConversations(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := DefaultListLimit
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > MaxListLimit {
			writeError(w, http.StatusBadRequest, &Error{Code: CodeInvalidRequest, Message: fmt.Sprintf("limit must be a number from 1 to %d", MaxListLimit)})
			return
		}
		limit = n
	}
	var before uint64 // list conversations created before this one; 0: from the newest
	if v := q.Get("cursor"); v != "" {
		seq, ok := decodeCursor(v)
		if !ok {
			writeError(w, http.StatusBadRequest, &Error{Code: CodeInvalidRequest, Message: "cursor is not one this server gave"})
			return
		}
		before = seq
	}
	agentName := q.Get("agent")
	if agentName != "" && s.agents[agentName] == nil {
		writeError(w, http.StatusNotFound, &Error{Code: CodeNotFound, Message: "no agent " + quote(agentName)})
		return
	}
	tenant := s.tenantOf(r)

	s.mu.Lock()
	var page []*conversation
	for _, c := range s.convs {
		if c.tenant == tenant && (agentName == "" || c.agent.Name == agentName) && (before == 0 || c.seq < before) {
			page = append(page, c)
		}
	}
	slices.SortFunc(page, func(a, b *conversation) int { return compareDesc(a.seq, b.seq) })
	out := wire.ConversationList{V: WireVersion, Conversations: []wire.ConversationSummary{}}
	if len(page) > limit {
		page = page[:limit]
		out.NextCursor = encodeCursor(page[limit-1].seq)
	}
	for _, c := range page {
		out.Conversations = append(out.Conversations, wire.ConversationSummary{
			ID: c.id, Agent: c.agent.Name, Title: c.title, Status: c.status(), MessageCount: len(c.messages),
			CreatedAt: c.created, UpdatedAt: c.updated,
		})
	}
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, out)
}

// deleteConversation serves DELETE /v1/conversations/{id}: the conversation
// and its runs are gone (204). One with a run going on is 409
// conversation_busy.
func (s *Server) deleteConversation(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	tenant := s.tenantOf(r)
	s.mu.Lock()
	c := s.convs[id]
	switch {
	case c == nil || c.tenant != tenant:
		s.mu.Unlock()
		writeError(w, http.StatusNotFound, notFoundConversation(id))
		return
	case c.running:
		s.mu.Unlock()
		writeError(w, http.StatusConflict, &Error{Code: CodeBusy, Message: "a run of this conversation is going on; wait for it to end", Retryable: true})
		return
	}
	delete(s.convs, id)
	kept := s.runOrder[:0]
	for _, ru := range s.runOrder {
		if ru.convID == id {
			delete(s.runs, ru.id)
			continue
		}
		kept = append(kept, ru)
	}
	clear(s.runOrder[len(kept):])
	s.runOrder = kept
	s.mu.Unlock()
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) tenantOf(r *http.Request) string {
	if s.cfg.tenant == nil {
		return ""
	}
	return s.cfg.tenant(r)
}

// title is the start of a conversation's first message, on one line.
func title(message string) string {
	t := strings.Join(strings.Fields(message), " ")
	if utf8.RuneCountInString(t) <= titleRunes {
		return t
	}
	r := []rune(t)
	return strings.TrimRight(string(r[:titleRunes-1]), " ") + "…"
}

// cursorPrefix makes cursors this server's: an opaque token is base64url of
// it and the sequence number of the last conversation of the page.
const cursorPrefix = "conversations-before:"

func encodeCursor(seq uint64) string {
	return base64.RawURLEncoding.EncodeToString([]byte(cursorPrefix + strconv.FormatUint(seq, 10)))
}

// decodeCursor accepts exactly what encodeCursor makes.
func decodeCursor(s string) (uint64, bool) {
	b, err := base64.RawURLEncoding.Strict().DecodeString(s)
	if err != nil {
		return 0, false
	}
	digits, ok := strings.CutPrefix(string(b), cursorPrefix)
	if !ok {
		return 0, false
	}
	seq, err := strconv.ParseUint(digits, 10, 64)
	if err != nil || seq == 0 || encodeCursor(seq) != s {
		return 0, false
	}
	return seq, true
}

func compareDesc(a, b uint64) int {
	switch {
	case a > b:
		return -1
	case a < b:
		return 1
	}
	return 0
}

func notFoundConversation(id string) *Error {
	return &Error{Code: CodeNotFound, Message: "no conversation " + quote(id) + " (conversations are kept in memory and lost on restart)"}
}

// internal answers 500 for a failure the client cannot act on, and logs it.
func (s *Server) internal(w http.ResponseWriter, what string, err error) {
	s.log.Error(what, "err", err)
	writeError(w, http.StatusInternalServerError, &Error{Code: CodeInternal, Message: what + " failed; see the server log"})
}
