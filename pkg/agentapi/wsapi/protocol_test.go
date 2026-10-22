package wsapi_test

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/bsamanvith/agentkit/internal/testkit"
	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
	"github.com/bsamanvith/agentkit/pkg/agentapi/wsapi"
	"github.com/bsamanvith/agentkit/pkg/agentclient"
	"github.com/bsamanvith/agentkit/pkg/agentclient/wsclient"
	"github.com/bsamanvith/agentkit/pkg/auth"
	"github.com/bsamanvith/agentkit/pkg/auth/login"
	"github.com/bsamanvith/agentkit/pkg/clock/clocktest"
	"github.com/bsamanvith/agentkit/pkg/llm/llmtest"
)

// The connection takes nothing before a hello: a command first is
// answered 401 and the connection closes (4001), as is a refused hello.
func TestHelloFirst(t *testing.T) {
	t.Parallel()
	r := newRig(t, &model{}, rigConfig{})

	w := r.dial()
	w.send(wire.WSMessage{Type: wire.WSStop, ID: "1", RunID: "run_1"})
	if m := w.must(); m.Type != wire.WSResult || m.ID != "1" || m.Status != http.StatusUnauthorized || codeOf(t, m.Body) != wire.CodeUnauthorized {
		t.Errorf("a command before the hello: %+v", m)
	}
	if code := w.closed(); code != wire.WSCloseUnauthorized {
		t.Errorf("closed with %d, want %d", code, wire.WSCloseUnauthorized)
	}

	w = r.dial()
	if m := w.hello("wrong-token-0123456789"); m.Status != http.StatusUnauthorized || codeOf(t, m.Body) != wire.CodeUnauthorized {
		t.Errorf("a wrong token's hello: %+v", m)
	}
	if code := w.closed(); code != wire.WSCloseUnauthorized {
		t.Errorf("closed with %d, want %d", code, wire.WSCloseUnauthorized)
	}

	// Even where the API asks for no credentials, the hello comes first.
	open := newRig(t, &model{}, rigConfig{noToken: true})
	w = open.dial()
	w.send(wire.WSMessage{Type: wire.WSStop, ID: "1", RunID: "run_1"})
	if m := w.must(); m.Status != http.StatusUnauthorized || codeOf(t, m.Body) != wire.CodeUnauthorized {
		t.Errorf("a command before the hello, on an open server: %+v", m)
	}
	if code := w.closed(); code != wire.WSCloseUnauthorized {
		t.Errorf("closed with %d, want %d", code, wire.WSCloseUnauthorized)
	}

	w = r.dial()
	m := w.hello(testToken)
	var me wire.Me
	if err := json.Unmarshal(m.Body, &me); err != nil || m.Status != http.StatusOK || me.Tenant != agentapi.DefaultTenant {
		t.Errorf("hello: %+v (%v)", m, err)
	}
}

// A connection that says no hello in time is closed (4001); one that said
// it stays.
func TestHelloTimeout(t *testing.T) {
	t.Parallel()
	clk := clocktest.NewFake(time.Time{})
	r := newRig(t, &model{}, rigConfig{ws: []wsapi.Option{wsapi.WithClock(clk), wsapi.WithHelloTimeout(time.Minute)}})
	said := r.dial()
	said.hello(testToken)
	silent := r.dial()
	if err := clk.WaitForTimers(t.Context(), 2); err != nil { // the two deadlines
		t.Fatal(err)
	}
	clk.Advance(time.Minute)
	if code := silent.closed(); code != wire.WSCloseUnauthorized {
		t.Errorf("closed with %d, want %d", code, wire.WSCloseUnauthorized)
	}
	said.send(wire.WSMessage{Type: wire.WSStop, ID: "1", RunID: "run_nope"})
	if m := said.must(); m.Status != http.StatusNotFound {
		t.Errorf("a command after the deadline, on a connection that said hello: %+v", m)
	}
}

func apiKey(id, key string, scopes ...string) auth.APIKey {
	sum := sha256.Sum256([]byte(key))
	return auth.APIKey{ID: id, Hash: "sha256:" + hex.EncodeToString(sum[:]), Tenant: "acme", Scopes: scopes}
}

