package agentapi_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/auth"
	"github.com/bsamanvith/agentkit/pkg/auth/authtest"
	"github.com/bsamanvith/agentkit/pkg/clock/clocktest"
	"github.com/bsamanvith/agentkit/pkg/llm/llmtest"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

// userStream is an open GET /v1/events, read in the background.
type userStream struct {
	t      *testing.T
	name   string
	events chan agentapi.UserEvent
	cancel context.CancelFunc
}

// notifications opens GET /v1/events as key (the rig's token for ""), after
// lastEventID if not empty. It checks the response and the SSE framing of
// every event: its id and event name are the notification's.
func (r *rig) notifications(name, key, lastEventID string) *userStream {
	r.t.Helper()
	ctx, cancel := context.WithCancel(context.WithoutCancel(r.t.Context()))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.ts.URL+"/v1/events", nil)
	if err != nil {
		r.t.Fatal(err)
	}
	if key == "" {
		key = r.token
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	if lastEventID != "" {
		req.Header.Set("Last-Event-ID", lastEventID)
	}
	resp, err := r.ts.Client().Do(req) // no timeout: the stream has no end
	if err != nil {
		cancel()
		r.t.Fatal(err)
	}
	ct, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if resp.StatusCode != http.StatusOK || ct != "text/event-stream" {
		cancel()
		resp.Body.Close()
		r.t.Fatalf("%s: GET /v1/events = %d %s", name, resp.StatusCode, ct)
	}
	s := &userStream{t: r.t, name: name, events: make(chan agentapi.UserEvent, 100), cancel: cancel}
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer close(s.events)
		defer resp.Body.Close()
		sc := bufio.NewScanner(resp.Body)
		var id, typ string
		for sc.Scan() {
			line := sc.Text()
			switch {
			case strings.HasPrefix(line, "id: "):
				id = strings.TrimPrefix(line, "id: ")
			case strings.HasPrefix(line, "event: "):
				typ = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				var n agentapi.UserEvent
				if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &n); err != nil {
					r.t.Errorf("%s: data %q: %v", name, line, err)
				}
				if n.ID != id || n.Type != typ || n.V != agentapi.WireVersion || n.At.IsZero() {
					r.t.Errorf("%s: event id %q, name %q, data %+v: want the notification's id and type", name, id, typ, n)
				}
				s.events <- n
			}
		}
	}()
	r.t.Cleanup(func() { cancel(); <-done })
	return s
}

// next returns the next notification.
func (s *userStream) next() agentapi.UserEvent {
	s.t.Helper()
	select {
	case n, ok := <-s.events:
		if !ok {
			s.t.Fatalf("%s: the stream ended", s.name)
		}
		return n
	case <-time.After(30 * time.Second):
		s.t.Fatalf("%s: no notification in 30s", s.name)
	}
	return agentapi.UserEvent{}
}

// untilEnded returns the notifications up to the run's run_status that is
// not running, and the conversation_updated that follows it. Every
// notification must be about the run.
func (s *userStream) untilEnded(runID string) []agentapi.UserEvent {
	s.t.Helper()
	var got []agentapi.UserEvent
	for {
		n := s.next()
		got = append(got, n)
		if n.RunID != runID {
			s.t.Fatalf("%s: got %s of run %q, want only run %q's: %s", s.name, n.Type, n.RunID, runID, jsonOf(got))
		}
		if n.Type != agentapi.TypeRunStatus {
			s.t.Fatalf("%s: got %s before the run ended: %s", s.name, n.Type, jsonOf(got))
		}
		if n.Status != agentapi.StatusRunning {
			if n = s.next(); n.Type != agentapi.TypeConversationUpdated || n.RunID != runID || n.ConversationID != got[0].ConversationID {
				s.t.Fatalf("%s: after %s got %+v, want run %q's conversation_updated", s.name, jsonOf(got), n, runID)
			}
			return append(got, n)
		}
	}
}

