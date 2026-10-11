package main

// End-to-end tests of the agentkit binary: it is built, run as separate OS
// processes, and talks to a local server that replays Claude responses
// recorded from the real API (testdata/e2e/<scenario>/NN.http), so the tests
// need no network and no key.
//
// To record the fixtures again against the real API (after a change to the
// prompts, the tools, the adapter, or to pick up a new model):
//
//	AGENTKIT_E2E_RECORD=1 ANTHROPIC_API_KEY=... go test -count=1 -run TestE2E ./cmd/agentkit/
//
// The server then forwards each request to the API with that key (the binary
// itself only ever gets a dummy key) and writes the responses as fixtures.
// Review the diff: the fixtures keep only the status, Content-Type and the
// event stream, never account headers.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	recordEnv = "AGENTKIT_E2E_RECORD"
	upstream  = "https://api.anthropic.com"
)

// Build information the test binary is stamped with.
const (
	e2eVersion = "1.2.3-e2e"
	e2eCommit  = "e2ecommit"
	e2eDate    = "2026-10-11T00:00:00Z"
)

var (
	buildOnce sync.Once
	binPath   string
	errBuild  error
)

// binary builds the agentkit binary once per test process.
func binary(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("builds and runs the binary")
	}
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "agentkit-e2e-")
		if err != nil {
			errBuild = err
			return
		}
		binPath = filepath.Join(dir, "agentkit")
		ldflags := fmt.Sprintf("-X main.version=%s -X main.commit=%s -X main.date=%s", e2eVersion, e2eCommit, e2eDate)
		out, err := exec.Command("go", "build", "-ldflags", ldflags, "-o", binPath, ".").CombinedOutput()
		if err != nil {
			errBuild = fmt.Errorf("go build: %w\n%s", err, out)
		}
	})
	if errBuild != nil {
		t.Fatal(errBuild)
	}
	return binPath
}

// request is one model request the server received, and which process sent
// it.
type request struct {
	proc string
	body []byte
}

// replayAPI stands in for the Claude API: it serves the scenario's fixtures in
// order (or, when recording, forwards to the real API and writes them), and
// keeps every request it received.
type replayAPI struct {
	t      *testing.T
	dir    string
	record bool
	key    string

	mu      sync.Mutex
	proc    string // the process now running, for the requests it sends
	served  int    // fixtures served (or recorded) so far
	reqs    []request
	stallAt int           // the request (counting from 1) to hold until its client gives up; 0 for none
	stalled chan struct{} // closed when that request arrives
	holdAt  int           // the request (counting from 1) to hold until release is closed; 0 for none
	held    chan struct{} // closed when that request arrives
	release chan struct{}
}

