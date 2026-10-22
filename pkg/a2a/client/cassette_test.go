package client_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/a2a/a2atest"
	"github.com/bsamanvith/agentkit/pkg/a2a/client"
	"github.com/bsamanvith/agentkit/pkg/toolset/toolsettest"
)

// The cassettes of testdata/cassettes are a remote agent's exchanges
// (T049's kit): ask_helper asking agentkit's own A2A server "hello",
// recorded by TestRecordCassettes and replayed by TestReplayedRemoteAgent
// through the kit's hermetic upstream. Request headers are never stored.

const cassettes = "testdata/cassettes"

// fixedIDs makes the message IDs a replay must send again.
func fixedIDs() func() string {
	var mu sync.Mutex
	n := 0
	return func() string {
		mu.Lock()
		defer mu.Unlock()
		n++
		return fmt.Sprintf("msg-%d", n)
	}
}

// TestReplayedRemoteAgent: the tool answers from a remote agent's recorded
// exchanges, over a client that reaches nothing else, sending its key in
// the Authorization header only.
func TestReplayedRemoteAgent(t *testing.T) {
	t.Parallel()
	up := toolsettest.NewUpstream(t, cassettes)
	// The card names the upstream's own address, so it is scripted.
	c := cardAt(up.URL() + "/a2a/helper")
	c.Capabilities.Streaming = true // as the recorded agent's
	card, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	up.Script(toolsettest.Response{Header: http.Header{"Content-Type": {"application/json"}}, Body: card})
	tools, err := client.Factory(client.WithLookupEnv(env), client.WithHTTPClient(up.Client()), client.WithNewID(fixedIDs())).
		Build(t.Context(), json.RawMessage(`{"agents": [{"name": "helper", "card_url": "`+up.URL()+`/card", "token_env": "`+keyEnv+`"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	out, err := ask(t.Context(), t, tools[0], `{"message": "hello"}`)
	if err != nil {
		t.Fatal(err)
	}
	if out.State != "completed" || out.Answer != "echo: hello" {
		t.Errorf("output %+v, want the recorded answer", out)
	}
	reqs := up.Requests()
	if len(reqs) < 2 {
		t.Fatalf("%d requests, want the card and the task's", len(reqs))
	}
	for _, r := range reqs {
		if r.Header.Get("Authorization") != "Bearer "+a2atest.Key {
			t.Errorf("%s %s: no bearer key", r.Method, r.Path)
		}
		if bytes.Contains(r.Body, []byte(a2atest.Key)) {
			t.Errorf("%s %s: the key is in the body", r.Method, r.Path)
		}
	}
}

// TestRecordCassettes rewrites testdata/cassettes from agentkit's own A2A
// server in process (make record-cassettes sets the variable).
func TestRecordCassettes(t *testing.T) {
	toolsettest.SkipUnlessRecording(t)
	s := a2atest.NewServer(t)
	if err := os.RemoveAll(cassettes); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(cassettes, 0o755); err != nil {
		t.Fatal(err)
	}
	rec := &recorder{t: t, next: s.Client().Transport}
	tools, err := client.Factory(client.WithLookupEnv(env), client.WithHTTPClient(&http.Client{Transport: rec}), client.WithNewID(fixedIDs())).
		Build(t.Context(), json.RawMessage(`{"agents": [{"name": "helper", "card_url": "`+s.CardURL()+`", "token_env": "`+keyEnv+`"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	rec.on = true
	if _, err := ask(t.Context(), t, tools[0], `{"message": "hello"}`); err != nil {
		t.Fatal(err)
	}
}

// recorder writes each exchange after the card as a cassette.
type recorder struct {
	t    *testing.T
	next http.RoundTripper
	on   bool
	mu   sync.Mutex
	n    int
}

func (r *recorder) RoundTrip(req *http.Request) (*http.Response, error) {
	var body []byte
	if req.Body != nil {
		body, _ = io.ReadAll(req.Body)
		req.Body = io.NopCloser(bytes.NewReader(body))
	}
	resp, err := r.next.RoundTrip(req)
	if err != nil || !r.on {
		return resp, err
	}
	res, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(res))
	r.mu.Lock()
	r.n++
	name := fmt.Sprintf("%02d-%s", r.n, map[bool]string{true: "post", false: "get"}[req.Method == http.MethodPost])
	r.mu.Unlock()
	cr := toolsettest.CassetteRequest{Method: req.Method, Path: req.URL.Path, RecordedAt: time.Now().UTC().Format("2006-01-02")}
	if len(body) > 0 {
		cr.Body = body
	}
	rb, _ := json.MarshalIndent(cr, "", "  ")
	httpRes := fmt.Sprintf("HTTP/1.1 %s\r\nContent-Type: %s\r\n\r\n%s", resp.Status, resp.Header.Get("Content-Type"), res)
	for file, data := range map[string][]byte{name + ".req.json": append(rb, '\n'), name + ".res.http": []byte(httpRes)} {
		if err := os.WriteFile(filepath.Join(cassettes, file), data, 0o644); err != nil {
			r.t.Fatal(err)
		}
	}
	return resp, nil
}
