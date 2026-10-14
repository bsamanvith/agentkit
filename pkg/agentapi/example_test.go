package agentapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/store/scoped"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

// shout is a model that answers with the user's last message in capitals.
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

func Example() {
	tools, _ := tool.NewRegistry(nil)
	h, _ := harness.New(shout{}, tools)
	srv, _ := agentapi.New(h, agentapi.WithStore(scoped.Memory(nil)))
	ts := httptest.NewServer(srv.Handler()) // agentkit serve mounts it at /v1/
	defer ts.Close()

	// Ask for the final run as JSON; without the Accept header the response is
	// a stream of events.
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/runs", strings.NewReader(`{"message": "hello"}`))
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Println(err)
		return
	}
	defer resp.Body.Close()
	var run agentapi.Run
	_ = json.NewDecoder(resp.Body).Decode(&run)
	fmt.Println(run.Status, run.Answer, run.Turns)
	// Output: done HELLO 1
}