// A later hello renews the credentials: the commands after it are
// authorized by its token. The transport's Renew does the same.
func TestRenewal(t *testing.T) {
	t.Parallel()
	const readKey, runKey = "read-key-0123456789abcdef", "run-key-0123456789abcdef"
	keys, err := auth.NewAPIKeys([]auth.APIKey{
		apiKey("reader", readKey, agentapi.ScopeRead),
		apiKey("runner", runKey, agentapi.ScopeRead, agentapi.ScopeRun),
	})
	if err != nil {
		t.Fatal(err)
	}
	r := newRig(t, &model{}, rigConfig{noToken: true, api: []agentapi.Option{agentapi.WithAuthenticator(keys)}})

	w := r.dial()
	w.hello(readKey)
	w.send(wire.WSMessage{Type: wire.WSStop, ID: "1", RunID: "run_1"})
	if m := w.must(); m.Status != http.StatusForbidden || codeOf(t, m.Body) != wire.CodeForbidden {
		t.Errorf("stop with a read-only key: %+v", m)
	}
	if m := w.hello(runKey); m.Status != http.StatusOK {
		t.Fatalf("renewal: %+v", m)
	}
	w.send(wire.WSMessage{Type: wire.WSStop, ID: "2", RunID: "run_1"})
	if m := w.must(); m.Status != http.StatusNotFound || codeOf(t, m.Body) != wire.CodeNotFound {
		t.Errorf("stop after the renewal: %+v, want 404 (the key may stop runs)", m)
	}
	if m := w.hello("revoked-0123456789abcdef"); m.Status != http.StatusUnauthorized {
		t.Errorf("a refused renewal: %+v", m)
	}
	if code := w.closed(); code != wire.WSCloseUnauthorized {
		t.Errorf("closed with %d after a refused renewal", code)
	}

	tr, err := wsclient.New(r.ts.URL, wsclient.WithToken(readKey), wsclient.WithFallback(r.ts.Client().Transport))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tr.Close() })
	c, err := agentclient.New(r.ts.URL, agentclient.WithHTTPClient(&http.Client{Transport: tr}))
	if err != nil {
		t.Fatal(err)
	}
	var ae *agentclient.APIError
	if _, err := c.Stop(t.Context(), "run_1"); !errors.As(err, &ae) || ae.Code != wire.CodeForbidden {
		t.Errorf("Stop with the read key: %v, want ErrForbidden", err)
	}
	if err := tr.Renew(t.Context(), runKey); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Stop(t.Context(), "run_1"); !errors.Is(err, agentclient.ErrNotFound) {
		t.Errorf("Stop after Renew: %v, want ErrNotFound", err)
	}
}

// Messages the binding cannot act on get a 400 result, and the connection
// goes on.
func TestMalformedMessages(t *testing.T) {
	t.Parallel()
	r := newRig(t, &model{gate: make(chan struct{}), replies: []llmtest.Reply{llmtest.Text("x")}}, rigConfig{})
	w := r.dial()
	w.hello(testToken)
	ctx := t.Context()
	if err := w.c.Write(ctx, websocket.MessageText, []byte(`{not json`)); err != nil {
		t.Fatal(err)
	}
	if m := w.must(); m.Status != http.StatusBadRequest || codeOf(t, m.Body) != wire.CodeInvalidRequest {
		t.Errorf("not JSON: %+v", m)
	}
	for _, tc := range []wire.WSMessage{
		{Type: "dance", ID: "1"},
		{Type: wire.WSStop, ID: "2"},
		{Type: wire.WSStop, RunID: "run_1"},
		{Type: wire.WSUnsubscribe, ID: "3", Sub: "nope"},
	} {
		w.send(tc)
		m := w.must()
		want := http.StatusBadRequest
		if tc.Type == wire.WSUnsubscribe {
			want = http.StatusNotFound
		}
		if m.Type != wire.WSResult || m.ID != tc.ID || m.Status != want {
			t.Errorf("%+v answered %+v, want %d", tc, m, want)
		}
	}
	// An ID in use by a stream is refused.
	w.send(wire.WSMessage{Type: wire.WSSend, ID: "s", Body: json.RawMessage(`{"message":"hi"}`)})
	if m := w.must(); m.Status != http.StatusOK || m.RunID == "" {
		t.Fatalf("send: %+v", m)
	}
	w.send(wire.WSMessage{Type: wire.WSStop, ID: "s", RunID: "run_1"})
	for {
		m := w.must()
		if m.Type == wire.WSResult {
			if m.ID != "s" || m.Status != http.StatusBadRequest {
				t.Errorf("a reused ID: %+v", m)
			}
			break
		}
	}
}

