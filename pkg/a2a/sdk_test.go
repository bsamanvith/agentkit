package a2a_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"os/exec"
	"strings"
	"testing"

	sdk "github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"
	"github.com/a2aproject/a2a-go/v2/a2aclient/agentcard"

	"github.com/bsamanvith/agentkit/pkg/a2a"
	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
)

// bearer is an http.RoundTripper that sends a key, as an A2A client
// configured with one does.
type bearer struct {
	key  string
	next http.RoundTripper
}

func (b bearer) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+b.key)
	return b.next.RoundTrip(req)
}

// sdkClient is the official Go SDK's client of the agent, from its card,
// on one binding.
func (r *rig) sdkClient(agent, key string, binding sdk.TransportProtocol) *a2aclient.Client {
	r.t.Helper()
	hc := &http.Client{Transport: bearer{key: key, next: r.ts.Client().Transport}}
	card, err := agentcard.NewResolver(hc).Resolve(r.t.Context(), r.url(a2a.PathPrefix+agent+"/.well-known/agent-card.json"))
	if err != nil {
		r.t.Fatal(err)
	}
	c, err := a2aclient.NewFromCard(r.t.Context(), card,
		a2aclient.WithJSONRPCTransport(hc), a2aclient.WithRESTTransport(hc),
		a2aclient.WithConfig(a2aclient.Config{PreferredTransports: []sdk.TransportProtocol{binding}}))
	if err != nil {
		r.t.Fatal(err)
	}
	r.t.Cleanup(func() { _ = c.Destroy() })
	return c
}

// ours converts an SDK event to our StreamResponse, through its JSON, for
// the stream invariants.
func (r *rig) ours(e sdk.Event) a2a.StreamResponse {
	r.t.Helper()
	b, err := json.Marshal(e)
	if err != nil {
		r.t.Fatal(err)
	}
	var sr a2a.StreamResponse
	var dst any
	switch e.(type) {
	case *sdk.Task:
		sr.Task = &a2a.Task{}
		dst = sr.Task
	case *sdk.TaskArtifactUpdateEvent:
		sr.ArtifactUpdate = &a2a.TaskArtifactUpdateEvent{}
		dst = sr.ArtifactUpdate
	case *sdk.TaskStatusUpdateEvent:
		sr.StatusUpdate = &a2a.TaskStatusUpdateEvent{}
		dst = sr.StatusUpdate
	default:
		r.t.Fatalf("unexpected event %T", e)
	}
	if err := json.Unmarshal(b, dst); err != nil {
		r.t.Fatal(err)
	}
	return sr
}

// TestSDKClientCompletesAStreamedTask: the official A2A Go SDK's client
// (a2a-go v2.7.0), on each binding, reads the agent's card, completes a
// streamed task of two turns (a tool call, then the answer) on the fake
// model, and reads, lists and continues it.
func TestSDKClientCompletesAStreamedTask(t *testing.T) {
	t.Parallel()
	for _, binding := range []sdk.TransportProtocol{sdk.TransportProtocolJSONRPC, sdk.TransportProtocolHTTPJSON} {
		t.Run(string(binding), func(t *testing.T) {
			t.Parallel()
			r := newRig(t)
			c := r.sdkClient("default", keyA, binding)
			if card := c.Card(); !card.Capabilities.Streaming || card.Capabilities.PushNotifications {
				t.Errorf("capabilities %+v, want streaming and no push notifications", card.Capabilities)
			}

			var events []a2a.StreamResponse
			for e, err := range c.SendStreamingMessage(t.Context(), &sdk.SendMessageRequest{
				Message: sdk.NewMessage(sdk.MessageRoleUser, sdk.NewTextPart("tool, please")),
			}) {
				if err != nil {
					t.Fatal(err)
				}
				events = append(events, r.ours(e))
			}
			r.record(events)
			if len(events) < 3 {
				t.Fatalf("%d events", len(events))
			}
			task := events[0].Task
			last := events[len(events)-1].StatusUpdate
			if task == nil || last == nil || last.Status.State != a2a.StateCompleted {
				t.Fatalf("stream from %+v to %+v, want a task and its completion", events[0], events[len(events)-1])
			}

			got, err := c.GetTask(t.Context(), &sdk.GetTaskRequest{ID: sdk.TaskID(task.ID)})
			if err != nil {
				t.Fatal(err)
			}
			if got.Status.State != sdk.TaskStateCompleted || len(got.Artifacts) != 1 || got.Artifacts[0].Parts[0].Text() != `the echo said {"text":"hi"}` {
				t.Errorf("GetTask: %s with %+v", got.Status.State, got.Artifacts)
			}

			follow, err := c.SendMessage(t.Context(), &sdk.SendMessageRequest{
				Message: &sdk.Message{ID: "m2", ContextID: task.ContextID, Role: sdk.MessageRoleUser, Parts: []*sdk.Part{sdk.NewTextPart("and then?")}},
			})
			if err != nil {
				t.Fatal(err)
			}
			next, ok := follow.(*sdk.Task)
			if !ok || next.ContextID != task.ContextID || next.Status.State != sdk.TaskStateCompleted {
				t.Fatalf("SendMessage in the context: %+v", follow)
			}

			list, err := c.ListTasks(t.Context(), &sdk.ListTasksRequest{ContextID: task.ContextID})
			if err != nil {
				t.Fatal(err)
			}
			if len(list.Tasks) != 2 || string(list.Tasks[0].ID) != string(next.ID) || string(list.Tasks[1].ID) != task.ID {
				t.Errorf("ListTasks: %+v, want the second task, then the first", list.Tasks)
			}

			if _, err := c.CancelTask(t.Context(), &sdk.CancelTaskRequest{ID: next.ID}); !errors.Is(err, sdk.ErrTaskNotCancelable) {
				t.Errorf("CancelTask of a completed task: %v, want ErrTaskNotCancelable", err)
			}
			if run := r.run(keyA, task.ID); run.Status != wire.StatusDone {
				t.Errorf("the run is %s", run.Status)
			}
		})
	}
}

// TestServerUsesNoSDKNorTheAgentAPIServer: the server's own code (not its
// tests) depends on neither the A2A SDK, which only its tests use as a
// client, nor the agent API's server or the harness: it reaches runs
// through the agent API's client, as a remote caller would.
func TestServerUsesNoSDKNorTheAgentAPIServer(t *testing.T) {
	t.Parallel()
	out, err := exec.Command("go", "list", "-deps", ".").Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	for _, dep := range strings.Fields(string(out)) {
		switch {
		case strings.HasPrefix(dep, "github.com/a2aproject/"),
			dep == "github.com/bsamanvith/agentkit/pkg/agentapi", dep == "github.com/bsamanvith/agentkit/pkg/harness":
			t.Errorf("pkg/a2a depends on %s", dep)
		}
	}
}