// statuses are the run_status statuses of ns.
func statuses(ns []agentapi.UserEvent) []string {
	var out []string
	for _, n := range ns {
		if n.Type == agentapi.TypeRunStatus {
			out = append(out, n.Status)
		}
	}
	return out
}

// The keys of notifyRig: ann and bob in tenant A, a key of A with no user,
// a user of A named as the bus names the no-user stream, and a user named
// ann in tenant B.
const (
	keyAnn   = "key-ann-tenant-a-012345"
	keyBob   = "key-bob-tenant-a-012345"
	keyNoone = "key-no-user-tenant-a-01"
	keyNone  = "key-user-none-tenant-a1"
	keyAnnB  = "key-ann-tenant-b-012345"
)

// notifyRig serves the default agent (echo) behind notifyRig's keys, each
// acting for its principal's tenant and user.
func notifyRig(t *testing.T) *rig {
	t.Helper()
	ten := func(name string) tenancy.Tenant {
		tn, err := tenancy.NewTenant(fmt.Sprintf("%s%d", name, time.Now().UnixNano()))
		if err != nil {
			t.Fatal(err)
		}
		return tn
	}
	a, b := ten("a"), ten("b")
	all := []string{"*"}
	authn := authtest.NewFake(map[string]auth.Principal{
		keyAnn:   {Tenant: a, User: "ann", Scopes: all, Toolsets: all},
		keyBob:   {Tenant: a, User: "bob", Scopes: all, Toolsets: all},
		keyNoone: {Tenant: a, Scopes: all, Toolsets: all},
		keyNone:  {Tenant: a, User: "none", Scopes: all, Toolsets: all},
		keyAnnB:  {Tenant: b, User: "ann", Scopes: all, Toolsets: all},
	})
	r := newRig(t, echo(t), rigConfig{api: []agentapi.Option{agentapi.WithTenant(nil), agentapi.WithAuthenticator(authn)}})
	r.models, r.token = nil, keyAnn // runs are posted raw, by several callers
	return r
}

// chat posts a chat run as key and returns it once it ended.
func (r *rig) chatAs(key, message string) agentapi.Run {
	r.t.Helper()
	resp, body := r.do(http.MethodPost, "/v1/runs", `{"message":"`+message+`"}`, asJSON(key)...)
	var run agentapi.Run
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &run) != nil {
		r.t.Fatalf("POST /v1/runs as %s = %d %s", key[:8], resp.StatusCode, body)
	}
	return run
}

// Notifications: a user's runs and tasks are on that user's stream, in that
// user's tenant, and nobody else's: not another user's of the tenant, not a
// caller's with no user, not the same user name's in another tenant. A
// caller with no user has those of the runs started with no user, and a
// user named as the bus names that stream ("none") does not.
func TestNotificationsReachOnlyTheirUser(t *testing.T) {
	t.Parallel()
	r := notifyRig(t)
	streams := map[string]*userStream{}
	for _, key := range []string{keyAnn, keyBob, keyNoone, keyNone, keyAnnB} {
		streams[key] = r.notifications(key[:12], key, "")
	}

	// Ann's task and chat: on ann's stream, each from running to done,
	// then its conversation updated.
	task := r.task(`{"input":"a task of ann's"}`, as(keyAnn)...)
	got := streams[keyAnn].untilEnded(task.ID)
	if s := statuses(got); !slices.Equal(s, []string{agentapi.StatusRunning, agentapi.StatusDone}) || !got[0].Background {
		t.Errorf("ann's task: %s, want running then done, a task", jsonOf(got))
	}
	if n := got[len(got)-1]; n.ConversationID != task.ConversationID {
		t.Errorf("ann's task's conversation_updated = %+v, want conversation %s", n, task.ConversationID)
	}
	chat := r.chatAs(keyAnn, "a chat of ann's")
	got = streams[keyAnn].untilEnded(chat.ID)
	if s := statuses(got); !slices.Equal(s, []string{agentapi.StatusRunning, agentapi.StatusDone}) || got[0].Background {
		t.Errorf("ann's chat: %s, want running then done, not a task", jsonOf(got))
	}

	// Every other caller's first notification is about its own run, sent
	// after ann's: none of ann's reached them.
	for _, key := range []string{keyBob, keyNoone, keyNone, keyAnnB} {
		run := r.chatAs(key, "mine")
		streams[key].untilEnded(run.ID)
	}
	// And none of theirs reached ann.
	last := r.chatAs(keyAnn, "ann again")
	streams[keyAnn].untilEnded(last.ID)
}

