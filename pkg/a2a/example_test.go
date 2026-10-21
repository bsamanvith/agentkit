package a2a_test

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/bsamanvith/agentkit/pkg/a2a"
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

func ExampleNew() {
	tools, _ := tool.NewRegistry(nil)
	h, _ := harness.New(shout{}, tools)
	api, _ := agentapi.New(h, agentapi.WithStore(scoped.Memory(nil)))
	srv, _ := a2a.New(api.Handler())
	defer func() { _ = srv.Shutdown(context.Background()) }()

	// agentkit serve mounts both: the agent API at /v1/, A2A at /a2a/ and
	// the well-known card.
	mux := http.NewServeMux()
	mux.Handle("/v1/", api.Handler())
	mux.Handle(a2a.PathPrefix, srv.Handler())
	mux.Handle(a2a.WellKnownPath, srv.Handler())
	ts := httptest.NewServer(mux)
	defer ts.Close()

	body := `{"jsonrpc":"2.0","id":1,"method":"SendMessage","params":{"message":` +
		`{"messageId":"m1","role":"ROLE_USER","parts":[{"text":"hello"}]}}}`
	req, _ := http.NewRequest(http.MethodPost, ts.URL+a2a.PathPrefix+agentapi.DefaultAgent, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("A2A-Version", a2a.ProtocolVersion)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Println(err)
		return
	}
	defer resp.Body.Close()
	var out struct {
		Result a2a.SendMessageResponse `json:"result"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	task := out.Result.Task
	fmt.Println(task.Status.State, *task.Artifacts[0].Parts[0].Text)
	// Output: TASK_STATE_COMPLETED HELLO
}
