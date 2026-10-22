package agentapi

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/bsamanvith/agentkit/pkg/a2ui"
	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
	"github.com/bsamanvith/agentkit/pkg/auth"
	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/runner"
	"github.com/bsamanvith/agentkit/pkg/runner/pool"
	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
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
	// Runner, set instead of Harness, runs the agent somewhere else: a
	// runner component in another container (package runnerrpc). The
	// server still keeps the agent's runs; only the turn loop runs there.
	// Exactly one of Harness and Runner is set.
	Runner runner.Agent
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
	// RuntimeToolsets name toolsets added at run time ([WithToolsetAdmin])
	// whose tools the harness has too, as "<toolset>__<tool>": the harness's
	// registry resolves them in the run's tenant at each turn (package
	// mcp/servers), so a tenant that has not added one gets none of its
	// tools.
	RuntimeToolsets []string
	// Skills and Memory describe the agent's skills and long-term memory.
	Skills []string
	Memory bool
	// UI is how the agent shows UI besides text: [a2ui.UIMode] when its
	// harness has the ui_render tool ([a2ui.Tool]), whose successful calls
	// the server streams as ui events and to which runs answer with
	// UIAction; "" for text only.
	UI string
	// Isolation is the least isolation the agent's runs need; the server's
	// router ([WithRouter]) never runs them in a weaker pool. The zero value
	// is [pool.Process]: the agent may run as built (Harness or Runner).
	Isolation pool.Isolation
	// Notify is the webhook told when a task (POST /v1/tasks) of the agent
	// ends, unless the task names its own; "" for none. It needs
	// [WithWebhooks].
	Notify string
	// Version is the version of a stored agent ([AgentRegistry]) the
	// harness was built from, which its runs record; 0 for an agent given
	// to [NewAgents].
	Version int64
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
// Without it, a request authenticated by [WithAuthenticator] or [WithToken]
// acts for its principal's tenant; with it, f decides even then.
func WithTenant(f func(*http.Request) string) Option { return func(c *config) { c.tenant = f } }

// NewAgents returns the agent API for several agents. A run names its agent
// (POST /v1/runs "agent"), or gets the default one: the only agent, else the
// one named [DefaultAgent]. It fails with [ErrInvalidConfig] for no agents,
// an agent with neither or both of a harness and a runner, an invalid or duplicate name, an agent naming a toolset
// [WithToolsets] does not give, an unknown [Agent.Isolation], or an option
// with an unusable value. When it fails it closes the router of [WithRouter].
func NewAgents(agents []Agent, opts ...Option) (*Server, error) {
	cfg := newConfig(opts)
	s, err := newAgents(cfg, agents)
	if err != nil && cfg.router != nil {
		// The router was the server's to own: with no server, close it here.
		err = errors.Join(err, cfg.router.Close())
	}
	return s, err
}

func newAgents(cfg config, agents []Agent) (*Server, error) {
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
		case (a.Harness == nil) == (a.Runner == nil):
			return nil, fmt.Errorf("%w: agent %q: needs either a harness or a runner", ErrInvalidConfig, a.Name)
		case !validName.MatchString(a.Name):
			return nil, fmt.Errorf("%w: agent name %q must match %s", ErrInvalidConfig, a.Name, validName)
		case byName[a.Name] != nil:
			return nil, fmt.Errorf("%w: two agents named %q", ErrInvalidConfig, a.Name)
		case a.Isolation > pool.VM:
			return nil, fmt.Errorf("%w: agent %q: unknown isolation %s", ErrInvalidConfig, a.Name, a.Isolation)
		case a.UI != "" && a.UI != a2ui.UIMode:
			return nil, fmt.Errorf("%w: agent %q: ui %q: want %q or none", ErrInvalidConfig, a.Name, a.UI, a2ui.UIMode)
		}
		if a.Notify != "" {
			probe := Server{cfg: cfg}
			if err := probe.checkWebhook(a.Notify); err != nil {
				return nil, fmt.Errorf("%w: agent %q: notify: %w", ErrInvalidConfig, a.Name, err)
			}
		}
		for _, name := range a.Toolsets {
			if sets[name] == nil {
				return nil, fmt.Errorf("%w: agent %q uses toolset %q, which WithToolsets does not give", ErrInvalidConfig, a.Name, name)
			}
		}
		for _, name := range a.RuntimeToolsets {
			switch {
			case cfg.admin == nil:
				return nil, fmt.Errorf("%w: agent %q uses toolsets added at run time, and there is no WithToolsetAdmin", ErrInvalidConfig, a.Name)
			case !validName.MatchString(name) || sets[name] != nil:
				return nil, fmt.Errorf("%w: agent %q: runtime toolset %q must match %s and not be a toolset of WithToolsets", ErrInvalidConfig, a.Name, name, validName)
			}
		}
		a.Toolsets, a.Skills = slices.Clone(a.Toolsets), slices.Clone(a.Skills)
		a.RuntimeToolsets = slices.Clone(a.RuntimeToolsets)
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

