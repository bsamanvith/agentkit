package agui_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/agui"
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

func ExampleNew() {
	tools, _ := tool.NewRegistry(nil)
	h, _ := harness.New(shout{}, tools)
	api, _ := agentapi.New(h, agentapi.WithStore(scoped.Memory(nil)))
	srv, _ := agui.New(api.Handler())
	defer func() { _ = srv.Shutdown(context.Background()) }()

	// agentkit serve mounts both: the agent API at /v1/, AG-UI at /agui/.
	mux := http.NewServeMux()
	mux.Handle("/v1/", api.Handler())
	mux.Handle(agui.PathPrefix, srv.Handler())
	ts := httptest.NewServer(mux)
	defer ts.Close()

	body := `{"threadId":"thread-1","runId":"run-1","messages":[{"id":"m1","role":"user","content":"hello"}]}`
	resp, err := http.Post(ts.URL+agui.PathPrefix+agentapi.DefaultAgent, "application/json", strings.NewReader(body))
	if err != nil {
		fmt.Println(err)
		return
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		data, ok := strings.CutPrefix(sc.Text(), "data: ")
		if !ok {
			continue
		}
		var e agui.Event
		_ = json.Unmarshal([]byte(data), &e)
		switch {
		case e.Delta != nil:
			fmt.Println(e.Type, *e.Delta)
		case e.Outcome != nil:
			fmt.Println(e.Type, e.Outcome.Type, string(e.Result))
		default:
			fmt.Println(e.Type)
		}
	}
	// Output:
	// RUN_STARTED
	// TEXT_MESSAGE_START
	// TEXT_MESSAGE_CONTENT HELLO
	// TEXT_MESSAGE_END
	// RUN_FINISHED success "HELLO"
}
