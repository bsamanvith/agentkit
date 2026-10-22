package a2a_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/a2a"
)

// TestAuth: the A2A paths take the agent API's credentials, as the agent
// API applies them. With none, or a wrong one, every path is 401 with a
// Bearer challenge; a task is visible only in its tenant; a key without
// agent:run cannot send or cancel; a key that may not use an agent's
// toolsets does not see the agent.
func TestAuth(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	task := r.send("default", keyA, message("secret of a"), nil)
	waiting := r.sendStarted("default", keyA, message("wait"))

	t.Run("credentials", func(t *testing.T) {
		rpcBody := `{"jsonrpc":"2.0","id":1,"method":"GetTask","params":{"id":"` + task.ID + `"}}`
		for _, key := range []string{"", "not-a-key-of-this-server-0123456"} {
			for _, c := range []struct{ method, path, body string }{
				{http.MethodGet, a2a.WellKnownPath, ""},
				{http.MethodGet, a2a.PathPrefix + "default/.well-known/agent-card.json", ""},
				{http.MethodPost, a2a.PathPrefix + "default", rpcBody},
				{http.MethodPost, a2a.PathPrefix + "default/message:send", `{"message":{"role":"ROLE_USER","parts":[{"text":"hi"}]}}`},
				{http.MethodGet, a2a.PathPrefix + "default/tasks/" + task.ID, ""},
				{http.MethodGet, a2a.PathPrefix + "default/tasks", ""},
			} {
				var body any
				if c.body != "" {
					body = c.body
				}
				status, h, data := r.do(c.method, c.path, key, body)
				if status != http.StatusUnauthorized || !strings.HasPrefix(h.Get("WWW-Authenticate"), "Bearer") {
					t.Errorf("%s %s with key %q: %d %q", c.method, c.path, key, status, h.Get("WWW-Authenticate"))
				}
				if strings.Contains(string(data), "secret of a") || strings.Contains(string(data), task.ID) && c.body == "" {
					t.Errorf("%s %s: the answer leaks the task: %s", c.method, c.path, data)
				}
			}
		}
		if n := r.model.calls.Load(); n != 2 {
			t.Errorf("the model ran %d times, want the 2 runs of key a", n)
		}
	})

	t.Run("tenants", func(t *testing.T) {
		for _, method := range []string{"GetTask", "CancelTask", "SubscribeToTask"} {
			for _, id := range []string{task.ID, waiting.ID} {
				if code := errCode(r.rpc("default", keyB, method, map[string]any{"id": id})); code != a2a.ErrTaskNotFound.Code {
					t.Errorf("tenant b, %s of tenant a's task: code %d", method, code)
				}
			}
		}
		var list a2a.ListTasksResponse
		r.result(r.rpc("default", keyB, "ListTasks", map[string]any{}), &list)
		if len(list.Tasks) != 0 || list.TotalSize != 0 {
			t.Errorf("tenant b lists %+v", list.Tasks)
		}
		r.result(r.rpc("default", keyB, "ListTasks", map[string]any{"contextId": task.ContextID}), &list)
		if len(list.Tasks) != 0 {
			t.Errorf("tenant b lists the context of a: %+v", list.Tasks)
		}
		m := message("into a's conversation")
		m["contextId"] = task.ContextID
		if code := errCode(r.rpc("default", keyB, "SendMessage", map[string]any{"message": m})); code != a2a.ErrInvalidParams.Code {
			t.Errorf("tenant b, a message to tenant a's context: code %d", code)
		}
		if run := r.run(keyA, waiting.ID); run.Status != "running" {
			t.Errorf("tenant a's run is %s after tenant b's cancel", run.Status)
		}
		// Tenant b's own task is in tenant b only.
		own := r.send("default", keyB, message("b's"), nil)
		r.result(r.rpc("default", keyA, "ListTasks", map[string]any{}), &list)
		for _, tk := range list.Tasks {
			if tk.ID == own.ID {
				t.Errorf("tenant a lists tenant b's task")
			}
		}
		if status, _, _ := r.do(http.MethodGet, "/v1/runs/"+own.ID, keyA, nil); status != http.StatusNotFound {
			t.Errorf("GET /v1/runs of tenant b's task as tenant a: %d", status)
		}
	})

	t.Run("scopes", func(t *testing.T) {
		var got a2a.Task
		r.result(r.rpc("default", keyRead, "GetTask", map[string]any{"id": task.ID}), &got)
		if got.ID != task.ID {
			t.Errorf("GetTask with agent:read: %+v", got)
		}
		for _, c := range []struct {
			method string
			params map[string]any
		}{
			{"SendMessage", map[string]any{"message": message("hi")}},
			{"SendStreamingMessage", map[string]any{"message": message("hi")}},
			{"CancelTask", map[string]any{"id": waiting.ID}},
		} {
			data, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": c.method, "params": c.params})
			if status, _, body := r.do(http.MethodPost, a2a.PathPrefix+"default", keyRead, data); status != http.StatusForbidden {
				t.Errorf("%s with agent:read only: %d %s", c.method, status, body)
			}
		}
		if run := r.run(keyA, waiting.ID); run.Status != "running" {
			t.Errorf("the run is %s after a refused cancel", run.Status)
		}
	})

	t.Run("agents", func(t *testing.T) {
		for _, path := range []string{
			a2a.PathPrefix + "default/.well-known/agent-card.json", a2a.WellKnownPath, a2a.PathPrefix + "default/tasks/" + task.ID,
		} {
			if status, _, _ := r.do(http.MethodGet, path, keyNoTools, nil); status != http.StatusNotFound {
				t.Errorf("GET %s by a key that may not use the agent: %d", path, status)
			}
		}
		data, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "SendMessage", "params": map[string]any{"message": message("hi")}})
		if status, _, _ := r.do(http.MethodPost, a2a.PathPrefix+"default", keyNoTools, data); status != http.StatusNotFound {
			t.Errorf("SendMessage to an agent the key may not use: %d", status)
		}
		if got := r.send("second", keyNoTools, message("hi"), nil); got.Status.State != a2a.StateCompleted {
			t.Errorf("the agent the key may use: %s", got.Status.State)
		}
	})
}

