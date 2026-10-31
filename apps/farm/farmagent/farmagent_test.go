package farmagent_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/bsamanvith/agentkit/apps/farm/farm"
	"github.com/bsamanvith/agentkit/apps/farm/farm/farmtest"
	"github.com/bsamanvith/agentkit/apps/farm/farmagent"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/llm/llmtest"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

func service(t *testing.T) (*farm.Service, farm.Farm) {
	t.Helper()
	store := &farmtest.Memory{}
	f := farmtest.Sample("farm_1", time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC))
	if err := store.Create(context.Background(), f); err != nil {
		t.Fatal(err)
	}
	svc, err := farm.NewService(store, farm.NewVerifier())
	if err != nil {
		t.Fatal(err)
	}
	return svc, f
}

func registry(t *testing.T, svc *farm.Service) tool.Registry {
	t.Helper()
	tools, err := farmagent.Tools(svc)
	if err != nil {
		t.Fatal(err)
	}
	reg, err := tool.NewRegistry(tools)
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

func invoke[T any](t *testing.T, reg tool.Registry, name, input string) T {
	t.Helper()
	raw, err := reg.Invoke(context.Background(), name, json.RawMessage(input))
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	var out T
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("%s: %v in %s", name, err, raw)
	}
	return out
}

func TestToolsAreReadOnly(t *testing.T) {
	t.Parallel()
	svc, _ := service(t)
	defs, err := registry(t, svc).List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, d := range defs {
		names = append(names, d.Name)
		if d.Kind != tool.KindRead {
			t.Errorf("%s: kind %q, want read", d.Name, d.Kind)
		}
	}
	if diff := cmp.Diff([]string{"convert_area", "get_farm", "list_farms", "verify_boundary"}, names); diff != "" {
		t.Errorf("tools (-want +got):\n%s", diff)
	}
}

func TestListAndGetFarm(t *testing.T) {
	t.Parallel()
	svc, f := service(t)
	reg := registry(t, svc)
	list := invoke[struct{ Farms []farmagent.Summary }](t, reg, "list_farms", `{}`)
	if len(list.Farms) != 1 || list.Farms[0].ID != f.ID || list.Farms[0].Corners != 4 || list.Farms[0].Checks != nil {
		t.Errorf("list_farms = %+v", list)
	}
	got := invoke[farmagent.Summary](t, reg, "get_farm", `{"id":"farm_1"}`)
	if got.Name != f.Name || got.Status != farm.StatusVerified || len(got.Checks) != 1 {
		t.Errorf("get_farm = %+v", got)
	}
	// The farmer's land and water, and the saved plan, come with it.
	if got.Profile == nil || got.Profile.Soil != f.Profile.Soil || len(got.Plan) != 1 || got.Plan[0].Crop != "groundnut" || got.PlanStatus == "" {
		t.Errorf("get_farm profile %+v, plan %+v (%s)", got.Profile, got.Plan, got.PlanStatus)
	}
	if _, err := reg.Invoke(context.Background(), "get_farm", json.RawMessage(`{"id":"nope"}`)); !errors.Is(err, tool.ErrToolFailed) {
		t.Errorf("get_farm of a missing farm: %v, want a tool failure", err)
	}
}

func TestVerifyBoundaryAndConvert(t *testing.T) {
	t.Parallel()
	svc, f := service(t)
	reg := registry(t, svc)
	corners, err := json.Marshal(f.Boundary.Ring())
	if err != nil {
		t.Fatal(err)
	}
	rep := invoke[farm.Report](t, reg, "verify_boundary", `{"corners":`+string(corners)+`,"declared_value":1,"declared_unit":"acre"}`)
	if rep.Measures == nil || rep.Measures.Acres < 0.9 || rep.Measures.Acres > 1.1 {
		t.Errorf("verify_boundary = %+v, want about an acre", rep)
	}
	// The saved farm is where this boundary is: an overlap.
	if !slices.ContainsFunc(rep.Checks, func(c farm.Check) bool { return c.ID == farm.CheckOverlap }) {
		t.Errorf("verify_boundary checks %+v, want an overlap with the saved farm", rep.Checks)
	}
	conv := invoke[struct {
		Value     float64
		AcresText string `json:"acres_text"`
	}](t, reg, "convert_area", `{"value":2.5,"from":"acre","to":"gunta"}`)
	if conv.Value != 100 || conv.AcresText != "2 acres 20 guntas" {
		t.Errorf("convert_area = %+v, want 100 guntas", conv)
	}
	if _, err := reg.Invoke(context.Background(), "convert_area", json.RawMessage(`{"value":1,"from":"bigha","to":"acre"}`)); !errors.Is(err, tool.ErrToolFailed) {
		t.Errorf("convert_area from bigha: %v, want a tool failure", err)
	}
}

func TestAssistantAnswersFromTheTools(t *testing.T) {
	t.Parallel()
	svc, _ := service(t)
	model := llmtest.NewFake(t,
		llmtest.ToolCalls(llm.ToolUse{ID: "c1", Name: "list_farms", Input: json.RawMessage(`{}`)}),
		llmtest.Text("మీ తూర్పు పొలం 1 ఎకరం."),
		llmtest.Text("Yes."),
	)
	a, err := farmagent.NewAssistant(model, svc)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	r, err := a.Ask(ctx, "", "నా పొలం ఎంత?")
	if err != nil {
		t.Fatal(err)
	}
	if r.Conversation == "" || r.Text != "మీ తూర్పు పొలం 1 ఎకరం." || !cmp.Equal(r.Tools, []string{"list_farms"}) {
		t.Errorf("Ask = %+v", r)
	}
	// The tool's result went back to the model with the farm in it.
	reqs := model.Requests()
	if last := reqs[1].Messages[len(reqs[1].Messages)-1]; !strings.Contains(string(mustJSON(t, last)), "farm_1") {
		t.Errorf("the model was not sent the farm: %s", mustJSON(t, last))
	}
	if !strings.Contains(reqs[0].System, "Reply in the language") {
		t.Errorf("system prompt not sent: %q", reqs[0].System)
	}

	// A follow-up continues the conversation.
	r2, err := a.Ask(ctx, r.Conversation, "Is it verified?")
	if err != nil {
		t.Fatal(err)
	}
	if len(r2.Tools) != 0 || len(model.Requests()[2].Messages) != 5 {
		t.Errorf("follow-up: %+v with %d messages sent, want the 4 before and the new one", r2, len(model.Requests()[2].Messages))
	}
	if _, err := a.Ask(ctx, "conv_forged", "hi"); !errors.Is(err, farmagent.ErrUnknownConversation) {
		t.Errorf("Ask in an unknown conversation: %v", err)
	}
	if _, err := a.Ask(ctx, "", "  "); !errors.Is(err, farm.ErrInvalid) {
		t.Errorf("Ask with no message: %v", err)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