// A replica serves the notifications of runs another replica executes, and
// a client that reconnects with Last-Event-ID gets those after it, once
// each; one whose ID is no longer kept gets a resync first.
func TestNotificationsAcrossReplicas(t *testing.T) {
	t.Parallel()
	m := echo(t)
	m.gate = make(chan struct{}, 1)
	a, b := replicas(t, m)
	s := b.notifications("b", "", "")
	task := a.task(`{"input":"hi"}`)
	running := s.next()
	if running.Type != agentapi.TypeRunStatus || running.RunID != task.ID || running.Status != agentapi.StatusRunning {
		t.Fatalf("b's first notification = %+v, want task %s running", running, task.ID)
	}
	resumed := b.notifications("b, from running", "", running.ID)
	m.gate <- struct{}{}
	rest := s.untilEnded(task.ID)
	if !slices.Equal(statuses(rest), []string{agentapi.StatusDone}) {
		t.Errorf("after running b sent %s, want done", jsonOf(rest))
	}
	if again := resumed.untilEnded(task.ID); !jsonEqual(again, rest) {
		t.Errorf("from Last-Event-ID %s b sent %s, want %s", running.ID, jsonOf(again), jsonOf(rest))
	}
	gone := b.notifications("b, from a lost id", "", "1")
	if n := gone.next(); n.Type != agentapi.TypeResync || n.ID != rest[len(rest)-1].ID {
		t.Errorf("from an ID not kept b sent %+v first, want a resync at %s", n, rest[len(rest)-1].ID)
	}
	if resp, body := b.do(http.MethodGet, "/v1/events", "", "Last-Event-ID", "x"); resp.StatusCode != http.StatusBadRequest || apiCode(body) != agentapi.CodeInvalidRequest {
		t.Errorf("Last-Event-ID x = %d %s, want 400", resp.StatusCode, body)
	}
}

