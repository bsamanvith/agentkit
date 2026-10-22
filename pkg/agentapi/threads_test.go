package agentapi_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/store/scoped"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

// seedThread stores, in tenant's part of st, a thread id of two messages: a
// request of agent a to agent b and b's reply, the request processed by a
// run.
func seedThread(t *testing.T, st agentapi.Store, tenant, id string) {
	t.Helper()
	tn, err := tenancy.NewTenant(tenant)
	if err != nil {
		t.Fatal(err)
	}
	ctx := tenancy.With(context.Background(), tn)
	req := store.Message{
		ID: id + "_req", ThreadID: id, From: "agent:a", To: "agent:b", FromConversation: "conv_a", ToConversation: "conv_" + id + "_b",
		Kind: store.MessageRequest, Text: "what is 2+2?",
	}
	if _, err := st.SendMessage(ctx, req); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ClaimMessage(ctx, req.ID, "probe", 60e9); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateMessage(ctx, req.ID, "probe", store.MessageUpdate{State: store.MessageDone, RunID: "run_b", Tokens: 42}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SendMessage(ctx, store.Message{
		ID: id + "_reply", ThreadID: id, From: "agent:b", To: "agent:a", FromConversation: req.ToConversation, ToConversation: "conv_a",
		Kind: store.MessageReply, Text: "4", InReplyTo: req.ID,
	}); err != nil {
		t.Fatal(err)
	}
}

// GET /v1/threads lists a tenant's threads newest first, in pages, and GET
// /v1/threads/{id} shows one with its messages; another tenant sees none
// of them.
func TestThreads(t *testing.T) {
	t.Parallel()
	st := scoped.Memory(nil)
	r := newRig(t, echo(t), rigConfig{store: st, api: []agentapi.Option{agentapi.WithTenant(func(req *http.Request) string {
		if h := req.Header.Get("Test-Tenant"); h != "" {
			return h
		}
		return "t1"
	})}})
	seedThread(t, st, "t1", "thr_1")
	seedThread(t, st, "t1", "thr_2")

	var page wire.ThreadList
	if code := r.get("/v1/threads?limit=1", &page); code != http.StatusOK {
		t.Fatalf("GET /v1/threads = %d", code)
	}
	if len(page.Threads) != 1 || page.Threads[0].ID != "thr_2" || page.NextCursor == "" || page.Threads[0].Messages != nil {
		t.Fatalf("first page = %+v, want thr_2 without messages, and a cursor", page)
	}
	var rest wire.ThreadList
	if code := r.get("/v1/threads?limit=1&cursor="+page.NextCursor, &rest); code != http.StatusOK {
		t.Fatalf("second page = %d", code)
	}
	if len(rest.Threads) != 1 || rest.Threads[0].ID != "thr_1" || rest.NextCursor != "" {
		t.Fatalf("second page = %+v, want thr_1 and no cursor", rest)
	}

	var th wire.Thread
	if code := r.get("/v1/threads/thr_1", &th); code != http.StatusOK {
		t.Fatalf("GET /v1/threads/thr_1 = %d", code)
	}
	if th.Status != wire.ThreadOpen || th.Hops != 2 || th.Tokens != 42 || len(th.Messages) != 2 {
		t.Fatalf("thread = %+v, want open, 2 hops, 42 tokens and 2 messages", th)
	}
	req, rep := th.Messages[0], th.Messages[1]
	if req.Hop != 1 || req.Kind != wire.MessageRequest || req.State != wire.MessageDone || req.RunID != "run_b" ||
		req.DeliveredAt == nil || req.ProcessedAt == nil || req.From != "agent:a" || req.To != "agent:b" {
		t.Errorf("request = %+v, want hop 1, done by run_b, delivered and processed", req)
	}
	if rep.Hop != 2 || rep.Kind != wire.MessageReply || rep.InReplyTo != req.ID || rep.State != wire.MessageQueued ||
		rep.DeliveredAt != nil || rep.Text != "4" {
		t.Errorf("reply = %+v, want hop 2, queued, answering the request", rep)
	}

	if code := r.get("/v1/threads/nope", new(map[string]any)); code != http.StatusNotFound {
		t.Errorf("a missing thread = %d, want 404", code)
	}
	if code := r.get("/v1/threads?cursor=nope", new(map[string]any)); code != http.StatusBadRequest {
		t.Errorf("a bad cursor = %d, want 400", code)
	}
	resp, body := r.do(http.MethodGet, "/v1/threads/thr_1", "", "Test-Tenant", "t2")
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("another tenant's GET /v1/threads/thr_1 = %d %s, want 404", resp.StatusCode, body)
	}
	resp, body = r.do(http.MethodGet, "/v1/threads", "", "Test-Tenant", "t2")
	if other := decodeAs[wire.ThreadList](t, body); resp.StatusCode != http.StatusOK || len(other.Threads) != 0 {
		t.Errorf("another tenant's GET /v1/threads = %d %s, want none", resp.StatusCode, body)
	}
}
