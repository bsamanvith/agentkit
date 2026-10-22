package client_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"

	"github.com/bsamanvith/agentkit/pkg/a2a"
	"github.com/bsamanvith/agentkit/pkg/a2a/client"
)

func ExampleConnect() {
	// A remote agent that answers every message at once.
	var ts *httptest.Server
	ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode(a2a.AgentCard{
				Name: "greeter", Description: "Says hello.", Version: "1.0.0",
				SupportedInterfaces: []a2a.AgentInterface{{URL: ts.URL + "/a2a", ProtocolBinding: a2a.BindingJSONRPC, ProtocolVersion: a2a.ProtocolVersion}},
			})
			return
		}
		task := a2a.Task{
			ID: "task-1", Status: a2a.TaskStatus{State: a2a.StateCompleted},
			Artifacts: []a2a.Artifact{{ArtifactID: "answer", Parts: []a2a.Part{a2a.TextPart("Hello!")}}},
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"task": task}})
	}))
	defer ts.Close()

	ctx := context.Background()
	c, err := client.Connect(ctx, ts.URL+"/.well-known/agent-card.json", client.Config{
		Header: http.Header{"Authorization": {"Bearer example-token"}},
	})
	if err != nil {
		fmt.Println(err)
		return
	}
	task, err := c.Start(ctx, client.UserText("Hi"))
	if err == nil && !client.Settled(task.Status.State) {
		task, err = c.Wait(ctx, task.ID)
	}
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(c.Card().Name, task.Status.State, client.Answer(task))
	// Output: greeter TASK_STATE_COMPLETED Hello!
}
