package main

// End-to-end test of agentkit serve: the built binary as a separate process,
// two demo toolsets and one optional toolset that fails, driven with the MCP
// SDK client and plain HTTP, then stopped with SIGTERM while a call is in
// flight. It needs no network beyond loopback and no recorded fixtures.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/bsamanvith/agentkit/internal/testkit"
)

const serveConfigJSON = `{
  "mcp": {
    "base_path": "/mcp",
    "call_timeout": "20s",
    "max_in_flight": 64,
    "toolsets": [
      {"name": "demo-a", "type": "demo", "settings": {"echo_prefix": "[a] "}},
      {"name": "demo-b", "type": "demo", "settings": {"echo_prefix": "[b] "}, "exclude": ["current_time"]},
      {"name": "broken", "type": "demo", "settings": {"utc_offset_minutes": 9999}, "optional": true}
    ]
  }
}`

// serveProcess is a running agentkit serve.
type serveProcess struct {
	cmd  *exec.Cmd
	url  string
	addr string
	mu   sync.Mutex
	logs bytes.Buffer
	done chan struct{} // closed when the process has exited
}

// startServe starts agentkit serve on a free loopback port and waits until it
// listens. It learns the port from the "listening" record the server logs.
func startServe(t *testing.T, config string, shutdownTimeout time.Duration) *serveProcess {
	t.Helper()
	path := filepath.Join(t.TempDir(), "agentkit.json")
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	p := &serveProcess{cmd: exec.Command(binary(t), "serve", "-config", path), done: make(chan struct{})}
	p.cmd.Env = []string{
		"HOME=" + t.TempDir(),
		"PATH=" + os.Getenv("PATH"),
		envComponents + "=mcp-server",
		envAddr + "=127.0.0.1:0",
		envLogLevel + "=debug",
		envShutdownTimeout + "=" + shutdownTimeout.String(),
	}
	stderr, err := p.cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := p.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	listening := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			p.mu.Lock()
			p.logs.Write(sc.Bytes())
			p.logs.WriteByte('\n')
			p.mu.Unlock()
			var rec struct{ Msg, Addr string }
			if json.Unmarshal(sc.Bytes(), &rec) == nil && rec.Msg == "listening" {
				listening <- rec.Addr
			}
		}
		_ = p.cmd.Wait()
		close(p.done)
	}()
	t.Cleanup(func() {
		_ = p.cmd.Process.Kill()
		<-p.done
		p.mu.Lock()
		defer p.mu.Unlock()
		t.Logf("agentkit serve log:\n%s", &p.logs)
	})
	select {
	case p.addr = <-listening:
	case <-p.done:
		t.Fatalf("agentkit serve exited before listening: exit %d", p.cmd.ProcessState.ExitCode())
	case <-time.After(time.Minute):
		t.Fatal("agentkit serve did not listen within a minute")
	}
	p.url = "http://" + p.addr
	return p
}

func (p *serveProcess) getJSON(t *testing.T, path string, v any) int {
	t.Helper()
	resp, err := http.Get(p.url + path) //nolint:noctx // test against a local server
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(body, v); err != nil {
		t.Fatalf("GET %s: %q: %v", path, body, err)
	}
	return resp.StatusCode
}

// connectMCP opens an MCP client session to one toolset path.
func connectMCP(t *testing.T, url string, client *http.Client) *mcp.ClientSession {
	t.Helper()
	c := mcp.NewClient(&mcp.Implementation{Name: "e2e", Version: "0"}, nil)
	cs, err := c.Connect(t.Context(), &mcp.StreamableClientTransport{
		Endpoint: url, HTTPClient: client, DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatalf("connect %s: %v", url, err)
	}
	return cs
}

func echo(t *testing.T, cs *mcp.ClientSession, text string) (string, error) {
	t.Helper()
	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "echo", Arguments: map[string]any{"text": text}})
	if err != nil {
		return "", err
	}
	sc, _ := res.StructuredContent.(map[string]any)
	got, _ := sc["text"].(string)
	if res.IsError {
		return "", &mcpToolError{got}
	}
	return got, nil
}

type mcpToolError struct{ text string }

func (e *mcpToolError) Error() string { return "tool error: " + e.text }

// gate holds back the body of the first tools/call request it sees until
// released. The request asks for 100-continue, which the server sends once a
// handler starts reading the body: from then on (sent is closed) the call is
// in flight on the server for as long as the test wants. (A request the
// server has not accepted yet, or not yet read from an idle keep-alive
// connection, can be lost when it shuts down: that race is HTTP/1.1's, and
// not what this test is about.)
type gate struct {
	claimed   sync.Once
	sent      chan struct{} // closed when the server asks for the body
	release   chan struct{}
	transport *http.Transport
}

func newGate() *gate {
	return &gate{
		sent: make(chan struct{}), release: make(chan struct{}),
		transport: &http.Transport{ExpectContinueTimeout: time.Minute},
	}
}

