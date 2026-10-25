package gemini_test

import (
	"bufio"
	"bytes"
	"io"
	"iter"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/llm/gemini"
)

// stallMarker is an SSE comment line in a fixture. The replay server stops
// there and stays silent until the client goes away, like a slow model.
const stallMarker = ": replay stall\n"

// replay is a Gemini API stand-in that answers each request
// with the next recorded HTTP response (status, headers and body, as
// `curl -i` prints it) and records what it was sent.
type replay struct {
	t         testing.TB
	mu        sync.Mutex
	responses [][]byte
	requests  []recorded
}

type recorded struct {
	url    string
	header http.Header
	body   []byte
}

// newReplay starts a replay server that serves the given fixture files, in
// order, one per request.
func newReplay(t testing.TB, files ...string) (*replay, *httptest.Server) {
	t.Helper()
	rp := &replay{t: t}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		rp.responses = append(rp.responses, data)
	}
	srv := httptest.NewServer(rp)
	t.Cleanup(srv.Close)
	return rp, srv
}

func (rp *replay) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		rp.t.Errorf("replay: reading request: %v", err)
		return
	}
	rp.mu.Lock()
	rp.requests = append(rp.requests, recorded{url: r.URL.String(), header: r.Header.Clone(), body: body})
	n := len(rp.requests)
	rp.mu.Unlock()
	if n > len(rp.responses) {
		rp.t.Errorf("replay: request %d, but only %d responses recorded", n, len(rp.responses))
		http.Error(w, "no response recorded", http.StatusTeapot)
		return
	}
	resp, err := http.ReadResponse(bufio.NewReader(bytes.NewReader(rp.responses[n-1])), r)
	if err != nil {
		rp.t.Errorf("replay: fixture %d: %v", n, err)
		return
	}
	defer resp.Body.Close()
	recordedBody, err := io.ReadAll(resp.Body)
	if err != nil {
		rp.t.Errorf("replay: fixture %d body: %v", n, err)
		return
	}
	for k, vs := range resp.Header {
		w.Header()[k] = vs
	}
	w.WriteHeader(resp.StatusCode)
	before, _, stall := bytes.Cut(recordedBody, []byte(stallMarker))
	_, _ = w.Write(before)
	if stall {
		http.NewResponseController(w).Flush()
		<-r.Context().Done()
	}
}

// sent returns the requests received so far.
func (rp *replay) sent() []recorded {
	rp.mu.Lock()
	defer rp.mu.Unlock()
	return append([]recorded(nil), rp.requests...)
}

// newModel returns a model talking to srv, with a fake API key and
// retries off (TestRetriesStayOn covers them).
func newModel(t testing.TB, srv *httptest.Server, opts ...gemini.Option) *gemini.Model {
	t.Helper()
	m, err := gemini.New(append([]gemini.Option{
		gemini.WithModel("gemini-replay"),
		gemini.WithBaseURL(srv.URL + "/v1beta"),
		gemini.WithAPIKey("test-key"), gemini.WithMaxRetries(0),
	}, opts...)...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return m
}

// fixtures returns testdata/<dir>/<name>.http followed by <name>.2.http,
// <name>.3.http and so on, the replies of a multi-turn exchange.
func fixtures(t testing.TB, dir, name string) []string {
	t.Helper()
	files := []string{filepath.Join("testdata", dir, name+".http")}
	for i := 2; ; i++ {
		f := filepath.Join("testdata", dir, name+"."+strconv.Itoa(i)+".http")
		if _, err := os.Stat(f); err != nil {
			return files
		}
		files = append(files, f)
	}
}

// userText is a request with one user message.
func userText(text string) llm.Request {
	return llm.Request{Messages: []llm.Message{llm.User(llm.Text{Text: text})}}
}

// drain reads a stream to its end: the events, then the error if it failed.
func drain(seq iter.Seq2[llm.Event, error]) ([]llm.Event, error) {
	var events []llm.Event
	for ev, err := range seq {
		if err != nil {
			return events, err
		}
		events = append(events, ev)
	}
	return events, nil
}