// TestAgentCard: each agent's card names its interfaces at the server's
// public URL, its skills (the agent, its toolsets), streaming but no push
// notifications, and the security schemes given; the directory is the
// default agent's card. Cards are cached by their ETag.
func TestAgentCard(t *testing.T) {
	t.Parallel()
	r := newRig(t, a2a.WithPublicURL("https://agents.example.com/"))
	status, h, body := r.do(http.MethodGet, a2a.PathPrefix+"default/.well-known/agent-card.json", keyA, nil)
	if status != http.StatusOK || h.Get("Content-Type") != "application/json" {
		t.Fatalf("card: %d %s", status, h.Get("Content-Type"))
	}
	if h.Get("Vary") != "Authorization" || !strings.HasPrefix(h.Get("Cache-Control"), "private") {
		t.Errorf("a card, which depends on the credential, is cached with Vary %q, Cache-Control %q", h.Get("Vary"), h.Get("Cache-Control"))
	}
	var card a2a.AgentCard
	if err := json.Unmarshal(body, &card); err != nil {
		t.Fatal(err)
	}
	if len(card.SupportedInterfaces) != 2 || card.SupportedInterfaces[0].URL != "https://agents.example.com/a2a/default" ||
		card.SupportedInterfaces[0].ProtocolBinding != a2a.BindingJSONRPC || card.SupportedInterfaces[1].ProtocolBinding != a2a.BindingHTTPJSON {
		t.Errorf("interfaces %+v", card.SupportedInterfaces)
	}
	if card.Name != "default" || card.Version != "1.2.3" || !card.Capabilities.Streaming || card.Capabilities.PushNotifications || card.Capabilities.ExtendedAgentCard {
		t.Errorf("card %+v", card)
	}
	if len(card.Skills) != 2 || card.Skills[1].ID != "toolset:demo" || !strings.Contains(card.Skills[1].Description, "echo") {
		t.Errorf("skills %+v", card.Skills)
	}
	if s := card.SecuritySchemes["bearer"]; s.HTTPAuth == nil || s.HTTPAuth.Scheme != "Bearer" || len(card.SecurityRequirements) != 1 {
		t.Errorf("security %+v %+v", card.SecuritySchemes, card.SecurityRequirements)
	}
	if strings.Contains(string(body), "canary") {
		t.Errorf("the card shows the system prompt: %s", body)
	}
	_, _, dir := r.do(http.MethodGet, a2a.WellKnownPath, keyA, nil)
	if string(dir) != string(body) {
		t.Errorf("the directory is not the default agent's card:\n%s\n%s", dir, body)
	}
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, r.url(a2a.WellKnownPath), nil)
	req.Header.Set("Authorization", "Bearer "+keyA)
	req.Header.Set("If-None-Match", h.Get("ETag"))
	resp, err := r.ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotModified {
		t.Errorf("a card fetched again with its ETag: %d", resp.StatusCode)
	}
}