// run is what runs the agent's turns: its harness, or its runner.
func (a *agent) run() runner.Agent {
	if a.Runner != nil {
		return a.Runner
	}
	return a.Harness
}

// resolveAgent picks the agent of a new conversation, among those the
// request may use: one it may not is no agent (404), and the default is none
// if it may not use it.
func (s *Server) resolveAgent(ctx context.Context, r *http.Request, name string) (*agent, *Error, int) {
	acc := accessOf(r)
	if name == "" {
		if s.defaultAgent == nil || !acc.agent(s.defaultAgent) {
			return nil, &Error{Code: CodeInvalidRequest, Message: "name an agent (\"agent\"); this server runs " + strings.Join(s.agentNames(acc), ", ")}, http.StatusBadRequest
		}
		return s.defaultAgent, nil, 0
	}
	a, err := s.agentFor(ctx, name, 0)
	switch {
	case err != nil && !errors.Is(err, errNoAgent):
		s.log.Error("reading a stored agent", "agent", name, "err", err)
		return nil, &Error{Code: CodeInternal, Message: "reading agent " + quote(name) + " failed; see the server log"}, http.StatusInternalServerError
	case a == nil || !acc.agent(a):
		return nil, &Error{Code: CodeNotFound, Message: "no agent " + quote(name) + "; this server runs " + strings.Join(s.agentNames(acc), ", ")}, http.StatusNotFound
	}
	return a, nil, 0
}

// agentNames names the agents acc may use, in order.
func (s *Server) agentNames(acc access) []string {
	names := []string{}
	for _, a := range s.agentOrder {
		if acc.agent(a) {
			names = append(names, a.Name)
		}
	}
	return names
}

