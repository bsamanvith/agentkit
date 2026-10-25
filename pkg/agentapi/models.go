package agentapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
	"github.com/bsamanvith/agentkit/pkg/llm/modelset"
	"github.com/bsamanvith/agentkit/pkg/store"
)

// WithModels lists the model providers the server builds and the models it
// knows of each, for GET /v1/models and for the choices of each agent's
// models ([wire.AgentModels.Choices]). Names are "<provider>/<id>". Without
// it, GET /v1/models lists no providers and an agent's choices are the
// models it allows by name.
func WithModels(providers ...wire.ModelProvider) Option {
	return func(c *config) { c.models = slices.Clone(providers) }
}

// models is the agent's set of models: [Agent.Models], or its default
// model alone; nil for an agent that names no model (a test's harness).
func (a *Agent) models() *modelset.Set {
	if a.Models != nil {
		return a.Models
	}
	if a.Model.ID == "" {
		return nil
	}
	set, err := modelset.New(defaultModelName(a.Model), modelset.Spec{}, nil)
	if err != nil {
		return nil
	}
	return set
}

// defaultModelName names the model m as "<provider>/<id>".
func defaultModelName(m wire.Model) string {
	if m.Provider == "" {
		return m.ID
	}
	return m.Provider + "/" + m.ID
}

// checkModels checks an agent's models when the server is made.
func checkModels(a Agent) error {
	if a.Models == nil {
		return nil
	}
	if c, err := modelset.Canonical(defaultModelName(a.Model)); err != nil || c != a.Models.Default() {
		return fmt.Errorf("agent %q: its models' default %q is not its model %q", a.Name, a.Models.Default(), defaultModelName(a.Model))
	}
	if !a.Models.Single() && a.ForModel == nil {
		return fmt.Errorf("agent %q allows more than its default model, and has no ForModel", a.Name)
	}
	return nil
}

// modelsView is the agent's models as GET /v1/agents shows them.
func (s *Server) modelsView(a *agent) wire.AgentModels {
	set := a.models()
	if set == nil {
		return wire.AgentModels{Allow: []string{}, Choices: []string{}}
	}
	var known []string
	for _, p := range s.cfg.models {
		known = append(known, p.Models...)
	}
	return wire.AgentModels{
		Default: set.Default(), Allow: set.Names(), AllowPatterns: set.Patterns(),
		Choices: set.Matching(known),
	}
}

// listModels serves GET /v1/models.
func (s *Server) listModels(w http.ResponseWriter, _ *http.Request) {
	out := wire.ModelList{V: WireVersion, Default: modelset.DefaultProvider, Providers: []wire.ModelProvider{}}
	for _, p := range s.cfg.models {
		out.Providers = append(out.Providers, wire.ModelProvider{Name: p.Name, Models: append([]string{}, p.Models...)})
	}
	writeJSON(w, http.StatusOK, out)
}

// chooseModel picks the model of a new run of agent a in conversation
// convID ("" for a new conversation) when the request asks for model
// asked: that one if the agent allows it, else the conversation's last
// model if the agent still allows it, else the agent's default. It refuses
// a model the agent does not allow (400) and one of another provider than
// the conversation's last run (409). The name is canonical; "" for an
// agent that names no model.
func (s *Server) chooseModel(ctx context.Context, a *agent, convID, asked string) (string, *Error, int, error) {
	set := a.models()
	if set == nil {
		if asked != "" {
			return "", &Error{Code: CodeModelNotAllowed, Message: "agent " + quote(a.Name) + " names no model, so a run cannot pick one"}, http.StatusBadRequest, nil
		}
		return "", nil, 0, nil
	}
	model, err := set.Resolve(asked)
	if err != nil {
		return "", &Error{Code: CodeModelNotAllowed, Message: notAllowed(a.Name, set, err)}, http.StatusBadRequest, nil
	}
	if convID == "" {
		return model, nil, 0, nil
	}
	prev, err := s.lastModel(ctx, convID)
	if err != nil {
		return "", nil, 0, err
	}
	switch {
	case prev == "":
	case asked == "" && set.Allows(prev):
		model = prev
	case !modelset.SameProvider(prev, model):
		return "", &Error{Code: CodeModelProviderMismatch, Message: "the conversation ran on " + quote(prev) + ", of another provider than " + quote(model) + "; start a new conversation to use it"}, http.StatusConflict, nil
	}
	return model, nil, 0, nil
}

// notAllowed says which models agent name allows, after why err refused
// one.
func notAllowed(name string, set *modelset.Set, err error) string {
	var b strings.Builder
	b.WriteString(err.Error())
	b.WriteString("; agent " + quote(name) + " allows " + quote(strings.Join(set.Names(), ", ")))
	if ps := set.Patterns(); len(ps) > 0 {
		b.WriteString(" and the names matching " + quote(strings.Join(ps, ", ")))
	}
	return b.String()
}

// lastModel is the model of the conversation's latest run: "" when it has
// none, or its latest run recorded none.
func (s *Server) lastModel(ctx context.Context, convID string) (string, error) {
	page, err := s.store.List(ctx, convID, store.Page{Limit: 1, Order: store.Descending})
	if err != nil || len(page.Runs) == 0 {
		return "", err
	}
	return page.Runs[0].Model, nil
}

// errModelBuild means an agent's ForModel failed.
var errModelBuild = errors.New("building the agent for its model")

// withModel is agent a as it runs on model (canonical): a itself for its
// default (or ""), else what its ForModel builds.
func (s *Server) withModel(ctx context.Context, a *agent, model string) (*agent, error) {
	set := a.models()
	if model == "" || set == nil || model == set.Default() {
		return a, nil
	}
	if a.ForModel == nil {
		return nil, fmt.Errorf("%w: agent %q runs only %q, not %q", errModelBuild, a.Name, set.Default(), model)
	}
	m, err := a.ForModel(ctx, model)
	if err != nil {
		return nil, fmt.Errorf("%w %q: %w", errModelBuild, model, err)
	}
	if (m.Harness == nil) == (m.Runner == nil) {
		return nil, fmt.Errorf("%w %q: agent %q came back with a harness and a runner or neither", errModelBuild, model, a.Name)
	}
	m.Name, m.Version, m.Models, m.ForModel = a.Name, a.Version, a.Models, a.ForModel
	return &agent{Agent: m}, nil
}
