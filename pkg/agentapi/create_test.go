package agentapi_test

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/agentapi"
)

// create posts body to POST /v1/conversations and decodes the 201.
func (r *rig) create(body string, header ...string) agentapi.Conversation {
	r.t.Helper()
	resp, got := r.do("POST", "/v1/conversations", body, header...)
	var c agentapi.Conversation
	if resp.StatusCode != http.StatusCreated || json.Unmarshal(got, &c) != nil {
		r.t.Fatalf("POST /v1/conversations %s = %d %s, want 201 and a conversation", body, resp.StatusCode, got)
	}
	if loc := resp.Header.Get("Location"); loc != "/v1/conversations/"+c.ID {
		r.t.Errorf("Location %q, want /v1/conversations/%s", loc, c.ID)
	}
	return c
}

// TestCreateConversation: POST /v1/conversations creates an empty, idle
// conversation of the agent named, which GET and the list show as created;
// the first run in it is that agent's, sees no earlier messages, and gives
// the conversation its title.
func TestCreateConversation(t *testing.T) {
	t.Parallel()
	r := newTwoAgents(t)
	c := r.create(`{"agent":"search"}`)
	if !strings.HasPrefix(c.ID, "conv_") || c.Agent != "search" || c.Status != agentapi.StatusIdle || c.Messages == nil || len(c.Messages) != 0 || !c.CreatedAt.Equal(c.UpdatedAt) {
		t.Errorf("created %+v; want an idle search conversation with no messages", c)
	}
	if got := r.getConversation(c.ID); !jsonEqual(got, c) {
		t.Errorf("GET returns\n%s\nnot what POST did\n%s", jsonOf(got), jsonOf(c))
	}
	if sum, ok := r.listAll()[c.ID]; !ok || sum.Title != "" || sum.MessageCount != 0 || sum.Status != agentapi.StatusIdle || sum.Agent != "search" {
		t.Errorf("listed as %+v (%v); want untitled, idle, no messages", sum, ok)
	}

	r.clock.Advance(time.Minute)
	s := r.startAgent(t.Context(), "What is new\n in Go?", c.ID, "")
	s.all()
	if got := s.conversationID(); got != c.ID {
		t.Errorf("the run went to %s, not the created %s", got, c.ID)
	}
	reqs := r.search.requests()
	if len(reqs) != 1 || len(reqs[0].Messages) != 1 || len(r.plain.requests()) != 0 {
		t.Errorf("search got %d requests (the first with %d messages), plain %d; want the search agent, from an empty transcript",
			len(reqs), len(reqs[0].Messages), len(r.plain.requests()))
	}
	sum := r.listAll()[c.ID]
	if sum.Title != "What is new in Go?" || sum.MessageCount != 2 || !sum.UpdatedAt.After(sum.CreatedAt) {
		t.Errorf("after the first run: %+v; want the first message as title, 2 messages, updated", sum)
	}
	r.startAgent(t.Context(), "And before that?", c.ID, "").all()
	if sum := r.listAll()[c.ID]; sum.Title != "What is new in Go?" {
		t.Errorf("the second message retitled it: %q", sum.Title)
	}
}

// TestCreateConversationRefusals: what POST /v1/conversations refuses, and
// what a run in a created conversation refuses, each changing nothing.
func TestCreateConversationRefusals(t *testing.T) {
	t.Parallel()
	r := newTwoAgents(t) // two agents, neither the default
	for _, c := range []struct {
		name, body string
		status     int
		code       string
	}{
		{"no agent, no default", ``, http.StatusBadRequest, agentapi.CodeInvalidRequest},
		{"empty object, no default", `{}`, http.StatusBadRequest, agentapi.CodeInvalidRequest},
		{"unknown agent", `{"agent":"nobody"}`, http.StatusNotFound, agentapi.CodeNotFound},
		{"unknown field", `{"agent":"plain","title":"x"}`, http.StatusBadRequest, agentapi.CodeInvalidRequest},
		{"not an object", `["plain"]`, http.StatusBadRequest, agentapi.CodeInvalidRequest},
		{"trailing data", `{"agent":"plain"} {}`, http.StatusBadRequest, agentapi.CodeInvalidRequest},
		{"wrong type", `{"agent":7}`, http.StatusBadRequest, agentapi.CodeInvalidRequest},
	} {
		resp, body := r.do("POST", "/v1/conversations", c.body)
		wantError(t, c.name, resp, body, c.status, c.code)
	}
	if got := r.listAll(); len(got) != 0 {
		t.Errorf("refused requests created %d conversations", len(got))
	}

	c := r.create(`{"agent":"plain"}`)
	resp, body := r.do("POST", "/v1/runs", jsonRun("", c.ID, ""), "Accept", "application/json")
	wantError(t, "an empty message to an empty conversation", resp, body, http.StatusBadRequest, agentapi.CodeInvalidRequest)
	resp, body = r.do("POST", "/v1/runs", jsonRun("hi", c.ID, "search"), "Accept", "application/json")
	wantError(t, "another agent in a created conversation", resp, body, http.StatusConflict, agentapi.CodeAgentMismatch)
	if got := r.getConversation(c.ID); len(got.Messages) != 0 || len(r.plain.requests())+len(r.search.requests()) != 0 {
		t.Errorf("the refused runs changed the conversation or called a model: %+v", got)
	}
	r.startAgent(t.Context(), "hi", c.ID, "plain").all() // its own agent, named, is fine
}

