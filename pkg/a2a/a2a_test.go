package a2a_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/internal/testkit"
	"github.com/bsamanvith/agentkit/pkg/a2a"
	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
)

// TestTaskIsARunOfTheAgentAPI: a task started over A2A is a run of the
// agent API (GET /v1/runs/{id}) and its context a conversation that GET
// /v1/conversations lists; a message with the contextId is the next run of
// the same conversation.
func TestTaskIsARunOfTheAgentAPI(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	task := r.send("default", keyA, message("hello"), nil)
	if task.Status.State != a2a.StateCompleted || text(task.Artifacts[0].Parts) != "echo: hello" {
		t.Fatalf("task %+v", task)
	}
	run := r.run(keyA, task.ID)
	if run.Status != wire.StatusDone || run.ConversationID != task.ContextID || run.Agent != "default" || run.Answer != "echo: hello" {
		t.Errorf("GET /v1/runs/%s: %+v", task.ID, run)
	}
	status, _, body := r.do(http.MethodGet, "/v1/conversations", keyA, nil)
	var convs wire.ConversationList
	if err := json.Unmarshal(body, &convs); status != http.StatusOK || err != nil {
		t.Fatalf("GET /v1/conversations: %d %s", status, body)
	}
	if len(convs.Conversations) != 1 || convs.Conversations[0].ID != task.ContextID || convs.Conversations[0].Title != "hello" {
		t.Errorf("conversations %+v, want the task's context", convs.Conversations)
	}

	m := message("again")
	m["contextId"] = task.ContextID
	next := r.send("default", keyA, m, nil)
	if next.ContextID != task.ContextID || next.ID == task.ID {
		t.Errorf("the next task %+v, want a new task of context %s", next, task.ContextID)
	}
	status, _, body = r.do(http.MethodGet, "/v1/conversations/"+task.ContextID, keyA, nil)
	var conv wire.Conversation
	if err := json.Unmarshal(body, &conv); status != http.StatusOK || err != nil {
		t.Fatalf("GET conversation: %d %s", status, body)
	}
	if len(conv.Messages) != 4 || conv.Messages[2].Message.Text() != "again" {
		t.Errorf("the conversation has %d messages, want the two prompts and answers", len(conv.Messages))
	}
}

// TestCancelAndTaskErrors: CancelTask cancels the run (POST
// /v1/runs/{id}/cancel), and a canceled task cannot be canceled again; an
// unknown task is TaskNotFoundError to every method; a message to a task
// that has ended, or is working, is UnsupportedOperationError; one whose
// contextId is not its task's is invalid.
func TestCancelAndTaskErrors(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	waiting := r.sendStarted("default", keyA, message("wait"))
	if waiting.Status.State != a2a.StateWorking {
		t.Fatalf("returnImmediately: %s, want working", waiting.Status.State)
	}

	m := message("more")
	m["taskId"] = waiting.ID
	if code := errCode(r.rpc("default", keyA, "SendMessage", map[string]any{"message": m})); code != a2a.ErrUnsupported.Code {
		t.Errorf("message to a working task: code %d", code)
	}

	var canceled a2a.Task
	r.result(r.rpc("default", keyA, "CancelTask", map[string]any{"id": waiting.ID}), &canceled)
	if canceled.Status.State != a2a.StateCanceled {
		t.Errorf("CancelTask: %s", canceled.Status.State)
	}
	if run := r.run(keyA, waiting.ID); run.Status != wire.StatusCanceled {
		t.Errorf("the run is %s, want canceled", run.Status)
	}
	if code := errCode(r.rpc("default", keyA, "CancelTask", map[string]any{"id": waiting.ID})); code != a2a.ErrTaskNotCancelable.Code {
		t.Errorf("CancelTask again: code %d, want TaskNotCancelableError", code)
	}
	if code := errCode(r.rpc("default", keyA, "SubscribeToTask", map[string]any{"id": waiting.ID})); code != a2a.ErrUnsupported.Code {
		t.Errorf("SubscribeToTask of a canceled task: code %d", code)
	}

	done := r.send("default", keyA, message("hi"), nil)
	for _, tc := range []struct {
		name string
		msg  func() map[string]any
		code int
	}{
		{"terminal task", func() map[string]any { m := message("more"); m["taskId"] = done.ID; return m }, a2a.ErrUnsupported.Code},
		{"canceled task", func() map[string]any { m := message("more"); m["taskId"] = waiting.ID; return m }, a2a.ErrUnsupported.Code},
		{"unknown task", func() map[string]any { m := message("more"); m["taskId"] = "run_nope"; return m }, a2a.ErrTaskNotFound.Code},
		{"another context", func() map[string]any {
			m := message("more")
			m["taskId"], m["contextId"] = done.ID, waiting.ContextID
			return m
		}, a2a.ErrInvalidParams.Code},
		{"unknown context", func() map[string]any { m := message("more"); m["contextId"] = "conv_nope"; return m }, a2a.ErrInvalidParams.Code},
	} {
		if code := errCode(r.rpc("default", keyA, "SendMessage", map[string]any{"message": tc.msg()})); code != tc.code {
			t.Errorf("message to a %s: code %d, want %d", tc.name, code, tc.code)
		}
	}

	for _, method := range []string{"GetTask", "CancelTask", "SubscribeToTask"} {
		if code := errCode(r.rpc("default", keyA, method, map[string]any{"id": "run_nope"})); code != a2a.ErrTaskNotFound.Code {
			t.Errorf("%s of an unknown task: code %d", method, code)
		}
		// A task of another agent is not one of this agent's.
		if code := errCode(r.rpc("second", keyA, method, map[string]any{"id": done.ID})); code != a2a.ErrTaskNotFound.Code {
			t.Errorf("%s of another agent's task: code %d", method, code)
		}
	}
	for _, path := range []string{"/tasks/run_nope", "/tasks/run_nope:cancel", "/tasks/run_nope:subscribe"} {
		method := http.MethodGet
		if strings.HasSuffix(path, ":cancel") || strings.HasSuffix(path, ":subscribe") {
			method = http.MethodPost
		}
		status, _, body := r.do(method, a2a.PathPrefix+"default"+path, keyA, nil)
		if status != http.StatusNotFound || !strings.Contains(string(body), `"reason":"TASK_NOT_FOUND"`) {
			t.Errorf("%s %s: %d %s", method, path, status, body)
		}
	}
}