func (s *Server) listAgents(w http.ResponseWriter, r *http.Request) {
	out := wire.AgentList{V: WireVersion, Agents: make([]wire.Agent, 0, len(s.agentOrder))}
	acc := accessOf(r)
	for _, a := range s.agentOrder {
		if !acc.agent(a) {
			continue
		}
		v, err := s.agentView(r, a)
		if err != nil {
			s.internal(w, "listing the agents' tools", err)
			return
		}
		out.Agents = append(out.Agents, v)
	}
	if s.cfg.registry != nil {
		ctx, ok := s.scope(w, r)
		if !ok {
			return
		}
		stored, err := s.storedAgents(ctx)
		if err != nil {
			s.internal(w, "listing the stored agents", err)
			return
		}
		for _, a := range stored {
			if !acc.agent(a) {
				continue
			}
			v, err := s.agentView(r, a)
			if err != nil {
				s.internal(w, "listing the agents' tools", err)
				return
			}
			out.Agents = append(out.Agents, v)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) getAgent(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	ctx, ok := s.scope(w, r)
	if !ok {
		return
	}
	a, err := s.agentFor(ctx, name, 0)
	if err != nil && !errors.Is(err, errNoAgent) {
		s.internal(w, "reading agent "+quote(name), err)
		return
	}
	if a == nil || !accessOf(r).agent(a) {
		writeError(w, http.StatusNotFound, &Error{Code: CodeNotFound, Message: "no agent " + quote(name)})
		return
	}
	v, err := s.agentView(r, a)
	if err != nil {
		s.internal(w, "listing the agent's tools", err)
		return
	}
	// The system prompt is for admins (and an open server).
	if p, ok := auth.From(r.Context()); !ok || p.HasScope(ScopeAdminAgents) {
		v.System = a.System
	}
	writeJSON(w, http.StatusOK, v)
}

// agentView describes an agent, without its system prompt.
func (s *Server) agentView(r *http.Request, a *agent) (wire.Agent, error) {
	v := wire.Agent{
		V: WireVersion, Name: a.Name, Description: a.Description, Default: a == s.defaultAgent,
		Model: a.Model, Toolsets: make([]wire.AgentToolset, 0, len(a.Toolsets)),
		Skills: append([]string{}, a.Skills...), Memory: a.Memory, UI: a.UI,
		Version: a.Version, ReadOnly: s.cfg.registry != nil && s.agents[a.Name] == a,
	}
	for _, name := range a.Toolsets {
		ts := s.toolsets[name]
		at := wire.AgentToolset{Name: name, Status: wire.ToolsetSkipped, Tools: []string{}}
		if ts.Tools != nil {
			defs, err := s.listToolset(r.Context(), name, ts.Tools)
			if err != nil {
				at.Status = wire.ToolsetUnavailable
				v.Toolsets = append(v.Toolsets, at)
				continue
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
	if len(a.RuntimeToolsets) > 0 {
		ctx, err := s.tenantContext(r)
		for _, name := range a.RuntimeToolsets {
			at := wire.AgentToolset{Name: name, Status: wire.ToolsetNotAdded, Tools: []string{}}
			if err == nil {
				at = s.runtimeAgentToolset(ctx, name)
			}
			v.Toolsets = append(v.Toolsets, at)
		}
	}
	return v, nil
}

func (s *Server) listToolsets(w http.ResponseWriter, r *http.Request) {
	out := wire.ToolsetList{V: WireVersion, Toolsets: make([]wire.Toolset, 0, len(s.cfg.toolsets))}
	acc := accessOf(r)
	var tctx context.Context // the tenant's, for the credential slots' status
	for _, ts := range s.cfg.toolsets {
		if !acc.toolset(ts.Name) {
			continue
		}
		cred := ts.Credential
		if cred.Slot != "" && s.cfg.credentials != nil {
			if tctx == nil {
				ctx, ok := s.scope(w, r)
				if !ok {
					return
				}
				tctx = ctx
			}
			cred = s.slotStatus(tctx, r, cred)
		}
		v := wire.Toolset{Name: ts.Name, Type: ts.Type, Status: wire.ToolsetSkipped, Reason: ts.Skipped, MCPPath: ts.MCPPath, Credential: cred}
		if ts.Tools != nil {
			defs, err := s.listToolset(r.Context(), ts.Name, ts.Tools)
			if err != nil {
				v.Status, v.Reason = wire.ToolsetUnavailable, unavailableReason
			} else {
				v.Status, v.ToolCount = wire.ToolsetReady, len(defs)
			}
		}
		out.Toolsets = append(out.Toolsets, v)
	}
	if s.cfg.admin != nil {
		ctx, ok := s.scope(w, r)
		if !ok {
			return
		}
		more, err := s.runtimeToolsets(ctx, acc)
		if err != nil {
			s.internal(w, "listing the toolsets added at run time", err)
			return
		}
		out.Toolsets = append(out.Toolsets, more...)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) listTools(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	ts := s.toolsets[name]
	if ts == nil || !accessOf(r).toolset(name) {
		if ts == nil && accessOf(r).toolset(name) && s.runtimeTools(w, r, name) {
			return
		}
		writeError(w, http.StatusNotFound, &Error{Code: CodeNotFound, Message: "no toolset " + quote(name)})
		return
	}
	out := wire.ToolList{V: WireVersion, Toolset: name, Status: wire.ToolsetSkipped, Tools: []wire.Tool{}}
	if ts.Tools != nil {
		defs, err := s.listToolset(r.Context(), name, ts.Tools)
		if err != nil {
			out.Status = wire.ToolsetUnavailable
			writeJSON(w, http.StatusOK, out)
			return
		}
		out.Status = wire.ToolsetReady
		for _, d := range defs {
			out.Tools = append(out.Tools, wireTool(d))
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// unavailableReason is a toolset's reason when its tools could not be
// listed; the error itself goes to the log, not to callers.
const unavailableReason = "its tools could not be listed just now (the toolsets component may be down); the server log has the error"

// listToolset lists a built toolset's tools, logging a failure: a toolset
// served by another component fails while that component is down, and the
// catalog then shows it as unavailable instead of failing the request.
func (s *Server) listToolset(ctx context.Context, name string, tools tool.Registry) ([]tool.Definition, error) {
	defs, err := tools.List(ctx)
	if err != nil && ctx.Err() == nil {
		s.log.Warn("listing a toolset's tools", "toolset", name, "err", err)
	}
	return defs, err
}

// wireTool is a tool's definition as the API shows it.
func wireTool(d tool.Definition) wire.Tool {
	kind, risk := d.Class()
	t := wire.Tool{
		Name: d.Name, Description: d.Description, InputSchema: d.InputSchema, OutputSchema: d.OutputSchema,
		Kind: string(kind), Risk: string(risk), Classified: d.Classified(),
		Interrupts: d.Interrupts, RequiresApproval: d.RequiresApproval, Tags: d.Tags,
	}
	if d.Display != (tool.Display{}) {
		t.Display = &wire.ToolDisplay{Title: d.Display.Title, Icon: d.Display.Icon, Present: d.Display.Present, Past: d.Display.Past}
	}
	return t
}

// createConversation creates an empty conversation of the agent named (or
// the default agent), for a client that wants the conversation's ID before
// its first message. A body is optional: none is {}.
func (s *Server) createConversation(w http.ResponseWriter, r *http.Request) {
	body, apiErr, code := s.readBody(w, r)
	if apiErr != nil {
		writeError(w, code, apiErr)
		return
	}
	var req wire.CreateConversationRequest
	if len(bytes.TrimSpace(body)) > 0 {
		if err := decodeStrict(body, &req); err != nil {
			writeError(w, http.StatusBadRequest, &Error{Code: CodeInvalidRequest, Message: "the body must be a JSON object {\"agent\": ...}: " + err.Error()})
			return
		}
	}
	ctx, ok := s.scope(w, r)
	if !ok {
		return
	}
	ag, apiErr, code := s.resolveAgent(ctx, r, req.Agent)
	if apiErr != nil {
		writeError(w, code, apiErr)
		return
	}
	c, err := s.store.CreateConversation(ctx, store.Conversation{ID: s.cfg.newID("conv_"), Agent: ag.Name})
	if err != nil {
		s.internal(w, "creating a conversation", err)
		return
	}
	s.log.Info("conversation created", "conversation_id", c.ID, "agent", ag.Name)
	w.Header().Set("Location", "/v1/conversations/"+url.PathEscape(c.ID))
	writeJSON(w, http.StatusCreated, Conversation{
		V: WireVersion, ID: c.ID, Agent: c.Agent, Status: StatusIdle, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt,
		Messages: []TimedMessage{},
	})
}

func (s *Server) getConversation(w http.ResponseWriter, r *http.Request) {
	ctx, ok := s.scope(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	c, err := s.store.GetConversation(ctx, id)
	switch {
	case errors.Is(err, store.ErrNotFound), errors.Is(err, store.ErrInvalid):
		writeError(w, http.StatusNotFound, notFoundConversation(id))
		return
	case err != nil:
		s.internal(w, "reading conversation "+quote(id), err)
		return
	}
	msgs, err := s.conversationMessages(ctx, id)
	if err != nil {
		s.internal(w, "reading the messages of conversation "+quote(id), err)
		return
	}
	endsOnUser := len(msgs) > 0 && msgs[len(msgs)-1].Message.Role == llm.RoleUser
	status, err := s.conversationStatus(ctx, id, func() (bool, error) { return endsOnUser, nil })
	if err != nil {
		s.internal(w, "reading the runs of conversation "+quote(id), err)
		return
	}
	writeJSON(w, http.StatusOK, Conversation{
		V: WireVersion, ID: c.ID, Agent: c.Agent, Status: status, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt,
		Messages: msgs,
	})
}

// conversationStatus is the conversation's status: running while a run of it
// goes on (or waits for a decision, or for the recovery loop), interrupted
// when its chat run stopped before it answered, else idle. endsOnUser tells
// whether its log ends on a user message; it is read only for a failed run.
func (s *Server) conversationStatus(ctx context.Context, id string, endsOnUser func() (bool, error)) (string, error) {
	run, err := s.store.Unfinished(ctx, id)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return StatusIdle, nil
	case err != nil:
		return "", err
	}
	now := s.cfg.clock.Now()
	switch {
	case held(run, now), run.Status == store.StatusSuspended:
		return StatusRunning, nil
	case run.Interrupted(now):
		return StatusInterrupted, nil
	case run.Status == store.StatusRunning: // a background run, for the recovery loop
		return StatusRunning, nil
	}
	user, err := endsOnUser()
	if err != nil || !user {
		return StatusIdle, err
	}
	return StatusInterrupted, nil
}

// lastIsUser reports whether the conversation's log ends on a user message.
func (s *Server) lastIsUser(ctx context.Context, id string) (bool, error) {
	page, err := s.store.History(ctx, id, store.Page{Order: store.Descending, Limit: 1})
	if err != nil || len(page.Entries) == 0 {
		return false, err
	}
	if page.Entries[0].Kind == store.KindInterrupt {
		// A pending turn is not in the conversation: its reply answers the
		// user message before it.
		return true, nil
	}
	msgs := page.Entries[0].Messages
	return msgs[len(msgs)-1].Role == llm.RoleUser, nil
}

// listConversations serves GET /v1/conversations: the caller's
// conversations, most recently started first. Pages follow the order of
// creation, so conversations started while a client pages land before its
// first page: it sees every older one exactly once.
func (s *Server) listConversations(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, ok := listLimit(w, q.Get("limit"))
	if !ok {
		return
	}
	agentName := q.Get("agent")
	// A stored agent's conversations stay listed when the agent is gone.
	if agentName != "" && s.agents[agentName] == nil && (s.cfg.registry == nil || !validName.MatchString(agentName)) {
		writeError(w, http.StatusNotFound, &Error{Code: CodeNotFound, Message: "no agent " + quote(agentName)})
		return
	}
	ctx, ok := s.scope(w, r)
	if !ok {
		return
	}
	page, err := s.store.ListConversations(ctx, agentName, store.Page{Cursor: q.Get("cursor"), Limit: limit, Order: store.Descending})
	switch {
	case errors.Is(err, store.ErrInvalid):
		writeError(w, http.StatusBadRequest, &Error{Code: CodeInvalidRequest, Message: "cursor is not one this server gave"})
		return
	case err != nil:
		s.internal(w, "listing conversations", err)
		return
	}
	out := wire.ConversationList{V: WireVersion, Conversations: []wire.ConversationSummary{}, NextCursor: page.Next}
	for _, c := range page.Conversations {
		status, err := s.conversationStatus(ctx, c.ID, func() (bool, error) { return s.lastIsUser(ctx, c.ID) })
		if err != nil {
			s.internal(w, "reading the runs of conversation "+quote(c.ID), err)
			return
		}
		out.Conversations = append(out.Conversations, wire.ConversationSummary{
			ID: c.ID, Agent: c.Agent, Title: c.Title, Status: status, MessageCount: int(c.Messages),
			CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// deleteConversation serves DELETE /v1/conversations/{id}: the conversation
// and its runs are gone (204). One with a run going on is 409
// conversation_busy.
func (s *Server) deleteConversation(w http.ResponseWriter, r *http.Request) {
	ctx, ok := s.scope(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	err := s.store.DeleteConversation(ctx, id)
	switch {
	case errors.Is(err, store.ErrNotFound), errors.Is(err, store.ErrInvalid):
		writeError(w, http.StatusNotFound, notFoundConversation(id))
		return
	case errors.Is(err, store.ErrLeased):
		writeError(w, http.StatusConflict, &Error{Code: CodeBusy, Message: "a run of this conversation is going on; stop it first", Retryable: true})
		return
	case err != nil:
		s.internal(w, "deleting conversation "+quote(id), err)
		return
	}
	t, _ := tenancy.From(ctx)
	s.mu.Lock()
	for k, h := range s.hubs {
		if k.tenant == t.ID() && h.convID == id && h.isDone() {
			delete(s.hubs, k)
		}
	}
	s.mu.Unlock()
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) tenantOf(r *http.Request) string {
	if s.cfg.tenant != nil {
		return s.cfg.tenant(r)
	}
	if p, ok := auth.From(r.Context()); ok {
		return p.Tenant.ID()
	}
	return ""
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

func notFoundConversation(id string) *Error {
	return &Error{Code: CodeNotFound, Message: "no conversation " + quote(id)}
}

// internal answers 500 for a failure the client cannot act on, and logs it.
func (s *Server) internal(w http.ResponseWriter, what string, err error) {
	s.log.Error(what, "err", err)
	writeError(w, http.StatusInternalServerError, &Error{Code: CodeInternal, Message: what + " failed; see the server log"})
}
