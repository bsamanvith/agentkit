package main

// Tests of agentkit chat and ask in process, against the real agent API
// (pkg/agentapi) with a scripted model: what the replay e2e tests
// (cli_e2e_test.go) do not reach cheaply.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"iter"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/agentclient"
	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/llm/llmtest"
)

const testToken = "test-token-canary-7f3a9c"

// newTestAPI serves the agent API for model, with the demo tools, behind
// wrap (nil: none), and returns its URL.
func newTestAPI(t *testing.T, model llm.Model, wrap func(http.Handler) http.Handler, opts ...agentapi.Option) string {
	t.Helper()
	tools, err := cli{}.demoTools(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	h, err := harness.New(model, tools)
	if err != nil {
		t.Fatal(err)
	}
	srv, err := agentapi.New(h, append([]agentapi.Option{agentapi.WithToken(testToken)}, opts...)...)
	if err != nil {
		t.Fatal(err)
	}
	handler := srv.Handler()
	if wrap != nil {
		handler = wrap(handler)
	}
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	return ts.URL
}

// syncBuffer is an output written by chat while the test reads it; seen is
// closed once it holds want.
type syncBuffer struct {
	mu   sync.Mutex
	buf  bytes.Buffer
	want string
	seen chan struct{}
}

func newSyncBuffer(want string) *syncBuffer {
	return &syncBuffer{want: want, seen: make(chan struct{})}
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n, err := b.buf.Write(p)
	if b.want != "" && strings.Contains(b.buf.String(), b.want) {
		close(b.seen)
		b.want = ""
	}
	return n, err
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// clientCLI is the command line of a client of the server at url, with the
// token, stdin and Ctrl-C channel given.
func clientCLI(url string, stdout io.Writer, stdin io.Reader, interrupts <-chan os.Signal) (cli, *syncBuffer) {
	stderr := newSyncBuffer("")
	env := map[string]string{envServer: url, envAPIToken: testToken}
	return cli{
		stdout: stdout, stderr: stderr, getenv: func(k string) string { return env[k] },
		term: clientTerm{stdin: stdin, interrupts: interrupts},
	}, stderr
}

// checkClientOutput checks what holds for every output not on a terminal: no
// escape sequence, no token.
func checkClientOutput(t *testing.T, out string) {
	t.Helper()
	if strings.Contains(out, "\x1b") {
		t.Errorf("output not on a terminal has an escape sequence: %q", out)
	}
	if strings.Contains(out, testToken) {
		t.Error("output holds the API token")
	}
}

// withUnknownBlock adds a block of a type this client does not know (from a
// newer server) to the last message of every conversation it returns.
func withUnknownBlock(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || !strings.HasPrefix(r.URL.Path, "/v1/conversations/") {
			next.ServeHTTP(w, r)
			return
		}
		rec := httptest.NewRecorder()
		next.ServeHTTP(rec, r)
		var conv map[string]any
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &conv) != nil {
			w.WriteHeader(rec.Code)
			_, _ = w.Write(rec.Body.Bytes())
			return
		}
		msgs, _ := conv["messages"].([]any)
		last, _ := msgs[len(msgs)-1].(map[string]any)
		m, _ := last["message"].(map[string]any)
		m["content"] = append(m["content"].([]any), map[string]any{"type": "image", "source": map[string]any{"type": "url", "url": "https://example.com/a.png"}})
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(conv)
	})
}

