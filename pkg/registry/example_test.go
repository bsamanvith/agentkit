package registry_test

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"

	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/registry"
	"github.com/bsamanvith/agentkit/pkg/store/memory"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

// silent is a model that is never called here.
type silent struct{}

func (silent) Generate(context.Context, llm.Request) iter.Seq2[llm.Event, error] {
	return func(func(llm.Event, error) bool) {}
}

func Example() {
	// The builder turns a definition into an agent; serve's checks its
	// toolsets and model, and refuses field by field.
	build := func(_ context.Context, name string, raw json.RawMessage) (agentapi.Agent, error) {
		var d struct {
			System string `json:"system"`
		}
		if err := json.Unmarshal(raw, &d); err != nil || d.System == "" {
			return agentapi.Agent{}, &agentapi.DefinitionError{Fields: []wire.FieldError{{Field: "system", Message: "required"}}}
		}
		tools, _ := tool.NewRegistry(nil)
		h, err := harness.New(silent{}, tools, harness.WithSystem(d.System))
		return agentapi.Agent{Name: name, System: d.System, Harness: h}, err
	}
	reg, _ := registry.New(memory.New(), build, registry.WithReadOnly("default"))
	tenant, _ := tenancy.NewTenant("acme")
	ctx := tenancy.With(context.Background(), tenant)

	_, _ = reg.Save(ctx, "admin", "writer", wire.SaveAgentRequest{Definition: json.RawMessage(`{"system": "Write v1."}`), Publish: true})
	_, _ = reg.Save(ctx, "admin", "writer", wire.SaveAgentRequest{Definition: json.RawMessage(`{"system": "Write v2."}`)})
	a, _ := reg.Agent(ctx, "writer", 0) // the published version
	fmt.Println(a.Version, a.System)

	_, _ = reg.Publish(ctx, "writer", 2)
	a, _ = reg.Agent(ctx, "writer", 0)
	fmt.Println(a.Version, a.System)

	_, err := reg.Save(ctx, "admin", "writer", wire.SaveAgentRequest{Definition: json.RawMessage(`{}`)})
	fmt.Println(err)
	_, err = reg.Save(ctx, "admin", "default", wire.SaveAgentRequest{Definition: json.RawMessage(`{"system": "x"}`)})
	fmt.Println(err)
	// Output:
	// 1 Write v1.
	// 2 Write v2.
	// invalid agent definition: system: required
	// the agent is read-only: agent "default" comes from the server's config file; change it there
}
