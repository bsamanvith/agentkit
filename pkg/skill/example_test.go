package skill_test

import (
	"context"
	"fmt"
	"os"

	"github.com/bsamanvith/agentkit/pkg/skill"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

func Example() {
	// Load every skill folder under testdata/skills.
	skills, err := skill.Load(os.DirFS("testdata"), "skills")
	if err != nil {
		panic(err)
	}
	cat, err := skill.NewCatalog(skills...)
	if err != nil {
		panic(err)
	}
	// The catalog goes in the system prompt; the toolset reads the rest.
	system := "You are a helpful assistant.\n\n" + cat.SystemText()
	tools, err := cat.Toolset()
	if err != nil {
		panic(err)
	}
	reg, err := tool.NewRegistry(tools)
	if err != nil {
		panic(err)
	}
	_ = system // harness.New(model, reg, harness.WithSystem(system))

	out, err := reg.Invoke(context.Background(), skill.ReadTool, []byte(`{"name":"sql-review","path":"checklist.md"}`))
	if err != nil {
		panic(err)
	}
	fmt.Println(cat.Names())
	fmt.Println(string(out))
	// Output:
	// [release-notes sql-review]
	// {"name":"sql-review","path":"checklist.md","content":"- Does any statement take a lock on a large table?\n- Can a column or table drop lose data?\n- Is there a way back (a down migration, or a plan)?\n"}
}