// TestChatCommands: a run with a tool call, then /details (the call and its
// result in full), /history (with a placeholder for a block of an unknown
// type), /conv and an unknown command, all as plain text when stdout is not
// a terminal.
func TestChatCommands(t *testing.T) {
	t.Parallel()
	long := strings.TrimSpace(strings.Repeat("pineapple ", 30))
	input, _ := json.Marshal(map[string]string{"text": long})
	model := llmtest.NewFake(t,
		llmtest.ToolCalls(llm.ToolUse{ID: "toolu_1", Name: "echo", Input: input}),
		llmtest.Text("It said pineapple, thirty times."),
	)
	url := newTestAPI(t, model, withUnknownBlock)
	out := newSyncBuffer("")
	c, stderr := clientCLI(url, out, strings.NewReader("Echo pineapple thirty times.\n/details\n/history\n/conv\n/bogus\n"), make(chan os.Signal))

	if code := c.main(t.Context(), []string{"chat"}); code != exitDone {
		t.Fatalf("exit %d\n%s%s", code, out, stderr)
	}
	got := out.String()
	t.Logf("output:\n%s", got)
	checkClientOutput(t, got+stderr.String())
	runPart, rest, _ := strings.Cut(got, "echo (call toolu_1)")
	mustContain(t, runPart, "→ echo", "← ", "…", "It said pineapple, thirty times.", "2 turns")
	if strings.Contains(runPart, long) {
		t.Error("the run shows the whole tool result; want one line, cut")
	}
	mustContain(t, rest, `{"text":"`+long+`"}`, // /details: the result in full
		"you: Echo pineapple thirty times.", "agent: It said pineapple, thirty times.", "[image]", // /history
		"conv_", "unknown command /bogus")
}

// TestChatCtrlCAtPromptQuits: Ctrl-C with no run going on ends chat.
func TestChatCtrlCAtPromptQuits(t *testing.T) {
	t.Parallel()
	in, w := io.Pipe()
	t.Cleanup(func() { _ = w.Close() })
	interrupts := make(chan os.Signal, 1)
	interrupts <- os.Interrupt
	out := newSyncBuffer("")
	c, _ := clientCLI("http://127.0.0.1:1", out, in, interrupts)
	code := make(chan int, 1)
	go func() { code <- c.main(context.WithoutCancel(t.Context()), []string{"chat"}) }()
	select {
	case got := <-code:
		if got != exitInterrupted {
			t.Errorf("exit %d, want %d", got, exitInterrupted)
		}
	case <-time.After(time.Minute): // only a chat that ignores Ctrl-C gets here
		t.Fatal("chat still waits for input after Ctrl-C at the prompt")
	}
}

// slowCancel is a model that, once cancelled, takes until ended is closed to
// stop: the server's run stays "running" for that long after Ctrl-C.
type slowCancel struct {
	llm.Model
	ended chan struct{}
}

func (m slowCancel) Generate(ctx context.Context, req llm.Request) iter.Seq2[llm.Event, error] {
	inner := m.Model.Generate(ctx, req)
	return func(yield func(llm.Event, error) bool) {
		for e, err := range inner {
			if err != nil && ctx.Err() != nil {
				<-m.ended
			}
			if !yield(e, err) {
				return
			}
		}
	}
}

// TestChatCtrlCCancelsRun: Ctrl-C while the answer streams cancels the run on
// the server; chat returns to the prompt once the server has ended the run,
// and an empty line then resumes it, sending the model the same conversation
// again.
func TestChatCtrlCCancelsRun(t *testing.T) {
	t.Parallel()
	stall := llmtest.Text("Let me think")
	stall.Stall = true
	model := slowCancel{Model: llmtest.NewFake(t, stall, llmtest.Text("ready")), ended: make(chan struct{})}
	// The cancelled run ends only after chat has asked for the
	// conversation's status (and seen it running): a chat that does not wait
	// would find the conversation busy when it resumes.
	var asked sync.Once
	url := newTestAPI(t, model, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r)
			if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/conversations/") {
				asked.Do(func() { close(model.ended) })
			}
		})
	})
	t.Cleanup(func() { asked.Do(func() { close(model.ended) }) }) // before the server closes
	in, w := io.Pipe()
	interrupts := make(chan os.Signal, 1)
	out := newSyncBuffer("Let me") // the first chunk the fake streams before it stalls
	c, stderr := clientCLI(url, out, in, interrupts)

	code := make(chan int, 1)
	go func() { code <- c.main(context.WithoutCancel(t.Context()), []string{"chat"}) }()
	if _, err := io.WriteString(w, "Reply with just the word: ready\n"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-out.seen: // the answer is streaming
	case <-time.After(time.Minute):
		t.Fatalf("the answer never started:\n%s", out)
	}
	interrupts <- os.Interrupt
	// chat reads the next line only once the cancelled run is settled.
	if _, err := io.WriteString(w, "\n"); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	if got := <-code; got != exitDone {
		t.Fatalf("exit %d\n%s%s", got, out, stderr)
	}
	got := out.String()
	t.Logf("output:\n%s", got)
	checkClientOutput(t, got+stderr.String())
	_, resumed, ok := strings.Cut(got, "cancelled before it answered")
	if !ok || !strings.Contains(resumed, "ready") {
		t.Errorf("want the cancel, then the resumed answer")
	}
	reqs := model.Model.(*llmtest.Fake).Requests()
	if len(reqs) != 2 || len(reqs[1].Messages) != 1 || reqs[1].Messages[0].Text() != "Reply with just the word: ready" {
		t.Errorf("model requests %+v; want the resumed run to send the message again, alone", reqs)
	}
	client, err := agentclient.New(url, agentclient.WithToken(testToken))
	if err != nil {
		t.Fatal(err)
	}
	page, err := client.Conversations(t.Context(), agentclient.ConversationsQuery{})
	if err != nil || len(page.Conversations) != 1 || page.Conversations[0].Status != agentapi.StatusIdle || page.Conversations[0].MessageCount != 2 {
		t.Errorf("conversations %+v (%v); want one, idle, with the message and the answer", page, err)
	}
}