// TestTaskOutlivesItsClient: a task goes on when its stream is closed, and
// after a SendMessage that returned at once: the server follows the run to
// its end.
func TestTaskOutlivesItsClient(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	// The stream is closed after its first item.
	_, items := r.sse(http.MethodPost, a2a.PathPrefix+"default", keyA, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "SendStreamingMessage", "params": map[string]any{"message": message("wait 1")},
	}, 1)
	var first struct{ Result a2a.StreamResponse }
	if err := json.Unmarshal(items[0], &first); err != nil || first.Result.Task == nil {
		t.Fatalf("first item %s", items[0])
	}
	quick := r.send("default", keyA, message("wait 2"), map[string]any{"returnImmediately": true})
	if run := r.run(keyA, first.Result.Task.ID); run.Status != wire.StatusRunning {
		t.Fatalf("the streamed run is %s after its stream closed", run.Status)
	}
	r.release()
	for _, id := range []string{first.Result.Task.ID, quick.ID} {
		// Polled as an A2A client does: until the task is no longer working.
		var got a2a.Task
		testkit.Eventually(t, 10*time.Second, func() bool {
			r.result(r.rpc("default", keyA, "GetTask", map[string]any{"id": id}), &got)
			return got.Status.State != a2a.StateWorking
		})
		if got.Status.State != a2a.StateCompleted || len(got.Artifacts) != 1 || text(got.Artifacts[0].Parts) != "released" {
			t.Errorf("GetTask %s: %+v", id, got)
		}
		if run := r.run(keyA, id); run.Status != wire.StatusDone {
			t.Errorf("run %s is %s", id, run.Status)
		}
	}
}

// TestSubscribeFollowsARunningTask: SubscribeToTask streams a working task
// from its current state to its end, as a second stream of the same task
// does.
func TestSubscribeFollowsARunningTask(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	task := r.send("default", keyA, message("wait"), map[string]any{"returnImmediately": true})
	r.whenStreamOpens(r.release)
	s := r.stream("default", keyA, "SubscribeToTask", map[string]any{"id": task.ID})
	if s[0].Task.Status.State != a2a.StateWorking || s[len(s)-1].StatusUpdate.Status.State != a2a.StateCompleted {
		t.Errorf("stream from %s to %+v", s[0].Task.Status.State, s[len(s)-1])
	}
}

