package wsapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/bsamanvith/agentkit/internal/testkit"
	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
	"github.com/bsamanvith/agentkit/pkg/agentapi/wsapi"
	"github.com/bsamanvith/agentkit/pkg/agentclient"
	"github.com/bsamanvith/agentkit/pkg/auth"
	"github.com/bsamanvith/agentkit/pkg/auth/login"
	"github.com/bsamanvith/agentkit/pkg/auth/login/logintest"
	"github.com/bsamanvith/agentkit/pkg/llm/llmtest"
)

// A run ID of "." or ".." names no run: the API's router would clean its
// path into another route, so the binding answers 404 without asking it.
func TestDotRunIDs(t *testing.T) {
	t.Parallel()
	r := newRig(t, &model{}, rigConfig{})
	w := r.dial()
	w.hello(testToken)
	for _, typ := range []string{wire.WSSubscribe, wire.WSResume, wire.WSStop, wire.WSCancel, wire.WSAnswer, wire.WSDecide, wire.WSSteer, wire.WSPause, wire.WSResumeTask} {
		for _, id := range []string{".", ".."} {
			w.send(wire.WSMessage{Type: typ, ID: typ + id, RunID: id})
			if m := w.must(); m.Type != wire.WSResult || m.ID != typ+id || m.Status != http.StatusNotFound || codeOf(t, m.Body) != wire.CodeNotFound {
				t.Errorf("%s of run %q: %+v", typ, id, m)
			}
		}
	}
}

// A renewal may change the scopes, not the principal: a hello with another
// tenant's key is refused (403) and closes the connection, so the streams
// and commands of a connection are always one tenant's.
func TestRenewalKeepsTheTenant(t *testing.T) {
	t.Parallel()
	const acmeKey, otherKey = "acme-key-0123456789abcdef", "other-key-0123456789abcdef"
	other := apiKey("other", otherKey, agentapi.ScopeRead, agentapi.ScopeRun)
	other.Tenant = "globex"
	keys, err := auth.NewAPIKeys([]auth.APIKey{apiKey("acme", acmeKey, agentapi.ScopeRead, agentapi.ScopeRun), other})
	if err != nil {
		t.Fatal(err)
	}
	r := newRig(t, &model{}, rigConfig{noToken: true, api: []agentapi.Option{agentapi.WithAuthenticator(keys)}})
	w := r.dial()
	w.hello(acmeKey)
	if m := w.hello(acmeKey); m.Status != http.StatusOK {
		t.Fatalf("a renewal with the same key: %+v", m)
	}
	if m := w.hello(otherKey); m.Status != http.StatusForbidden || codeOf(t, m.Body) != wire.CodeForbidden {
		t.Errorf("a renewal with another tenant's key: %+v, want 403 forbidden", m)
	}
	w.send(wire.WSMessage{Type: wire.WSStop, ID: "1", RunID: "run_1"})
	if code := w.closed(); code != wire.WSCloseUnauthorized {
		t.Errorf("closed with %d after a renewal to another tenant, want %d", code, wire.WSCloseUnauthorized)
	}

	// The other tenant's key is good on a connection of its own.
	w = r.dial()
	if m := w.hello(otherKey); m.Status != http.StatusOK {
		t.Errorf("hello with the other key: %+v", m)
	}
}

