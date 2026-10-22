package wsclient_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
	"github.com/bsamanvith/agentkit/pkg/agentclient"
	"github.com/bsamanvith/agentkit/pkg/agentclient/wsclient"
)

// A connection whose server sent a gap is closing (the server closes it
// next): the transport sends nothing more on it, and dials again, even
// while the close has not come yet. This server never closes, so a
// request on the old connection would never be answered.
func TestAfterAGapTheTransportDialsAgain(t *testing.T) {
	t.Parallel()
	var conns atomic.Int64
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = c.CloseNow() }()
		n := conns.Add(1)
		ctx := r.Context()
		for {
			_, data, err := c.Read(ctx)
			if err != nil {
				return
			}
			var m wire.WSMessage
			if json.Unmarshal(data, &m) != nil {
				return
			}
			reply := func(m wire.WSMessage) {
				b, _ := json.Marshal(m)
				_ = c.Write(ctx, websocket.MessageText, b)
			}
			switch {
			case m.Type == wire.WSHello:
				reply(wire.WSMessage{Type: wire.WSResult, ID: m.ID, Status: http.StatusOK, Body: json.RawMessage(`{"v":1}`)})
			case m.Type == wire.WSSubscribe && n == 1:
				reply(wire.WSMessage{Type: wire.WSResult, ID: m.ID, Status: http.StatusOK, RunID: m.RunID})
				reply(wire.WSMessage{Type: wire.WSEvent, Sub: m.ID, Event: json.RawMessage(`{"v":1,"type":"run_start","id":"1","seq":1,"run_id":"run_1"}`)})
				reply(wire.WSMessage{Type: wire.WSGap, Sub: m.ID, After: "1"})
			case m.Type == wire.WSSubscribe && m.After == "1":
				reply(wire.WSMessage{Type: wire.WSResult, ID: m.ID, Status: http.StatusOK, RunID: m.RunID})
				reply(wire.WSMessage{Type: wire.WSEvent, Sub: m.ID, Event: json.RawMessage(`{"v":1,"type":"run_done","id":"2","seq":2,"run_id":"run_1","answer":"ok"}`)})
				reply(wire.WSMessage{Type: wire.WSEnd, Sub: m.ID})
			}
		}
	}))
	t.Cleanup(ts.Close)
	tr, err := wsclient.New(ts.URL, wsclient.WithFallback(ts.Client().Transport))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tr.Close() })
	c, err := agentclient.New(ts.URL, agentclient.WithHTTPClient(&http.Client{Transport: tr}))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	s, err := c.Events(ctx, "run_1", "")
	if err != nil {
		t.Fatal(err)
	}
	if e, err := s.Next(); err != nil || e.Type != wire.TypeRunStart {
		t.Fatalf("first event %+v, %v", e, err)
	}
	_, err = s.Next()
	var gap *wsclient.GapError
	if !errors.Is(err, wsclient.ErrGap) || !errors.As(err, &gap) || gap.After != "1" {
		t.Fatalf("after the gap: %v", err)
	}
	rest, err := c.Events(ctx, "run_1", gap.After)
	if err != nil {
		t.Fatalf("following again after the gap: %v", err)
	}
	if e, err := rest.Next(); err != nil || e.Type != wire.TypeRunDone || e.Answer != "ok" {
		t.Fatalf("after the gap: %+v, %v", e, err)
	}
	if n := conns.Load(); n != 2 {
		t.Errorf("%d connections, want 2", n)
	}
}