// TestParts: text parts and data parts (as JSON) make the prompt, in order;
// file parts are ContentTypeNotSupportedError, and so are output modes
// without text; a message with no text, from the agent, or with a part of
// no kind is invalid.
func TestParts(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	msg := map[string]any{"role": "ROLE_USER", "messageId": "m", "parts": []any{
		map[string]any{"text": "look at"},
		map[string]any{"data": map[string]any{"b": 1, "a": []int{1, 2}}},
	}}
	task := r.send("default", keyA, msg, nil)
	if got := text(task.Artifacts[0].Parts); got != "echo: look at\n\n{\"a\":[1,2],\"b\":1}" {
		t.Errorf("answer %q", got)
	}
	part := func(p map[string]any) map[string]any {
		return map[string]any{"role": "ROLE_USER", "messageId": "m", "parts": []any{p}}
	}
	for _, tc := range []struct {
		name   string
		params map[string]any
		code   int
	}{
		{"raw", map[string]any{"message": part(map[string]any{"raw": "aGk=", "mediaType": "text/plain"})}, a2a.ErrContentType.Code},
		{"url", map[string]any{"message": part(map[string]any{"url": "https://example.com/a.png"})}, a2a.ErrContentType.Code},
		{"images only", map[string]any{"message": message("hi"), "configuration": map[string]any{"acceptedOutputModes": []string{"image/png"}}}, a2a.ErrContentType.Code},
		{"empty text", map[string]any{"message": part(map[string]any{"text": " "})}, a2a.ErrInvalidParams.Code},
		{"no parts", map[string]any{"message": map[string]any{"role": "ROLE_USER", "messageId": "m", "parts": []any{}}}, a2a.ErrInvalidParams.Code},
		{"two kinds", map[string]any{"message": part(map[string]any{"text": "a", "url": "https://example.com"})}, a2a.ErrInvalidParams.Code},
		{"no kind", map[string]any{"message": part(map[string]any{"mediaType": "text/plain"})}, a2a.ErrInvalidParams.Code},
		{"agent role", map[string]any{"message": map[string]any{"role": "ROLE_AGENT", "messageId": "m", "parts": []any{map[string]any{"text": "hi"}}}}, a2a.ErrInvalidParams.Code},
		{"no message", map[string]any{}, a2a.ErrInvalidParams.Code},
		{"push", map[string]any{"message": message("hi"), "configuration": map[string]any{"taskPushNotificationConfig": map[string]any{"url": "https://example.com"}}}, a2a.ErrPushNotSupported.Code},
		{"negative history", map[string]any{"message": message("hi"), "configuration": map[string]any{"historyLength": -1}}, a2a.ErrInvalidParams.Code},
	} {
		for _, method := range []string{"SendMessage", "SendStreamingMessage"} {
			if code := errCode(r.rpc("default", keyA, method, tc.params)); code != tc.code {
				t.Errorf("%s, %s: code %d, want %d", tc.name, method, code, tc.code)
			}
		}
	}
	if n := r.model.calls.Load(); n != 1 {
		t.Errorf("the model was called %d times; refused messages must not start runs", n)
	}
}