// A session cookie works as it does over HTTP: the upgrade carries it and
// a same-origin Origin, the hello the session's CSRF token, and unsafe
// commands without the token are refused (403 csrf). A page of another
// origin cannot open the connection at all (403 on the upgrade), unless
// WithOriginPatterns allows it, and its unsafe commands are refused even
// then, as the API checks their Origin.
func TestSessionCookieAndOrigin(t *testing.T) {
	t.Parallel()
	const password = "ws-session-pw-0123456789"
	hasher := &logintest.Hasher{}
	l, err := login.New([]login.User{
		{Username: "ann", PasswordHash: logintest.PHC(hasher.Hash, password, login.DefaultParams), Tenant: agentapi.DefaultTenant, Roles: []string{login.RoleUser}},
	}, login.WithHasher(hasher.Hash))
	if err != nil {
		t.Fatal(err)
	}
	r := newRig(t, &model{}, rigConfig{api: []agentapi.Option{agentapi.WithLogin(l)}, ws: []wsapi.Option{wsapi.WithOriginPatterns("allowed.example")}})
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, r.ts.URL+"/v1/auth/login", strings.NewReader(fmt.Sprintf(`{"username":"ann","password":%q}`, password)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", r.ts.URL)
	resp, err := r.ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	cookies := resp.Cookies()
	if resp.StatusCode/100 != 2 || len(cookies) == 0 {
		t.Fatalf("sign-in: %d, %d cookies", resp.StatusCode, len(cookies))
	}
	header := func(origin string) http.Header {
		h := http.Header{"Origin": {origin}}
		for _, c := range cookies {
			h.Add("Cookie", c.String())
		}
		return h
	}
	dial := func(origin string) (*raw, *http.Response, error) {
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		c, resp, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(r.ts.URL, "http")+wire.WSPath, &websocket.DialOptions{HTTPHeader: header(origin)})
		if err != nil {
			return nil, resp, err
		}
		t.Cleanup(func() { _ = c.CloseNow() })
		return &raw{t: t, c: c}, resp, nil
	}

	// The console's own page: the hello with the cookie alone gives the
	// session and its CSRF token; commands need the token.
	w, _, err := dial(r.ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	var me wire.Me
	if m := w.hello(""); m.Status != http.StatusOK || json.Unmarshal(m.Body, &me) != nil || me.Username != "ann" || me.CSRF == "" {
		t.Fatalf("hello with the session cookie: %+v", m)
	}
	w.send(wire.WSMessage{Type: wire.WSStop, ID: "1", RunID: "run_nope"})
	if m := w.must(); m.Status != http.StatusForbidden || codeOf(t, m.Body) != wire.CodeCSRF {
		t.Errorf("stop without the CSRF token: %+v, want 403 csrf", m)
	}
	w.send(wire.WSMessage{Type: wire.WSHello, ID: "renew", CSRF: me.CSRF})
	if m := w.must(); m.Status != http.StatusOK {
		t.Fatalf("hello with the CSRF token: %+v", m)
	}
	w.send(wire.WSMessage{Type: wire.WSStop, ID: "2", RunID: "run_nope"})
	if m := w.must(); m.Status != http.StatusNotFound {
		t.Errorf("stop with the CSRF token: %+v, want 404", m)
	}

	// Another site's page cannot connect.
	if _, resp, err := dial("https://evil.example"); err == nil || resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Errorf("an upgrade from another origin: %v, %v; want 403", resp, err)
	}

	// An origin the server allows connects, but cannot act for the session.
	w, _, err = dial("https://allowed.example")
	if err != nil {
		t.Fatalf("an upgrade from an allowed origin: %v", err)
	}
	w.send(wire.WSMessage{Type: wire.WSHello, ID: "hello", CSRF: me.CSRF})
	if m := w.must(); m.Status != http.StatusOK {
		t.Fatalf("hello from an allowed origin: %+v", m)
	}
	w.send(wire.WSMessage{Type: wire.WSStop, ID: "1", RunID: "run_nope"})
	if m := w.must(); m.Status != http.StatusForbidden || codeOf(t, m.Body) != wire.CodeCSRF {
		t.Errorf("stop from another origin: %+v, want 403 csrf", m)
	}
}

// A gap names the event the client is to subscribe again after: for a
// stream the client gets no event of, that is the subscribe's own after,
// not the run's start (which would send the events before it again).
func TestGapBeforeTheFirstEvent(t *testing.T) {
	t.Parallel()
	m := &model{replies: []llmtest.Reply{llmtest.ToolCalls(call("toolu_1", "echo", `{"text":"x"}`)), llmtest.Text("Echoed x.")}}
	r := newRig(t, m, rigConfig{ws: []wsapi.Option{wsapi.WithQueue(1)}})
	slow := make(chan struct{}, 1)
	wsapi.OnSlow(r.ws, func() {
		select {
		case slow <- struct{}{}:
		default:
		}
	})
	hc := r.httpClient(testToken)
	s, err := hc.Stream(t.Context(), agentclient.RunRequest{Message: "echo x"})
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := s.Next(); err != nil {
			break
		}
	}
	all := sseEvents(t, hc, s.RunID(), "")
	if len(all) < 4 {
		t.Fatalf("the run has %d events", len(all))
	}
	after := eventIDOf(t, all[1])

	w := r.dial()
	w.hello(testToken)
	w.conn.hold()
	// The writer takes this one's result and waits at the gate, so the
	// queue holds the subscribe's result, and its first event is one too
	// many.
	w.send(wire.WSMessage{Type: "dance", ID: "x"})
	w.conn.waitBlocked(t)
	w.send(wire.WSMessage{Type: wire.WSSubscribe, ID: "s", RunID: s.RunID(), After: after})
	select {
	case <-slow:
	case <-time.After(20 * time.Second):
		t.Fatal("the server never found the client slow")
	}
	w.conn.release()
	var gap *wire.WSMessage
	for {
		msg, err := w.recv()
		if err != nil {
			if code := websocket.CloseStatus(err); code != wire.WSCloseSlow {
				t.Fatalf("closed with %v, want %d", err, wire.WSCloseSlow)
			}
			break
		}
		switch msg.Type {
		case wire.WSEvent:
			t.Fatalf("an event came: %s", msg.Event)
		case wire.WSGap:
			gap = &msg
		}
	}
	if gap == nil || gap.Sub != "s" || gap.After != after {
		t.Errorf("gap %+v, want one after %q, the subscribe's", gap, after)
	}
}

