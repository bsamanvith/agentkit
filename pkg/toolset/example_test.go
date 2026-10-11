package toolset_test

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/bsamanvith/agentkit/pkg/toolset"
)

func ExampleBuild() {
	catalog, err := toolset.NewCatalog(toolset.Demo())
	if err != nil {
		panic(err)
	}

	var specs []toolset.Spec
	config := `[
		{"name": "demo",       "type": "demo", "settings": {"echo_prefix": "demo: "}},
		{"name": "demo-maths", "type": "demo", "include": ["calculate"]},
		{"name": "later",      "type": "missing-connector", "optional": true}
	]`
	if err := json.Unmarshal([]byte(config), &specs); err != nil {
		panic(err)
	}

	// Unknown types are always an error; drop the last spec to build the rest.
	_, _, err = toolset.Build(context.Background(), catalog, specs)
	fmt.Println(err)

	regs, problems, err := toolset.Build(context.Background(), catalog, specs[:2])
	if err != nil {
		panic(err)
	}
	fmt.Println(len(regs), len(problems))

	out, _ := regs["demo"].Invoke(context.Background(), "echo", json.RawMessage(`{"text":"hello"}`))
	fmt.Println(string(out))
	defs, _ := regs["demo-maths"].List(context.Background())
	fmt.Println(len(defs), defs[0].Name)
	// Output:
	// toolset "later": unknown type "missing-connector" (compiled-in types: demo)
	// 2 0
	// {"text":"demo: hello"}
	// 1 calculate
}
