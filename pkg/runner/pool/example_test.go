package pool_test

import (
	"fmt"

	"github.com/bsamanvith/agentkit/pkg/runner"
	"github.com/bsamanvith/agentkit/pkg/runner/pool"
)

// Chats and background runs get pools of their own, both in process; an
// agent that needs a VM has none here, so its runs do not start.
func ExampleRouter() {
	rt, err := pool.NewRouter(
		[]pool.Pool{pool.NewProcess("default"), pool.NewProcess("chat"), pool.NewProcess("background")},
		[]pool.Rule{{Pool: "chat", When: pool.Chats()}, {Pool: "background", When: pool.Background()}},
		"default")
	if err != nil {
		panic(err)
	}
	for _, r := range []pool.Request{
		{Placement: runner.Placement{ConversationID: "c1", Interactive: true}, Agent: "helper"},
		{Placement: runner.Placement{ConversationID: "c2"}, Agent: "helper"},
		{Placement: runner.Placement{ConversationID: "c3"}, Agent: "untrusted", Isolation: pool.VM},
	} {
		p, err := rt.Route(r)
		if err != nil {
			fmt.Println(r.ConversationID, "→", err)
			continue
		}
		fmt.Println(r.ConversationID, "→", p.Name(), p.Isolation())
	}
	// Output:
	// c1 → chat process
	// c2 → background process
	// c3 → no pool may run this agent: agent "untrusted" needs vm isolation
}
