package anthropic_test

import (
	"bufio"
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"

	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/bsamanvith/agentkit/pkg/llm/anthropic"
)

// stallMarker is an SSE comment line in a fixture. The replay server stops
// there and stays silent until the client goes away, like a slow model.
const stallMarker = ": replay stall\n"

// replay is an Anthropic API stand-in that answers each request with the next
// recorded HTTP response (status, headers and body, as `curl -i` prints it)
// and records what it was sent.
type replay struct {
	t         testing.TB
	mu        sync.Mutex
	responses [][]byte
	requests  []recorded
}

type recorded struct {
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
	rp.requests = append(rp.requests, recorded{header: r.Header.Clone(), body: body})
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

// newModel returns a model talking to srv, with a fake API key and the SDK's
// retries off (TestRetriesStayOn covers them).
func newModel(t testing.TB, srv *httptest.Server, opts ...anthropic.Option) *anthropic.Model {
	t.Helper()
	m, err := anthropic.New(append([]anthropic.Option{
		anthropic.WithBaseURL(srv.URL),
		anthropic.WithClientOptions(option.WithAPIKey("test-key"), option.WithMaxRetries(0)),
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
