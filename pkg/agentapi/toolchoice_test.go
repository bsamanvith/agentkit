package agentapi_test

import (
	"encoding/json"
	"net/http"
	"slices"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/llm"
)

// choiceRig serves agent "solo" with toolsets "notes" (the note tool) and
// "looks" (the look tool).
func choiceRig(t *testing.T, m *model) *rig {
	t.Helper()
	of := func(name string) string {
		switch name {
		case "note":
			return "notes"
		case "look":
			return "looks"
		}
		return ""
	}
	reg := policyRegistry(t)
	agents := []agentapi.Agent{{
		Name: "solo", Toolsets: []string{"notes", "looks"},
		Harness: agentHarness(t, m, reg, harness.WithToolsets(of)),
	}}
	var sets []agentapi.Toolset
	for _, name := range agents[0].Toolsets {
		sets = append(sets, agentapi.Toolset{Name: name, Type: "demo", Tools: reg, Credential: agentapi.Credential{Status: wire.CredentialNotNeeded}})
	}
	return newRig(t, m, rigConfig{agents: agents, api: []agentapi.Option{agentapi.WithToolsets(sets...)}})
}

func toolNamesOf(req llm.Request) []string {
	var out []string
	for _, d := range req.Tools {
		out = append(out, d.Name)
	}
	slices.Sort(out)
	return out
}

// A run's tools reach the model: the toolsets it chose, directly or behind
// tool search.
func TestRunToolChoice(t *testing.T) {
	t.Parallel()
	m := echo(t)
	r := choiceRig(t, m)
	r.open(r.post(t.Context(), `{"message":"go","tools":{"toolsets":["looks"]}}`, "")).all()
	if got := toolNamesOf(m.requests()[0]); !slices.Equal(got, []string{"look"}) {
		t.Errorf("tools with toolsets [looks] = %v, want [look]", got)
	}
	r.open(r.post(t.Context(), `{"message":"go","tools":{"search":"search"}}`, "")).all()
	if got := toolNamesOf(m.requests()[1]); !slices.Equal(got, []string{harness.SearchTool}) {
		t.Errorf("tools with search = %v, want only %s", got, harness.SearchTool)
	}
	r.open(r.post(t.Context(), `{"message":"go"}`, "")).all()
	if got := toolNamesOf(m.requests()[2]); !slices.Equal(got, []string{"look", "note"}) {
		t.Errorf("tools without a choice = %v, want both", got)
	}
}

// An unknown toolset or mode is 400, and so is a resume that sets tools.
func TestRunToolChoiceValidation(t *testing.T) {
	t.Parallel()
	r := choiceRig(t, echo(t))
	first := r.run("hi", "")
	for name, body := range map[string]string{
		"unknown toolset": `{"message":"go","tools":{"toolsets":["mail"]}}`,
		"unknown mode":    `{"message":"go","tools":{"search":"lazy"}}`,
		"unknown field":   `{"message":"go","tools":{"toolset":["looks"]}}`,
		"resume with one": `{"message":"","conversation_id":"` + first.conversationID() + `","tools":{"search":"direct"}}`,
	} {
		resp := r.post(t.Context(), body, "")
		var e agentapi.ErrorResponse
		_ = json.NewDecoder(resp.Body).Decode(&e)
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest || e.Error == nil || e.Error.Code != agentapi.CodeInvalidRequest {
			t.Errorf("%s: %d %+v, want 400 invalid_request", name, resp.StatusCode, e.Error)
		}
	}
}