// A connection holds at most WithMaxStreams streams: one more is 503
// over_capacity. Unsubscribe closes a stream, which ends with end, and
// frees its place.
func TestStreamLimitAndUnsubscribe(t *testing.T) {
	t.Parallel()
	m := &model{gate: make(chan struct{}), replies: []llmtest.Reply{llmtest.Text("x")}}
	r := newRig(t, m, rigConfig{ws: []wsapi.Option{wsapi.WithMaxStreams(1)}})
	s, err := r.httpClient(testToken).Stream(t.Context(), agentclient.RunRequest{Message: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	w := r.dial()
	w.hello(testToken)
	w.send(wire.WSMessage{Type: wire.WSSubscribe, ID: "a", RunID: s.RunID()})
	if res := w.must(); res.Status != http.StatusOK || res.RunID != s.RunID() || res.ConversationID != s.ConversationID() {
		t.Fatalf("subscribe: %+v", res)
	}
	if ev := w.must(); ev.Type != wire.WSEvent || ev.Sub != "a" {
		t.Fatalf("first message of the stream: %+v", ev)
	}
	w.send(wire.WSMessage{Type: wire.WSSubscribe, ID: "b", RunID: s.RunID()})
	if res := w.must(); res.ID != "b" || res.Status != http.StatusServiceUnavailable || codeOf(t, res.Body) != wire.CodeOverCapacity || res.RetryAfter == "" {
		t.Errorf("a stream past the limit: %+v", res)
	}
	w.send(wire.WSMessage{Type: wire.WSUnsubscribe, ID: "u", Sub: "a"})
	var gotResult, gotEnd bool
	for !gotResult || !gotEnd {
		switch m := w.must(); {
		case m.Type == wire.WSResult && m.ID == "u" && m.Status == http.StatusOK:
			gotResult = true
		case m.Type == wire.WSEnd && m.Sub == "a":
			gotEnd = true
		default:
			t.Fatalf("after unsubscribe: %+v", m)
		}
	}
	testkit.Eventually(t, 10*time.Second, func() bool {
		w.send(wire.WSMessage{Type: wire.WSSubscribe, ID: "c", RunID: s.RunID()})
		res := w.must()
		if res.Status == http.StatusOK {
			return true
		}
		if res.Status != http.StatusServiceUnavailable {
			t.Fatalf("subscribe after unsubscribe: %+v", res)
		}
		return false
	})
}

// A client too slow to read its streams is not waited for: each stream
// open gets a gap naming the last event sent on it, and the connection
// closes (4008). Subscribing again after that event gives the rest, the
// same as the SSE stream after it, and nothing twice (T091's second
// criterion).
func TestSlowClientGetsAGapAndResumes(t *testing.T) {
	t.Parallel()
	m := &model{replies: []llmtest.Reply{llmtest.ToolCalls(call("toolu_1", "echo", `{"text":"x"}`)), llmtest.Text("Echoed x, and then some more words.")}}
	r := newRig(t, m, rigConfig{ws: []wsapi.Option{wsapi.WithQueue(2)}})
	slow := make(chan struct{}, 4)
	wsapi.OnSlow(r.ws, func() {
		select {
		case slow <- struct{}{}:
		default:
		}
	})
	ctx := t.Context()
	hc := r.httpClient(testToken)
	s, err := hc.Stream(ctx, agentclient.RunRequest{Message: "echo x"})
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := s.Next(); err != nil {
			break
		}
	}
	want := sseEvents(t, hc, s.RunID(), "")
	if len(want) < 5 {
		t.Fatalf("the run has %d events; the test needs more than the queue holds", len(want))
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
	w.conn.release()
	var got []string
	after := ""
	for {
		msg, err := w.recv()
		if err != nil {
			if code := websocket.CloseStatus(err); code != wire.WSCloseSlow {
				t.Fatalf("closed with %v, want %d", err, wire.WSCloseSlow)
			}
			break
		}
		switch msg.Type {
		case wire.WSResult:
		case wire.WSEvent:
			got = append(got, string(msg.Event))
		case wire.WSGap:
			if msg.Sub != "s" {
				t.Errorf("gap %+v", msg)
			}
			after = msg.After
		default:
			t.Fatalf("message %+v", msg)
		}
	}
	if len(got) == 0 || len(got) >= len(want) {
		t.Fatalf("got %d of %d events before the gap", len(got), len(want))
	}
	if last := eventIDOf(t, got[len(got)-1]); after != last {
		t.Errorf("the gap is after %q, the last event sent is %q", after, last)
	}

	firstGap, before := after, len(got)

	// Subscribing again after each gap gets the rest. The queue is so
	// short that even a client reading at once may get gaps: each
	// connection still gets some events further.
	for ended := false; !ended; {
		w = r.dial()
		w.hello(testToken)
		w.send(wire.WSMessage{Type: wire.WSSubscribe, ID: "s", RunID: s.RunID(), After: after})
		if res := w.must(); res.Status != http.StatusOK {
			t.Fatalf("subscribe after the gap: %+v", res)
		}
		progress := len(got)
		for done := false; !done; {
			msg, err := w.recv()
			switch {
			case err != nil:
				t.Fatalf("closed before an end or a gap: %v", err)
			case msg.Type == wire.WSEnd:
				done, ended = true, true
			case msg.Type == wire.WSGap:
				done, after = true, msg.After
				if code := w.closed(); code != wire.WSCloseSlow {
					t.Fatalf("closed with %d after a gap", code)
				}
			default:
				got = append(got, string(msg.Event))
			}
		}
		if !ended && len(got) == progress {
			t.Fatal("a resubscription got no further")
		}
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("events before and after the gaps:\n%s\nwant (SSE):\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if rest := sseEvents(t, hc, s.RunID(), firstGap); strings.Join(rest, "\n") != strings.Join(got[before:], "\n") {
		t.Errorf("the SSE stream after %q differs from the WebSocket's", firstGap)
	}
}

// A slow client of a run going on does not hold the run up: the run ends
// while the client's connection cannot be written to, and the client gets
// its gap afterwards.
func TestSlowClientDoesNotBlockTheRun(t *testing.T) {
	t.Parallel()
	m := &model{gate: make(chan struct{}), replies: []llmtest.Reply{llmtest.ToolCalls(call("toolu_1", "echo", `{"text":"x"}`)), llmtest.Text("Echoed x, and then some more words.")}}
	r := newRig(t, m, rigConfig{ws: []wsapi.Option{wsapi.WithQueue(1)}})
	slow := make(chan struct{}, 4)
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
	defer s.Close()
	w := r.dial()
	w.hello(testToken)
	w.conn.hold()
	w.send(wire.WSMessage{Type: wire.WSSubscribe, ID: "s", RunID: s.RunID()})
	close(m.gate)
	for {
		if _, err := s.Next(); err != nil {
			if !errors.Is(err, io.EOF) {
				t.Fatalf("the HTTP stream: %v", err)
			}
			break
		}
	}
	testkit.Eventually(t, 20*time.Second, func() bool {
		run, err := hc.GetRun(t.Context(), s.RunID())
		return err == nil && run.Status == wire.StatusDone
	})
	select {
	case <-slow:
	case <-time.After(20 * time.Second):
		t.Fatal("the server never found the client slow")
	}
	w.conn.release()
	sawGap := false
	for {
		msg, err := w.recv()
		if err != nil {
			break
		}
		sawGap = sawGap || msg.Type == wire.WSGap
	}
	if !sawGap {
		t.Error("the slow client got no gap")
	}
}

// Heartbeats close a connection whose client does not answer pings, and
// keep one that does. The heartbeat and the pong timeout run on the
// server's clock, a fake one here.
func TestHeartbeatClosesADeadClient(t *testing.T) {
	t.Parallel()
	const every, wait = 30 * time.Second, 10 * time.Second
	rig := func() (*rig, *clocktest.Fake, chan error) {
		clk := clocktest.NewFake(time.Time{})
		r := newRig(t, &model{}, rigConfig{ws: []wsapi.Option{wsapi.WithClock(clk), wsapi.WithHeartbeat(every, wait), wsapi.WithHelloTimeout(time.Hour)}})
		pings := make(chan error, 8)
		wsapi.OnPing(r.ws, func(err error) { pings <- err })
		return r, clk, pings
	}
	ping := func(pings chan error) error {
		t.Helper()
		select {
		case err := <-pings:
			return err
		case <-time.After(20 * time.Second):
			t.Fatal("the server sent no ping")
			return nil
		}
	}

	// A client that reads answers the pings and stays.
	r, clk, pings := rig()
	live := r.dial()
	live.hello(testToken)
	readCtx := live.c.CloseRead(t.Context())
	if err := clk.WaitForTimers(t.Context(), 2); err != nil { // the heartbeat and the hello deadline
		t.Fatal(err)
	}
	for range 3 {
		clk.Advance(every)
		if err := ping(pings); err != nil {
			t.Fatalf("the ping of a client that reads: %v", err)
		}
	}
	if live.conn.isClosed() || readCtx.Err() != nil {
		t.Error("the heartbeat closed a client that answers")
	}

	// One that reads no more never answers: past the pong timeout, its
	// connection is closed.
	r, clk, pings = rig()
	dead := r.dial()
	dead.hello(testToken)
	if err := clk.WaitForTimers(t.Context(), 2); err != nil {
		t.Fatal(err)
	}
	clk.Advance(every)
	if err := clk.WaitForTimers(t.Context(), 3); err != nil { // and the pong's
		t.Fatal(err)
	}
	clk.Advance(wait)
	if err := ping(pings); err == nil {
		t.Fatal("a ping nobody answered succeeded")
	}
	testkit.Eventually(t, 20*time.Second, dead.conn.isClosed)
}

// Shutdown closes the connections (1001) and refuses new ones (503).
func TestShutdown(t *testing.T) {
	t.Parallel()
	r := newRig(t, &model{}, rigConfig{})
	w := r.dial()
	w.hello(testToken)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	closed := make(chan websocket.StatusCode, 1)
	go func() { // a client that reads answers the close at once
		_, _, err := w.c.Read(ctx)
		closed <- websocket.CloseStatus(err)
	}()
	if err := r.ws.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if code := <-closed; code != websocket.StatusGoingAway {
		t.Errorf("closed with %d, want 1001", code)
	}
	_, resp, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(r.ts.URL, "http")+wire.WSPath, nil)
	if err == nil || resp == nil || resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("a dial after the shutdown: %v, %v", resp, err)
	}
}

// A plain request to the endpoint is 426 with the API's error body.
func TestNotAnUpgrade(t *testing.T) {
	t.Parallel()
	r := newRig(t, &model{}, rigConfig{})
	resp, err := r.ts.Client().Get(r.ts.URL + wire.WSPath)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var e wire.ErrorResponse
	if err := json.NewDecoder(resp.Body).Decode(&e); err != nil || resp.StatusCode != http.StatusUpgradeRequired || e.Error == nil || e.Error.Code != wire.CodeInvalidRequest {
		t.Errorf("GET without an upgrade: %d %+v %v", resp.StatusCode, e.Error, err)
	}
}

// The hello's CSRF token goes in the header the login package checks.
func TestCSRFHeader(t *testing.T) {
	t.Parallel()
	if wsapi.CSRFHeader != login.CSRFHeader {
		t.Errorf("the binding sends the CSRF token in %q, the login package reads %q", wsapi.CSRFHeader, login.CSRFHeader)
	}
}

// The binding serves the API's handler; it links neither the server nor
// the harness, and the client links no server either.
func TestBindingDoesNotImportTheServer(t *testing.T) {
	t.Parallel()
	for _, pkg := range []string{".", "../../agentclient/wsclient"} {
		out, err := exec.Command("go", "list", "-deps", pkg).Output()
		if err != nil {
			t.Fatalf("go list: %v", err)
		}
		sc := bufio.NewScanner(strings.NewReader(string(out)))
		for sc.Scan() {
			switch strings.TrimPrefix(sc.Text(), "github.com/bsamanvith/agentkit/") {
			case "pkg/agentapi", "pkg/harness", "pkg/agentclient":
				t.Errorf("%s depends on %s", pkg, sc.Text())
			}
		}
	}
}

// sseEvents are a run's events after the ID after, over SSE, as the
// server wrote their data.
func sseEvents(t *testing.T, c *agentclient.Client, runID, after string) []string {
	t.Helper()
	s, err := c.Events(t.Context(), runID, after)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var out []string
	for {
		if _, err := s.Next(); err != nil {
			return out
		}
		out = append(out, string(s.RawData()))
	}
}

func eventIDOf(t *testing.T, data string) string {
	t.Helper()
	var e wire.Event
	if err := json.Unmarshal([]byte(data), &e); err != nil {
		t.Fatal(err)
	}
	return e.ID
}

// Through the client's transport, a stream the server cut off ends with
// an error matching wsclient.ErrGap that names the last event received;
// following the run from there dials again and gets the rest.
func TestClientResumesAfterAGap(t *testing.T) {
	t.Parallel()
	m := &model{replies: []llmtest.Reply{llmtest.ToolCalls(call("toolu_1", "echo", `{"text":"x"}`)), llmtest.Text("Echoed x, and then some more words.")}}
	r := newRig(t, m, rigConfig{ws: []wsapi.Option{wsapi.WithQueue(2)}})
	slow := make(chan struct{}, 8)
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
	want := sseEvents(t, hc, s.RunID(), "")

	c := r.wsClient(testToken)
	if _, err := c.Stop(t.Context(), "run_nope"); !errors.Is(err, agentclient.ErrNotFound) { // dials
		t.Fatal(err)
	}
	conn := r.ln.conn(r.ln.count() - 1)
	conn.hold()
	type opened struct {
		s   *agentclient.Stream
		err error
	}
	ch := make(chan opened, 1)
	go func() {
		ws, err := c.Events(t.Context(), s.RunID(), "")
		ch <- opened{ws, err}
	}()
	select {
	case <-slow:
	case <-time.After(20 * time.Second):
		t.Fatal("the server never found the client slow")
	}
	conn.release()
	o := <-ch
	if o.err != nil {
		t.Fatal(o.err)
	}
	ws := o.s
	defer ws.Close()
	var got []string
	var gap *wsclient.GapError
	for {
		if _, err := ws.Next(); err != nil {
			if !errors.Is(err, wsclient.ErrGap) || !errors.As(err, &gap) {
				t.Fatalf("the stream ended with %v, want a gap", err)
			}
			break
		}
		got = append(got, string(ws.RawData()))
	}
	if last := eventIDOf(t, got[len(got)-1]); gap.After != last {
		t.Errorf("gap after %q, last event %q", gap.After, last)
	}
	for after := gap.After; ; {
		rest, err := c.Events(t.Context(), s.RunID(), after)
		if err != nil {
			t.Fatal(err)
		}
		for {
			_, err := rest.Next()
			if err == nil {
				got = append(got, string(rest.RawData()))
				continue
			}
			if errors.As(err, &gap) {
				after = gap.After
				break
			}
			if !errors.Is(err, io.EOF) {
				t.Fatal(err)
			}
			if strings.Join(got, "\n") != strings.Join(want, "\n") {
				t.Errorf("events over the WebSocket, with gaps:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
			}
			return
		}
	}
}