// TestJSONRPCEnvelope: malformed JSON-RPC requests get JSON-RPC's own
// errors, push notification methods PushNotificationNotSupportedError, and
// a version other than 1.0 VersionNotSupportedError.
func TestJSONRPCEnvelope(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	path := a2a.PathPrefix + "default"
	for _, tc := range []struct {
		name   string
		body   string
		header map[string]string
		code   int
	}{
		{"not JSON", `{"jsonrpc":`, nil, a2a.ErrParse.Code},
		{"batch", `[{"jsonrpc":"2.0","id":1,"method":"GetTask","params":{"id":"x"}}]`, nil, a2a.ErrInvalidRequest.Code},
		{"no id", `{"jsonrpc":"2.0","method":"GetTask","params":{"id":"x"}}`, nil, a2a.ErrInvalidRequest.Code},
		{"object id", `{"jsonrpc":"2.0","id":{},"method":"GetTask","params":{"id":"x"}}`, nil, a2a.ErrInvalidRequest.Code},
		{"version 1.0 of JSON-RPC", `{"jsonrpc":"1.0","id":1,"method":"GetTask","params":{"id":"x"}}`, nil, a2a.ErrInvalidRequest.Code},
		{"unknown method", `{"jsonrpc":"2.0","id":1,"method":"message/send","params":{}}`, nil, a2a.ErrMethodNotFound.Code},
		{"no params", `{"jsonrpc":"2.0","id":1,"method":"GetTask"}`, nil, a2a.ErrInvalidParams.Code},
		{"params of the wrong type", `{"jsonrpc":"2.0","id":1,"method":"GetTask","params":{"id":7}}`, nil, a2a.ErrInvalidParams.Code},
		{"push config", `{"jsonrpc":"2.0","id":1,"method":"CreateTaskPushNotificationConfig","params":{"taskId":"x","url":"https://example.com"}}`, nil, a2a.ErrPushNotSupported.Code},
		{"version 2.0", `{"jsonrpc":"2.0","id":1,"method":"GetTask","params":{"id":"x"}}`, map[string]string{"A2A-Version": "2.0"}, a2a.ErrVersionUnsupported.Code},
		{"version 0.3", `{"jsonrpc":"2.0","id":1,"method":"GetTask","params":{"id":"x"}}`, map[string]string{"A2A-Version": "0.3"}, a2a.ErrVersionUnsupported.Code},
		{"version 1.0", `{"jsonrpc":"2.0","id":1,"method":"GetTask","params":{"id":"x"}}`, map[string]string{"A2A-Version": "1.0"}, a2a.ErrTaskNotFound.Code},
	} {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, r.url(path), strings.NewReader(tc.body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+keyA)
		for k, v := range tc.header {
			req.Header.Set(k, v)
		}
		resp, err := r.ts.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var got rpcResp
		err = json.NewDecoder(resp.Body).Decode(&got)
		resp.Body.Close()
		if err != nil || resp.StatusCode != http.StatusOK || got.JSONRPC != "2.0" {
			t.Errorf("%s: HTTP %d, %v", tc.name, resp.StatusCode, err)
			continue
		}
		if errCode(got) != tc.code {
			t.Errorf("%s: code %d, want %d", tc.name, errCode(got), tc.code)
		}
	}
	if status, _, _ := r.do(http.MethodGet, path, keyA, nil); status != http.StatusMethodNotAllowed {
		t.Errorf("GET of the JSON-RPC endpoint: %d", status)
	}
	big := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"SendMessage","params":{"message":{"role":"ROLE_USER","parts":[{"text":%q}]}}}`, strings.Repeat("x", a2a.DefaultMaxBodyBytes))
	if code := errCode(r.rawRPC(path, big)); code != a2a.ErrInvalidRequest.Code {
		t.Errorf("a body over the limit: code %d", code)
	}
}

// rawRPC posts a JSON-RPC body as keyA.
func (r *rig) rawRPC(path, body string) rpcResp {
	r.t.Helper()
	status, _, data := r.do(http.MethodPost, path, keyA, body)
	var resp rpcResp
	if err := json.Unmarshal(data, &resp); err != nil || status != http.StatusOK {
		r.t.Fatalf("HTTP %d: %v %s", status, err, data)
	}
	return resp
}

// TestConcurrentTasks: tasks sent at once each get their own run and
// context, and all complete.
func TestConcurrentTasks(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	const n = 16
	var wg sync.WaitGroup
	tasks := make([]a2a.Task, n)
	streams := make([][]a2a.StreamResponse, n)
	for i := range n {
		wg.Go(func() {
			if i%2 == 0 {
				streams[i] = r.stream("default", keyA, "SendStreamingMessage", map[string]any{"message": message(fmt.Sprintf("q%d", i))})
				return
			}
			tasks[i] = r.send("second", keyA, message(fmt.Sprintf("q%d", i)), nil)
		})
	}
	wg.Wait()
	seen := map[string]bool{}
	for i := range n {
		id, state, ctx := tasks[i].ID, tasks[i].Status.State, tasks[i].ContextID
		if i%2 == 0 {
			id, ctx, state = streams[i][0].Task.ID, streams[i][0].Task.ContextID, streams[i][len(streams[i])-1].StatusUpdate.Status.State
		}
		if state != a2a.StateCompleted || seen[id] || seen[ctx] {
			t.Errorf("task %d: %s %s in %s", i, id, state, ctx)
		}
		seen[id], seen[ctx] = true, true
	}
}
