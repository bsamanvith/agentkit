package a2astep_test

import (
	"context"
	"fmt"

	"github.com/bsamanvith/agentkit/pkg/a2a"
	"github.com/bsamanvith/agentkit/pkg/a2a/a2astep"
	"github.com/bsamanvith/agentkit/pkg/flow"
)

// translator is a remote agent that has done its task already.
type translator struct{}

func (translator) Start(context.Context, a2a.Message) (a2a.Task, error) {
	return a2a.Task{
		ID: "task-1", Status: a2a.TaskStatus{State: a2a.StateCompleted},
		Artifacts: []a2a.Artifact{{ArtifactID: "answer", Parts: []a2a.Part{a2a.TextPart("Bonjour")}}},
	}, nil
}

func (t translator) GetTask(ctx context.Context, _ string) (a2a.Task, error) {
	return t.Start(ctx, a2a.Message{})
}

func (t translator) Wait(ctx context.Context, id string) (a2a.Task, error) { return t.GetTask(ctx, id) }

func ExampleNew() {
	// In a program, the agent is a *client.Client (client.Connect) and the
	// saver keeps task IDs where the flow's checkpoints are.
	step, _ := a2astep.New("translate", translator{}, a2astep.NewMemorySaver(), a2astep.Keys{Input: "text", Output: "french"})
	st := flow.NewState()
	_ = flow.Set(st, "text", "Hello")
	st, err := flow.Run(context.Background(), "run-1", step, flow.WithCheckpointer(flow.NewMemory()), flow.WithState(st))
	if err != nil {
		fmt.Println(err)
		return
	}
	out, _ := flow.Get[a2astep.Output](st, "french")
	fmt.Println(out.TaskID, out.Answer)
	// Output: task-1 Bonjour
}