// holdFirst is a model whose first call waits until release is closed.
type holdFirst struct {
	llm.Model
	called  atomic.Bool
	started chan struct{}
	release chan struct{}
}

func (m *holdFirst) Generate(ctx context.Context, req llm.Request) iter.Seq2[llm.Event, error] {
	inner := m.Model.Generate(ctx, req)
	first := m.called.CompareAndSwap(false, true)
	return func(yield func(llm.Event, error) bool) {
		if first {
			close(m.started)
			select {
			case <-m.release:
			case <-ctx.Done():
				yield(llm.Event{}, ctx.Err())
				return
			}
		}
		for e, err := range inner {
			if !yield(e, err) {
				return
			}
		}
	}
}

// TestAskRetriesOverCapacity: with -retry, a run refused because the server
// is at capacity waits the server's Retry-After, then runs.
func TestAskRetriesOverCapacity(t *testing.T) {
	t.Parallel()
	model := &holdFirst{
		Model:   llmtest.NewFake(t, llmtest.Text("first"), llmtest.Text("second")),
		started: make(chan struct{}), release: make(chan struct{}),
	}
	url := newTestAPI(t, model, nil, agentapi.WithMaxConcurrentRuns(1))
	client, err := agentclient.New(url, agentclient.WithToken(testToken))
	if err != nil {
		t.Fatal(err)
	}
	// Another run takes the one slot until the retry's wait.
	s, err := client.Stream(t.Context(), agentclient.RunRequest{Message: "first"})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	<-model.started
	other := make(chan error, 1)
	go func() {
		for {
			if _, err := s.Next(); err != nil {
				other <- err
				return
			}
		}
	}()

	out := newSyncBuffer("")
	c, stderr := clientCLI(url, out, nil, nil)
	var waits []time.Duration
	c.term.sleep = func(_ context.Context, d time.Duration) error {
		waits = append(waits, d)
		if len(waits) == 1 { // let the other run end; its slot is free once it has
			close(model.release)
			if err := <-other; err != io.EOF { //nolint:errorlint // Next returns io.EOF itself
				t.Errorf("the other run: %v", err)
			}
		}
		return nil
	}
	if code := c.main(t.Context(), []string{"ask", "-retry", "3", "second"}); code != exitDone || out.String() != "second\n" {
		t.Fatalf("exit %d, stdout %q, stderr %q; want 0 and the answer", code, out, stderr)
	}
	if len(waits) != 1 || waits[0] != 2*time.Second {
		t.Errorf("waits %v, want one of the server's Retry-After (2s)", waits)
	}
	checkClientOutput(t, out.String()+stderr.String())
}
