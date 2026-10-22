package agenttools

// White-box: the in-process transport is unexported, and its streaming and
// cancellation are easier to pin down on a handler of the test's own than
// through the agent API.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/toolset"
)

// TestInProcessStreams: the response arrives once the handler sends its
// headers, the body as it is written, and closing the body cancels the
// handler's context and waits for it to return.
func TestInProcessStreams(t *testing.T) {
	t.Parallel()
	next := make(chan struct{})
	returned := make(chan struct{})
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(returned)
		if r.URL.Path != "/v1/x" || r.RequestURI != "/v1/x?a=1" || r.Header.Get("X-Test") != "yes" {
			t.Errorf("handler got %s %q %v", r.URL.Path, r.RequestURI, r.Header)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusAccepted)
		_ = http.NewResponseController(w).Flush()
		<-next
		_, _ = io.WriteString(w, "one\n")
		<-r.Context().Done()
	})
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://in-process/v1/x?a=1", nil)
	req.Header.Set("X-Test", "yes")
	resp, err := (&http.Client{Transport: inProcess{h}}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusAccepted || resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("response %d %v", resp.StatusCode, resp.Header)
	}
	close(next)
	line, err := bufio.NewReader(resp.Body).ReadString('\n')
	if err != nil || line != "one\n" {
		t.Fatalf("first line %q, %v", line, err)
	}
	select {
	case <-returned:
		t.Fatal("the handler returned before the body was closed")
	default:
	}
	_ = resp.Body.Close()
	select {
	case <-returned:
	default:
		t.Fatal("Close returned before the handler did")
	}
}

// TestInProcessEnds: a handler that returns ends the body; one that panics
// before answering is an error; a request whose context ends before the
// answer fails with it.
func TestInProcessEnds(t *testing.T) {
	t.Parallel()
	do := func(h http.HandlerFunc, ctx context.Context) (*http.Response, error) {
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "http://in-process/v1/runs", strings.NewReader(`{"message":"hi"}`))
		return (&http.Client{Transport: inProcess{h}}).Do(req)
	}
	resp, err := do(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_, _ = w.Write(b)
	}, t.Context())
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil || string(b) != `{"message":"hi"}` || resp.StatusCode != http.StatusOK {
		t.Errorf("echo: %d %q %v", resp.StatusCode, b, err)
	}
	_ = resp.Body.Close()

	if _, err := do(func(http.ResponseWriter, *http.Request) { panic("boom") }, t.Context()); err == nil || !strings.Contains(err.Error(), "panicked") {
		t.Errorf("a handler that panics: %v", err)
	}

	resp, err = do(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "part")
		panic("boom")
	}, t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if b, err := io.ReadAll(resp.Body); err == nil || string(b) != "part" {
		t.Errorf("a handler that panics midway: %q, %v: want the part and an error", b, err)
	}
	_ = resp.Body.Close()

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if _, err := do(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }, ctx); err == nil {
		t.Error("a request whose context ended before the answer: no error")
	}
}

// TestForwarding: the agent API gets the MCP request's Authorization
// header, and no other: not one the client of the API set itself.
func TestForwarding(t *testing.T) {
	t.Parallel()
	var got []string
	api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, strings.Join(r.Header.Values("Authorization"), ","))
	})
	for _, sent := range []string{"Bearer caller", ""} {
		mcp := ForwardCredentials(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			req, _ := http.NewRequestWithContext(r.Context(), http.MethodGet, "http://in-process/v1/agents", nil)
			req.Header.Set("Authorization", "Bearer the-client's-own")
			resp, err := (&http.Client{Transport: newForwarding(inProcess{api}, req.URL)}).Do(req)
			if err != nil {
				t.Fatal(err)
			}
			_ = resp.Body.Close()
		}))
		req := httptest.NewRequest(http.MethodPost, "/mcp/agentkit", nil)
		if sent != "" {
			req.Header.Set("Authorization", sent)
		}
		mcp.ServeHTTP(httptest.NewRecorder(), req)
	}
	if len(got) != 2 || got[0] != "Bearer caller" || got[1] != "" {
		t.Errorf("the API got %q: want the caller's header, then none", got)
	}
}

// TestForwardingStaysAtTheAPI: a toolset with a url sends the caller's
// credential to that origin only: a redirect to another host gets none.
func TestForwardingStaysAtTheAPI(t *testing.T) {
	t.Parallel()
	elsewhere := make(chan string, 1)
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		elsewhere <- r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"agents":[]}`)
	}))
	defer other.Close()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer caller" {
			t.Errorf("the API got %q", r.Header.Get("Authorization"))
		}
		http.Redirect(w, r, other.URL+r.URL.Path, http.StatusFound)
	}))
	defer api.Close()

	f, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	tools, err := f.Build(t.Context(), toolset.BuildInput{Settings: json.RawMessage(`{"url":"` + api.URL + `"}`)})
	if err != nil {
		t.Fatal(err)
	}
	h := ForwardCredentials(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = tools[0].Invoke(r.Context(), nil) // agents_list
	}))
	req := httptest.NewRequest(http.MethodPost, "/mcp/agentkit", nil)
	req.Header.Set("Authorization", "Bearer caller")
	h.ServeHTTP(httptest.NewRecorder(), req)
	select {
	case got := <-elsewhere:
		if got != "" {
			t.Errorf("the redirect's target got Authorization %q: want none", got)
		}
	default:
		t.Fatal("the redirect was not followed: the test checks nothing")
	}
}

// TestAgentRunLeavesWithTheCaller: while the API has not answered the run's
// request, the caller leaving (or its call timeout) ends agent_run, which
// does not wait on the detached request.
func TestAgentRunLeavesWithTheCaller(t *testing.T) {
	t.Parallel()
	started, gone := make(chan struct{}), make(chan struct{})
	stuck := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
		close(gone)
	})
	f, err := New(WithHandler(stuck))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	tools, err := f.Build(t.Context(), toolset.BuildInput{Settings: nil})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	errc := make(chan error, 1)
	go func() {
		_, err := tools[1].Invoke(ctx, json.RawMessage(`{"message":"hi"}`)) // agent_run
		errc <- err
	}()
	<-started
	cancel()
	select {
	case err := <-errc:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("agent_run = %v: want context.Canceled", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("agent_run still waits for the API after its caller left")
	}
	select {
	case <-gone:
	case <-time.After(10 * time.Second):
		t.Fatal("the API's request was not canceled")
	}
}