func (g *gate) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Body == nil {
		return g.transport.RoundTrip(req)
	}
	body, err := io.ReadAll(req.Body)
	_ = req.Body.Close()
	if err != nil {
		return nil, err
	}
	req = req.Clone(req.Context())
	req.Body = io.NopCloser(bytes.NewReader(body))
	gated := false
	if bytes.Contains(body, []byte(`"tools/call"`)) {
		g.claimed.Do(func() { gated = true })
	}
	if gated {
		req = req.WithContext(httptrace.WithClientTrace(req.Context(), &httptrace.ClientTrace{
			Got100Continue: func() { close(g.sent) },
		}))
		req.Header.Set("Expect", "100-continue")
		req.Body = io.NopCloser(&heldReader{release: g.release, rest: bytes.NewReader(body)})
		req.GetBody = nil
	}
	return g.transport.RoundTrip(req)
}

// heldReader blocks its first Read until release is closed.
type heldReader struct {
	release chan struct{}
	once    sync.Once
	rest    io.Reader
}

func (h *heldReader) Read(p []byte) (int, error) {
	h.once.Do(func() { <-h.release })
	return h.rest.Read(p)
}

// TestE2EServe runs agentkit serve as its own process: two toolsets on two
// paths, /healthz, /readyz (with the skipped optional toolset), /version from
// -ldflags, then SIGTERM while a tool call is in flight: the call completes
// and the process exits 0 within the shutdown deadline.
func TestE2EServe(t *testing.T) {
	t.Parallel()
	const deadline = 10 * time.Second
	p := startServe(t, serveConfigJSON, deadline)

	var health struct{ Status string }
	if code := p.getJSON(t, "/healthz", &health); code != http.StatusOK || health.Status != "ok" {
		t.Errorf("/healthz = %d %+v", code, health)
	}
	var version struct{ Version, Commit, Date string }
	if code := p.getJSON(t, "/version", &version); code != http.StatusOK ||
		version.Version != e2eVersion || version.Commit != e2eCommit || version.Date != e2eDate {
		t.Errorf("/version = %d %+v, want the -ldflags values", code, version)
	}
	// Only the listed component runs; it is ready, degraded by the skipped
	// optional toolset, which /readyz names.
	var ready struct {
		Status     string
		Components []struct{ Name, Status, Reason string }
	}
	code := p.getJSON(t, "/readyz", &ready)
	if code != http.StatusOK || ready.Status != "ready" || len(ready.Components) != 1 ||
		ready.Components[0].Name != "mcp-server" || ready.Components[0].Status != "degraded" ||
		!strings.Contains(ready.Components[0].Reason, `"broken"`) {
		t.Errorf("/readyz = %d %+v, want 200 with mcp-server degraded by toolset \"broken\"", code, ready)
	}

	// Each path serves its own toolset.
	for name, want := range map[string][]string{
		"demo-a": {"calculate", "current_time", "echo"},
		"demo-b": {"calculate", "echo"},
	} {
		cs := connectMCP(t, p.url+"/mcp/"+name, nil)
		res, err := cs.ListTools(t.Context(), nil)
		if err != nil {
			t.Fatalf("%s: ListTools: %v", name, err)
		}
		var got []string
		for _, tl := range res.Tools {
			got = append(got, tl.Name)
		}
		slices.Sort(got)
		if !slices.Equal(got, want) {
			t.Errorf("/mcp/%s tools = %v, want %v", name, got, want)
		}
		prefix := "[" + strings.TrimPrefix(name, "demo-") + "] "
		if got, err := echo(t, cs, "hi"); err != nil || got != prefix+"hi" {
			t.Errorf("/mcp/%s echo = %q %v, want %q", name, got, err, prefix+"hi")
		}
		_ = cs.Close()
	}
	var listing struct{ Toolsets []struct{ Name string } }
	if code := p.getJSON(t, "/mcp/", &listing); code != http.StatusOK || len(listing.Toolsets) != 2 {
		t.Errorf("GET /mcp/ = %d %+v, want the two built toolsets", code, listing)
	}

	// SIGTERM with a call in flight.
	g := newGate()
	cs := connectMCP(t, p.url+"/mcp/demo-a", &http.Client{Transport: g})
	type result struct {
		text string
		err  error
	}
	resc := make(chan result, 1)
	go func() {
		text, err := echo(t, cs, "in flight")
		resc <- result{text, err}
	}()
	<-g.sent
	if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	signalled := time.Now()
	// The shutdown has begun once the listener refuses new connections.
	testkit.Eventually(t, deadline, func() bool {
		conn, err := net.Dial("tcp", p.addr)
		if err == nil {
			_ = conn.Close()
		}
		return err != nil
	})
	close(g.release)
	if r := <-resc; r.err != nil || r.text != "[a] in flight" {
		t.Errorf("in-flight call = %q %v, want it to complete", r.text, r.err)
	}
	select {
	case <-p.done:
	case <-time.After(deadline + 5*time.Second): // only a broken shutdown gets here
		t.Fatalf("agentkit serve still running %s after SIGTERM", time.Since(signalled))
	}
	if code := p.cmd.ProcessState.ExitCode(); code != 0 {
		t.Errorf("exit %d after SIGTERM, want 0", code)
	}
	if took := time.Since(signalled); took > deadline {
		t.Errorf("exited %s after SIGTERM, past the %s deadline", took, deadline)
	}
}