// TestCreateConversationDefaultAgent: with no body, or an empty one, the
// conversation is the default agent's.
func TestCreateConversationDefaultAgent(t *testing.T) {
	t.Parallel()
	r := newRig(t, echo(t), rigConfig{})
	for _, body := range []string{``, ` `, `{}`, `{"agent":""}`} {
		if c := r.create(body); c.Agent != agentapi.DefaultAgent {
			t.Errorf("body %q: agent %q, want %q", body, c.Agent, agentapi.DefaultAgent)
		}
	}
}

// TestCreatedConversationsAreTheCallersTenant: a conversation created by one
// tenant does not exist for another.
func TestCreatedConversationsAreTheCallersTenant(t *testing.T) {
	t.Parallel()
	tenant := agentapi.WithTenant(func(r *http.Request) string { return r.Header.Get("Test-Tenant") })
	m := echo(t)
	r := newRig(t, m, rigConfig{api: []agentapi.Option{tenant}})
	c := r.create(`{}`, "Test-Tenant", "a")
	for _, req := range []struct{ method, path, body string }{
		{"GET", "/v1/conversations/" + c.ID, ""},
		{"POST", "/v1/runs", jsonRun("theirs", c.ID, "")},
		{"DELETE", "/v1/conversations/" + c.ID, ""},
	} {
		resp, body := r.do(req.method, req.path, req.body, "Test-Tenant", "b", "Accept", "application/json")
		wantError(t, "tenant b: "+req.method+" "+req.path, resp, body, http.StatusNotFound, agentapi.CodeNotFound)
	}
	resp, body := r.do("GET", "/v1/conversations", "", "Test-Tenant", "b")
	if resp.StatusCode != http.StatusOK || strings.Contains(string(body), c.ID) {
		t.Errorf("tenant b lists %s", body)
	}
	resp, body = r.do("POST", "/v1/runs", jsonRun("mine", c.ID, ""), "Test-Tenant", "a", "Accept", "application/json")
	if resp.StatusCode != http.StatusOK || len(m.requests()) != 1 {
		t.Errorf("tenant a's run in its conversation = %d %s", resp.StatusCode, body)
	}
}

// TestRetentionDropsEmptyConversationsFirst: past the conversation count,
// conversations without messages go first, oldest first; the one just
// created never goes; past the retention age, empty ones go like any other.
func TestRetentionDropsEmptyConversationsFirst(t *testing.T) {
	t.Parallel()
	r := newRig(t, echo(t), rigConfig{api: []agentapi.Option{agentapi.WithRetention(10, 3, time.Hour)}})
	used := r.runJSON("keep me", "").ConversationID
	used2 := r.runJSON("keep me too", "").ConversationID
	var empty []string
	for range 4 {
		r.clock.Advance(time.Second)
		empty = append(empty, r.create(`{}`).ID)
	}
	listed := r.listAll()
	want := []string{used, used2, empty[3]} // each create dropped the empty one before it
	if got := slices.Sorted(maps.Keys(listed)); !slices.Equal(got, slices.Sorted(slices.Values(want))) {
		t.Errorf("kept %v, want %v: the used conversations and the newest", got, want)
	}

	// A create that must drop a conversation with messages drops the least
	// recently used, never itself.
	r2 := newRig(t, echo(t), rigConfig{api: []agentapi.Option{agentapi.WithRetention(10, 1, time.Hour)}})
	old := r2.runJSON("old", "").ConversationID
	c := r2.create(`{}`)
	var v agentapi.ErrorResponse
	if code := r2.get("/v1/conversations/"+old, &v); code != http.StatusNotFound {
		t.Errorf("the used conversation is still there (%d), past a count of 1", code)
	}
	r2.getConversation(c.ID)

	r2.clock.Advance(2 * time.Hour)
	c2 := r2.create(`{}`)
	if code := r2.get("/v1/conversations/"+c.ID, &v); code != http.StatusNotFound {
		t.Errorf("an empty conversation older than the retention is still there (%d)", code)
	}
	r2.getConversation(c2.ID)
}

// TestCreateConversationsConcurrently: concurrent creates and runs (under
// -race) give distinct conversations, each listed once.
func TestCreateConversationsConcurrently(t *testing.T) {
	t.Parallel()
	r := newRig(t, echo(t), rigConfig{})
	const n = 16
	ids := make([]string, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			if i%2 == 0 {
				ids[i] = r.create(`{}`).ID
				return
			}
			c := r.create(`{}`)
			r.startAgent(t.Context(), fmt.Sprint("hello ", i), c.ID, "").all()
			ids[i] = c.ID
		})
	}
	wg.Wait()
	listed := r.listAll()
	slices.Sort(ids)
	if len(slices.Compact(ids)) != n || len(listed) != n {
		t.Errorf("%d distinct IDs, %d listed; want %d each", len(slices.Compact(ids)), len(listed), n)
	}
}