// A task taken over by another process after its own stopped keeps telling
// the user who started it, though the process that takes it over acts for
// nobody; a cancel of a task nobody executes is on the stream too.
func TestNotificationsOfATaskTakenOver(t *testing.T) {
	t.Parallel()
	sets := demoTools(t)
	m := script(t, textAndCalls("Checking.", call("c1", "echo", `{"text":"one"}`)), llmtest.Text("never sent"))
	m.gate = make(chan struct{})
	authn := authtest.NewFake(map[string]auth.Principal{
		keyAnn: {Tenant: mustTenant(t, agentapi.DefaultTenant), User: "ann", Scopes: []string{"*"}, Toolsets: []string{"*"}},
	})
	withAuth := []agentapi.Option{agentapi.WithAuthenticator(authn)}
	a, b := replicasWith(t, m, replicaConfig{
		tools: sets["a"], apiB: withAuth,
		apiA: func(*clocktest.Fake) []agentapi.Option { return withAuth },
	})
	a.token, b.token = keyAnn, keyAnn
	s := b.notifications("ann on b", "", "")
	task := a.task(`{"input":"echo one"}`)
	if n := s.next(); n.RunID != task.ID || n.Status != agentapi.StatusRunning {
		t.Fatalf("ann's first notification = %+v, want her task running", n)
	}
	m.gate <- struct{}{} // turn 1; the answer waits for ever

	// a stops: the task stays running, and says nothing of it.
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if err := a.srv.Shutdown(ctx); !errors.Is(err, agentapi.ErrRunsCutOff) {
		t.Fatalf("a's shutdown: %v, want the task cut off", err)
	}
	c := a.replica(t, script(t, llmtest.Text("Done: one.")))
	c.models = nil
	tctx := tenancy.With(t.Context(), mustTenant(t, agentapi.DefaultTenant)) // no user
	x, err := c.srv.Runner().Resume(tctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	c.srv.RunRecovered(tctx, x)
	got := s.untilEnded(task.ID)
	if !slices.Equal(statuses(got), []string{agentapi.StatusRunning, agentapi.StatusDone}) {
		t.Errorf("after a's stop ann got %s, want the task running again (on c), then done", jsonOf(got))
	}
}

// A run stopped is interrupted on its user's stream, and canceled once
// canceled, though nobody executes it then.
func TestNotificationsOfStopAndCancel(t *testing.T) {
	t.Parallel()
	m := echo(t)
	m.gate = make(chan struct{}) // never answers
	r := newRig(t, m, rigConfig{})
	r.models = nil
	s := r.notifications("r", "", "")
	run := r.start(t.Context(), "hi", "")
	id := run.until(agentapi.TypeRunStart, 0).RunID
	if resp, body := r.do(http.MethodPost, "/v1/runs/"+id+"/stop", ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("stop = %d %s", resp.StatusCode, body)
	}
	run.all()
	if resp, body := r.do(http.MethodPost, "/v1/runs/"+id+"/cancel", ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("cancel = %d %s", resp.StatusCode, body)
	}
	var got []string
	for !slices.Contains(got, agentapi.StatusCanceled) {
		n := s.next()
		if n.RunID != id {
			t.Fatalf("notification %+v, want only run %s's", n, id)
		}
		got = append(got, statuses([]agentapi.UserEvent{n})...)
	}
	i := slices.Index(got, agentapi.StatusInterrupted)
	if got[0] != agentapi.StatusRunning || i < 0 || i > slices.Index(got, agentapi.StatusCanceled) {
		t.Errorf("statuses %v, want running, interrupted, then canceled", got)
	}
}

// The stream ends when the server starts to drain, and when it shuts down,
// so neither waits for it.
func TestNotificationsEndWithTheServer(t *testing.T) {
	t.Parallel()
	stop := make(chan struct{})
	drains := newRig(t, echo(t), rigConfig{api: []agentapi.Option{agentapi.WithDrain(stop, time.Minute)}})
	plain := newRig(t, echo(t), rigConfig{})
	drained, shut := drains.notifications("drained", "", ""), plain.notifications("shut", "", "")
	close(stop)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if err := plain.srv.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	for _, s := range []*userStream{drained, shut} {
		select {
		case n, ok := <-s.events:
			if ok {
				t.Errorf("%s: got %+v, want the stream to end", s.name, n)
			}
		case <-time.After(10 * time.Second):
			t.Errorf("%s: the stream did not end", s.name)
		}
	}
}

// A stop or cancel by another caller of the tenant tells the run's user, and
// only that user: not the caller who sent it, nor a caller with no user. On
// the server holding the run (the stop, then the cancel of a run nobody
// executes) and through the bus from another replica (a cancel the holder
// carries out).
func TestNotificationsOfAnotherCallersStopAndCancel(t *testing.T) {
	t.Parallel()
	m := echo(t)
	m.gate = make(chan struct{}) // a's model never answers
	dt := mustTenant(t, agentapi.DefaultTenant)
	all := []string{"*"}
	authn := authtest.NewFake(map[string]auth.Principal{
		keyAnn:   {Tenant: dt, User: "ann", Scopes: all, Toolsets: all},
		keyBob:   {Tenant: dt, User: "bob", Scopes: all, Toolsets: all},
		keyNoone: {Tenant: dt, Scopes: all, Toolsets: all},
	})
	withAuth := []agentapi.Option{agentapi.WithAuthenticator(authn)}
	a, b := replicasWith(t, m, replicaConfig{
		apiB: withAuth,
		apiA: func(*clocktest.Fake) []agentapi.Option { return withAuth },
	})
	a.token, b.token = keyAnn, keyAnn // bob's requests say so
	a.models = nil                    // runs are posted raw, by several callers
	ann := b.notifications("ann", keyAnn, "")
	others := map[string]*userStream{keyBob: b.notifications("bob", keyBob, ""), keyNoone: b.notifications("no user", keyNoone, "")}

	// untilCanceled reads ann's stream until the run is canceled; every
	// notification on it is about the run.
	untilCanceled := func(id string) []string {
		t.Helper()
		var got []string
		for !slices.Contains(got, agentapi.StatusCanceled) {
			n := ann.next()
			if n.RunID != id {
				t.Fatalf("ann got %+v, want only run %s's", n, id)
			}
			got = append(got, statuses([]agentapi.UserEvent{n})...)
		}
		return got
	}
	post := func(r *rig, path string) {
		t.Helper()
		if resp, body := r.do(http.MethodPost, path, "", asJSON(keyBob)...); resp.StatusCode != http.StatusOK {
			t.Fatalf("bob's POST %s = %d %s", path, resp.StatusCode, body)
		}
	}

	// Bob stops ann's chat on a, which runs it, then cancels it there.
	chat := a.start(t.Context(), "one", "")
	first := chat.until(agentapi.TypeRunStart, 0).RunID
	if n := ann.next(); n.RunID != first || n.Status != agentapi.StatusRunning {
		t.Fatalf("ann's first notification = %+v, want chat %s running", n, first)
	}
	post(a, "/v1/runs/"+first+"/stop")
	chat.all()
	post(a, "/v1/runs/"+first+"/cancel")
	if got := untilCanceled(first); !slices.Contains(got, agentapi.StatusInterrupted) {
		t.Errorf("ann got %v for the chat bob stopped then canceled, want interrupted then canceled", got)
	}

	// Bob cancels ann's next task through b, which does not run it.
	second := a.task(`{"input":"two"}`, as(keyAnn)...)
	for n := ann.next(); n.Status != agentapi.StatusRunning || n.RunID != second.ID; n = ann.next() {
		if n.RunID != first { // the chat's end, which may come twice
			t.Fatalf("ann got %+v, want task %s running", n, second.ID)
		}
	}
	post(b, "/v1/runs/"+second.ID+"/cancel")
	untilCanceled(second.ID)

	// Bob's and the no-user caller's first notifications are of their own
	// runs, run on b after all that: none of ann's reached them.
	for key, s := range others {
		run := b.chatAs(key, "mine")
		s.untilEnded(run.ID)
	}
}

// A caller may have so many notification streams open on a server, each
// holding a subscription of the bus; one more is 503 over_capacity, while
// other callers of the tenant open theirs. A stream closed makes room.
func TestNotificationStreamsPerCaller(t *testing.T) {
	t.Parallel()
	r := notifyRig(t)
	var ann []*userStream
	for i := range agentapi.MaxCallerNotificationStreams {
		ann = append(ann, r.notifications(fmt.Sprintf("ann %d", i), keyAnn, ""))
	}
	resp, body := r.do(http.MethodGet, "/v1/events", "", as(keyAnn)...)
	if resp.StatusCode != http.StatusServiceUnavailable || apiCode(body) != agentapi.CodeOverCapacity || resp.Header.Get("Retry-After") == "" {
		t.Fatalf("one stream more for ann = %d %s (Retry-After %q), want 503 over_capacity", resp.StatusCode, body, resp.Header.Get("Retry-After"))
	}
	r.notifications("bob", keyBob, "")
	r.notifications("no user", keyNoone, "")
	r.notifications("ann of b", keyAnnB, "")
	served := agentapi.MaxCallerNotificationStreams + 3
	if n := r.srv.NotificationStreams(); n != served {
		t.Errorf("the server serves %d streams, want %d", n, served)
	}
	ann[0].cancel()
	eventually(t, "ann's closed stream is counted closed", func() bool { return r.srv.NotificationStreams() == served-1 })
	r.notifications("ann again", keyAnn, "")
}
