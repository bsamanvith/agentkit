package farmagent_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/bsamanvith/agentkit/apps/farm/farm"
	"github.com/bsamanvith/agentkit/apps/farm/farmagent"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

func groupRegistry(t *testing.T) (tool.Registry, *farm.Service) {
	t.Helper()
	svc, _ := service(t)
	tools, err := farmagent.GroupTools(plannerFor(t, svc))
	if err != nil {
		t.Fatal(err)
	}
	reg, err := tool.NewRegistry(tools)
	if err != nil {
		t.Fatal(err)
	}
	return reg, svc
}

func TestGroupTools(t *testing.T) {
	t.Parallel()
	reg, svc := groupRegistry(t)
	defs, err := reg.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range defs {
		want := tool.KindRead
		if d.Name == farmagent.SaveGroupTool || d.Name == farmagent.SaveDistributionTool {
			want = tool.KindWrite
		}
		if d.Kind != want {
			t.Errorf("%s: kind %s, want %s", d.Name, d.Kind, want)
		}
	}
	type group struct {
		ID    string
		Plots []struct{ ID, Name string }
	}
	g := invoke[group](t, reg, farmagent.SaveGroupTool, `{"name":"Home farm","plots":["farm_1"]}`)
	if g.ID == "" || len(g.Plots) != 1 || g.Plots[0].Name == "" {
		t.Fatalf("save_group = %+v", g)
	}
	list := invoke[struct{ Groups []group }](t, reg, farmagent.ListGroupsTool, `{}`)
	if len(list.Groups) != 1 {
		t.Errorf("list_groups = %+v", list)
	}
	d := `{"plants":[{"id":"trees","name":"Mango border","crops":["mango"],"place":"border"},{"id":"veg","name":"Vegetables","crops":["tomato"]}]}`
	type out struct {
		Status string
		Plants []struct {
			Plant  string
			AreaM2 float64 `json:"area_m2"`
		}
		Plots []struct {
			Pieces []struct {
				Plant string
				Trees int
			}
		}
		Effects []struct{ ID, Severity, Message string }
		Raw     json.RawMessage
		Saved   bool
	}
	laid := invoke[out](t, reg, farmagent.DistributePlantsTool, `{"group":"`+g.ID+`","distribution":`+d+`}`)
	if len(laid.Plants) != 2 || len(laid.Plots) != 1 || len(laid.Plots[0].Pieces) != 2 || laid.Plots[0].Pieces[0].Trees == 0 || len(laid.Effects) == 0 || laid.Saved {
		t.Fatalf("distribute_plants = %+v", laid)
	}
	if got, _ := svc.Group(context.Background(), g.ID); got.Distribution != nil {
		t.Error("distribute_plants saved")
	}
	saved := invoke[out](t, reg, farmagent.SaveDistributionTool, `{"group":"`+g.ID+`","distribution":`+string(laid.Raw)+`}`)
	if !saved.Saved {
		t.Errorf("save_distribution = %+v", saved)
	}
	again := invoke[out](t, reg, farmagent.DistributePlantsTool, `{"group":"`+g.ID+`"}`)
	if !slices.EqualFunc(again.Plants, laid.Plants, func(a, b struct {
		Plant  string
		AreaM2 float64 `json:"area_m2"`
	},
	) bool {
		return a == b
	}) {
		t.Errorf("the saved distribution laid out differently: %+v", again.Plants)
	}
	// A bad distribution is the model's to fix.
	_, err = reg.Invoke(context.Background(), farmagent.DistributePlantsTool, json.RawMessage(`{"group":"`+g.ID+`","distribution":{"plants":[{"id":"x","crops":["mangoes"]}]}}`))
	var te *tool.Error
	if !errors.As(err, &te) || !errors.Is(te.Kind, tool.ErrInvalidInput) {
		t.Errorf("unknown crop: %v, want invalid input", err)
	}
}