func newReplayAPI(t *testing.T, scenario string) (*replayAPI, *httptest.Server) {
	t.Helper()
	binary(t) // build, or skip under -short, before anything is set up
	c := &replayAPI{
		t: t, dir: filepath.Join("testdata", "e2e", scenario),
		stalled: make(chan struct{}), held: make(chan struct{}), release: make(chan struct{}),
	}
	if os.Getenv(recordEnv) != "" {
		c.record = true
		c.key = os.Getenv("ANTHROPIC_API_KEY")
		if c.key == "" {
			t.Fatalf("%s needs ANTHROPIC_API_KEY", recordEnv)
		}
		old, _ := filepath.Glob(filepath.Join(c.dir, "*.http"))
		for _, f := range old {
			if err := os.Remove(f); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.MkdirAll(c.dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	srv := httptest.NewServer(c)
	t.Cleanup(srv.Close)
	t.Cleanup(func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		if left, _ := filepath.Glob(filepath.Join(c.dir, "*.http")); !c.record && c.served != len(left) {
			t.Errorf("served %d of %d fixtures in %s: the binary made a different number of model calls (re-record?)", c.served, len(left), c.dir)
		}
	})
	return c, srv
}

func (c *replayAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	c.mu.Lock()
	c.reqs = append(c.reqs, request{proc: c.proc, body: body})
	if len(c.reqs) == c.stallAt {
		c.mu.Unlock()
		close(c.stalled)
		<-r.Context().Done() // the process is killed while it waits for the model
		return
	}
	hold := len(c.reqs) == c.holdAt
	c.served++
	n := c.served
	c.mu.Unlock()
	if hold { // answered once the test lets it go
		close(c.held)
		select {
		case <-c.release:
		case <-r.Context().Done():
			return
		}
	}

	file := filepath.Join(c.dir, fmt.Sprintf("%02d.http", n))
	var raw []byte
	if c.record {
		raw, err = c.forward(r, body)
		if err == nil {
			err = os.WriteFile(file, raw, 0o644)
		}
	} else {
		raw, err = os.ReadFile(file)
	}
	if err != nil {
		c.t.Errorf("model request %d: %v (re-record with %s=1)", n, err, recordEnv)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest) // not retried by the SDK
		fmt.Fprint(w, `{"type":"error","error":{"type":"invalid_request_error","message":"no fixture"}}`)
		return
	}
	resp, err := http.ReadResponse(bufio.NewReader(bytes.NewReader(raw)), r)
	if err != nil {
		c.t.Errorf("fixture %s: %v", file, err)
		return
	}
	defer resp.Body.Close()
	for k, vs := range resp.Header {
		w.Header()[k] = vs
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// forward sends the request to the real API with the real key and returns
// the response in fixture form: status line, Content-Type, then the body.
func (c *replayAPI) forward(r *http.Request, body []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(r.Context(), r.Method, upstream+r.URL.RequestURI(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	for k, vs := range r.Header {
		switch http.CanonicalHeaderKey(k) {
		case "X-Api-Key", "Authorization", "Accept-Encoding", "Content-Length", "Host":
		default:
			req.Header[k] = vs
		}
	}
	req.Header.Set("X-Api-Key", c.key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	fmt.Fprintf(&b, "HTTP/1.1 %s\r\nContent-Type: %s\r\nRequest-Id: req_replay\r\n\r\n", resp.Status, resp.Header.Get("Content-Type"))
	b.Write(got)
	return b.Bytes(), nil
}

func (c *replayAPI) requests(proc string) [][]byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out [][]byte
	for _, r := range c.reqs {
		if r.proc == proc {
			out = append(out, r.body)
		}
	}
	return out
}

func (c *replayAPI) all() []request {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]request(nil), c.reqs...)
}

// command returns the binary as process proc, pointed at the server with a
// dummy key and an empty home (no credentials of the machine leak in).
func (c *replayAPI) command(t *testing.T, srv *httptest.Server, proc string, args ...string) (*exec.Cmd, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	c.mu.Lock()
	c.proc = proc
	c.mu.Unlock()
	cmd := exec.Command(binary(t), args...)
	cmd.Env = []string{
		"HOME=" + t.TempDir(),
		"PATH=" + os.Getenv("PATH"),
		"ANTHROPIC_BASE_URL=" + srv.URL,
		"ANTHROPIC_API_KEY=e2e-dummy-key",
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	return cmd, &stdout, &stderr
}

// run runs the binary as process proc to its end and returns its output and
// exit code.
func (c *replayAPI) run(t *testing.T, srv *httptest.Server, proc string, args ...string) (string, int) {
	t.Helper()
	cmd, stdout, stderr := c.command(t, srv, proc, args...)
	err := cmd.Run()
	t.Logf("process %s: agentkit %q\nstdout:\n%s\nstderr:\n%s", proc, args, stdout, stderr)
	var exit *exec.ExitError
	if err != nil && !errors.As(err, &exit) {
		t.Fatal(err)
	}
	return stdout.String() + stderr.String(), cmd.ProcessState.ExitCode()
}

func TestE2ERun(t *testing.T) {
	t.Parallel()
	c, srv := newReplayAPI(t, "run")
	out, code := c.run(t, srv, "run", "run", "-effort", "low",
		"Use the calculate tool to multiply 48271 by 9733, then tell me the result.")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	mustContain(t, out, "→ calculate", `{"result":469821643}`, "· turn 1: tool_use", ": end_turn", "· done: 2 turns")
	checkConversation(t, c.all())
}

// TestE2EFlowKilledResumedApproved: process A starts the demo flow and is
// killed (SIGINT) while waiting for the model after research turn 1; process
// B resumes the research where A stopped and waits at the approval; process
// C approves and writes the draft.
func TestE2EFlowKilledResumedApproved(t *testing.T) {
	t.Parallel()
	c, srv := newReplayAPI(t, "flow")
	state := filepath.Join(t.TempDir(), "demo.json")
	c.stallAt = 2

	cmd, stdout, stderr := c.command(t, srv, "A", "flow", "demo", "-state", state,
		"What is today's date, and what is 1234 times 5678? Use a tool for each, one per turn.")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-c.stalled:
	case <-time.After(2 * time.Minute):
		_ = cmd.Process.Kill()
		t.Fatalf("process A never made its second model call\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
	}
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	t.Logf("process A (killed)\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
	if code := cmd.ProcessState.ExitCode(); code != 1 {
		t.Errorf("process A exit %d, want 1", code)
	}
	if got := saved(t, state); got.status != "running" || got.messages["demo/research"] != 3 {
		t.Fatalf("after the kill: %+v, want status running and 3 research messages (prompt, reply, tool results)", got)
	}

	out, code := c.run(t, srv, "B", "flow", "demo", "-state", state)
	if code != 0 {
		t.Fatalf("process B exit %d", code)
	}
	mustContain(t, out, "── demo/research ──", "Waiting for approval at demo/approve")
	a, b := c.requests("A"), c.requests("B")
	if len(a) != 2 || len(b) == 0 {
		t.Fatalf("requests: A %d, B %d", len(a), len(b))
	}
	if !bytes.Equal(b[0], a[1]) {
		t.Errorf("B's first request differs from the one A was killed waiting for:\nA: %s\nB: %s", a[1], b[0])
	}
	for _, r := range b {
		if bytes.Equal(r, a[0]) {
			t.Error("B asked research turn 1 again")
		}
	}
	if got := saved(t, state); got.status != "suspended" {
		t.Errorf("after B: %+v, want suspended", got)
	}

	out, code = c.run(t, srv, "C", "flow", "demo", "-state", state, "-approve")
	if code != 0 {
		t.Fatalf("process C exit %d", code)
	}
	mustContain(t, out, "── demo/write ──", "=== draft ===")
	cr := c.requests("C")
	if len(cr) == 0 || !bytes.Contains(cr[0], []byte("Research:")) {
		t.Errorf("C's first request is not the writer's brief: %s", cr)
	}
	if strings.Contains(out, "── demo/research ──") {
		t.Error("C ran the research again")
	}
	got := saved(t, state)
	if got.status != "completed" || got.messages["demo/write"] < 2 {
		t.Errorf("after C: %+v, want completed with the writer's transcript", got)
	}
	checkConversation(t, c.all())
}

// savedState is what the demo's state file says.
type savedState struct {
	status   string
	messages map[string]int // step path → saved messages
}

func saved(t *testing.T, path string) savedState {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Checkpoints map[string]struct {
			Status string `json:"status"`
		} `json:"checkpoints"`
		Transcripts map[string]map[string][]json.RawMessage `json:"transcripts"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	s := savedState{status: f.Checkpoints["demo"].Status, messages: map[string]int{}}
	for p, msgs := range f.Transcripts["demo"] {
		s.messages[p] = len(msgs)
	}
	return s
}

// checkConversation checks what must hold for every request the binary sent,
// across processes: each agent's request carries the previous one of the
// same agent as an exact prefix, bytes included (thinking blocks come back as
// they were received, from memory or from the state file), and ends with a
// user message.
func checkConversation(t *testing.T, reqs []request) {
	t.Helper()
	last := map[string][]json.RawMessage{} // system prompt → messages of its last request
	for i, r := range reqs {
		var body struct {
			System   json.RawMessage   `json:"system"`
			Messages []json.RawMessage `json:"messages"`
		}
		if err := json.Unmarshal(r.body, &body); err != nil {
			t.Fatalf("request %d: %v", i+1, err)
		}
		var lastMsg struct {
			Role string `json:"role"`
		}
		if n := len(body.Messages); n == 0 || json.Unmarshal(body.Messages[n-1], &lastMsg) != nil || lastMsg.Role != "user" {
			t.Errorf("request %d (process %s) does not end with a user message", i+1, r.proc)
		}
		agent := string(body.System)
		prev := last[agent]
		if len(prev) > len(body.Messages) {
			t.Errorf("request %d (process %s) is shorter than the agent's previous one", i+1, r.proc)
		} else {
			for j := range prev {
				if !bytes.Equal(prev[j], body.Messages[j]) {
					t.Errorf("request %d (process %s): message %d changed since the agent's previous request", i+1, r.proc, j)
				}
			}
		}
		last[agent] = body.Messages
	}
}