// Once a connection closes (here: its client was found slow), the
// messages that still come are not acted on: a send after the gap starts
// no run.
func TestNothingStartsOnAClosingConnection(t *testing.T) {
	t.Parallel()
	m := &model{replies: []llmtest.Reply{llmtest.ToolCalls(call("toolu_1", "echo", `{"text":"x"}`)), llmtest.Text("Echoed x."), llmtest.Text("spare")}}
	r := newRig(t, m, rigConfig{ws: []wsapi.Option{wsapi.WithQueue(1)}})
	slow := make(chan struct{}, 1)
	wsapi.OnSlow(r.ws, func() {
		select {
		case slow <- struct{}{}:
		default:
		}
	})
	hc := r.httpClient(testToken)
	s, err := hc.Stream(t.Context(), agentclient.RunRequest{Message: "echo x"})
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := s.Next(); err != nil {
			break
		}
	}
	w := r.dial()
	w.hello(testToken)
	w.conn.hold()
	w.send(wire.WSMessage{Type: wire.WSSubscribe, ID: "s", RunID: s.RunID()})
	select {
	case <-slow:
	case <-time.After(20 * time.Second):
		t.Fatal("the server never found the client slow")
	}
	w.send(wire.WSMessage{Type: wire.WSSend, ID: "late", Body: json.RawMessage(`{"message":"hi"}`)})
	w.conn.release()
	for {
		msg, err := w.recv()
		if err != nil {
			break
		}
		if msg.ID == "late" {
			t.Errorf("the send after the gap was answered: %+v", msg)
		}
	}
	// Shutdown waits for the connection and what it started.
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if err := r.ws.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if got := r.wsRequests(); slices.Contains(got, "POST /v1/runs") {
		t.Errorf("the send after the gap reached the API: %q", got)
	}
}

// A stream whose request's context ends is unsubscribed on the server:
// its place on the connection is free again.
func TestCanceledStreamIsUnsubscribed(t *testing.T) {
	t.Parallel()
	m := &model{gate: make(chan struct{}), replies: []llmtest.Reply{llmtest.Text("x")}}
	r := newRig(t, m, rigConfig{ws: []wsapi.Option{wsapi.WithMaxStreams(1)}})
	s, err := r.httpClient(testToken).Stream(t.Context(), agentclient.RunRequest{Message: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	c := r.wsClient(testToken)
	ctx, cancel := context.WithCancel(t.Context())
	ws, err := c.Events(ctx, s.RunID(), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ws.Next(); err != nil {
		t.Fatal(err)
	}
	cancel()
	testkit.Eventually(t, 10*time.Second, func() bool {
		again, err := c.Events(t.Context(), s.RunID(), "")
		if err != nil {
			var ae *agentclient.APIError
			if !errors.As(err, &ae) || ae.Code != wire.CodeOverCapacity {
				t.Fatalf("following the run again: %v", err)
			}
			return false
		}
		_ = again.Close()
		return true
	})
}

// Concurrent requests on a new transport share one connection, and each
// gets its own result.
func TestConcurrentRoundTrips(t *testing.T) {
	t.Parallel()
	r := newRig(t, &model{}, rigConfig{})
	// Requests whose results differ: a stop of no run (404) and an answer
	// with no answers (400).
	do := func(c *agentclient.Client, i int) string {
		var err error
		if i%2 == 0 {
			_, err = c.Stop(t.Context(), fmt.Sprintf("run_nope%d", i))
		} else {
			_, err = c.Answer(t.Context(), fmt.Sprintf("run_nope%d", i))
		}
		var ae *agentclient.APIError
		if !errors.As(err, &ae) {
			return fmt.Sprintf("not an API error: %v", err)
		}
		return fmt.Sprintf("%d %s", ae.Status, ae.Code)
	}
	hc := r.httpClient(testToken)
	want := []string{do(hc, 0), do(hc, 1)}
	if want[0] == want[1] {
		t.Fatalf("both requests give %s", want[0])
	}
	c := r.wsClient(testToken)
	before := r.ln.count()
	var wg sync.WaitGroup
	for i := range 16 {
		wg.Go(func() {
			if got := do(c, i); got != want[i%2] {
				t.Errorf("request %d: %s, want %s", i, got, want[i%2])
			}
		})
	}
	wg.Wait()
	if n := r.ln.count() - before; n != 1 {
		t.Errorf("%d connections, want 1", n)
	}
}
