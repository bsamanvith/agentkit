package agenttools_test

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"strings"

	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/agenttools"
	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/store/scoped"
	"github.com/bsamanvith/agentkit/pkg/tool"
	"github.com/bsamanvith/agentkit/pkg/toolset"
)

// shout answers with the user's last message in capitals.
type shout struct{}

func (shout) Generate(_ context.Context, req llm.Request) iter.Seq2[llm.Event, error] {
	text := strings.ToUpper(req.Messages[len(req.Messages)-1].Text())
	return func(yield func(llm.Event, error) bool) {
		_ = yield(llm.Event{Kind: llm.EventTextDelta, Text: text}, nil) &&
			yield(llm.Event{Kind: llm.EventBlockDone, Block: llm.Text{Text: text}}, nil) &&
			yield(llm.Event{Kind: llm.EventMessageDone, Response: &llm.Response{
				Message: llm.Assistant(llm.Text{Text: text}), Stop: llm.StopReason{Kind: llm.StopEndTurn},
			}}, nil)
	}
}

// The agentkit toolset over an agent API in the same process. agentkit
// serve serves it over MCP, at /mcp/<name>; here its tools are called
// directly.
func Example() {
	ctx := context.Background()
	none, _ := tool.NewRegistry(nil)
	h, _ := harness.New(shout{}, none)
	api, _ := agentapi.New(h, agentapi.WithStore(scoped.Memory(nil)))

	f, _ := agenttools.New(agenttools.WithHandler(api.Handler()))
	defer f.Close()
	catalog, _ := toolset.NewCatalog(f)
	sets, _, err := toolset.Build(ctx, catalog, []toolset.Spec{{Name: "agentkit", Type: agenttools.Type}})
	if err != nil {
		fmt.Println(err)
		return
	}
	out, err := sets["agentkit"].Invoke(ctx, "agent_run", json.RawMessage(`{"message": "hello"}`))
	if err != nil {
		fmt.Println(err)
		return
	}
	var run agenttools.Run
	_ = json.Unmarshal(out, &run)
	fmt.Println(run.Status, run.Answer, run.Agent)
	// Output: done HELLO default
}
