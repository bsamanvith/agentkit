package registry_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/clock/clocktest"
	"github.com/bsamanvith/agentkit/pkg/mcp/registry"
)

// The cassettes in testdata are the official registry's answers, recorded
// on 2026-10-22 with curl and kept byte for byte:
//
//	linear-20.json  /v0.1/servers?search=linear&limit=20&version=latest (one page, the last)
//	linear-1.json   /v0.1/servers?search=linear&limit=4&version=latest
//	linear-2.json   the same with cursor=io.github.mrfentmen/linear-mcp:1.1.0
var cassettes = map[string]string{
	"limit=20&search=linear&version=latest":                                                "linear-20.json",
	"limit=4&search=linear&version=latest":                                                 "linear-1.json",
	"cursor=io.github.mrfentmen%2Flinear-mcp%3A1.1.0&limit=4&search=linear&version=latest": "linear-2.json",
}

// replay serves the cassettes under prefix, and fails the test on any other
// request. It counts the requests it answers.
type replay struct {
	t    *testing.T
	ts   *httptest.Server
	mu   sync.Mutex
	seen []string
	// fail, when set, answers instead of the cassettes.
	fail http.HandlerFunc
}

func newReplay(t *testing.T, prefix string) *replay {
	t.Helper()
	r := &replay{t: t}
	r.ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		r.seen = append(r.seen, req.URL.RawQuery)
		fail := r.fail
		r.mu.Unlock()
		if fail != nil {
			fail(w, req)
			return
		}
		file, ok := cassettes[req.URL.Query().Encode()]
		if req.Method != http.MethodGet || req.URL.Path != prefix+"/v0.1/servers" || !ok {
			t.Errorf("unexpected request %s %s", req.Method, req.URL)
			http.NotFound(w, req)
			return
		}
		b, err := os.ReadFile(filepath.Join("testdata", file))
		if err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(b)
	}))
	t.Cleanup(r.ts.Close)
	return r
}

func (r *replay) requests() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.seen)
}

func (r *replay) setFail(h http.HandlerFunc) {
	r.mu.Lock()
	r.fail = h
	r.mu.Unlock()
}

func names(s []registry.Server) []string {
	var out []string
	for _, x := range s {
		out = append(out, x.Name)
	}
	return out
}

// Criterion "Search": against the recorded registry, only servers with a
// remote are returned; the packages-only ones are dropped.
func TestSearchKeepsOnlyRemotes(t *testing.T) {
	t.Parallel()
	r := newReplay(t, "")
	c, err := registry.New(r.ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.Search(t.Context(), "linear", 20)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"app.linear/linear", "io.github.Evozim/linear-broker", "io.github.pipeworx-io/linear",
		"io.github.sadri-dridi/linear-id-ok", "io.github.sadri-dridi/linear-id-shape", "io.usefulapi/linear",
	}
	if !slices.Equal(names(got), want) {
		t.Fatalf("servers %q, want %q (the packages-only ones dropped)", names(got), want)
	}
	for _, s := range got {
		if len(s.Remotes) == 0 {
			t.Errorf("%s has no remote", s.Name)
		}
	}
	linear := got[0]
	if linear.Title != "Linear" || linear.Version != "1.0.1" || linear.WebsiteURL != "https://linear.app" || linear.Status != "active" ||
		!reflect.DeepEqual(linear.Remotes, []registry.Remote{{Transport: registry.TransportStreamableHTTP, URL: "https://mcp.linear.app/mcp"}}) {
		t.Errorf("linear: %+v", linear)
	}
	broker := got[1]
	wantHeader := registry.Header{Name: "payment-signature", Description: "Base64-encoded EIP-3009 TransferWithAuthorization signature. Required for premium settlement.", Secret: true}
	if len(broker.Remotes) != 1 || broker.Remotes[0].Transport != registry.TransportSSE || !slices.Equal(broker.Remotes[0].Headers, []registry.Header{wantHeader}) {
		t.Errorf("an SSE remote with a header: %+v", broker.Remotes)
	}
}

// A page can hold fewer servers with remotes than asked for: the next page
// is read, by its cursor, until there are enough.
func TestSearchReadsMorePages(t *testing.T) {
	t.Parallel()
	r := newReplay(t, "/sub")
	c, err := registry.New(r.ts.URL + "/sub")
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.Search(t.Context(), " linear ", 4)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"app.linear/linear", "io.github.Evozim/linear-broker", "io.github.pipeworx-io/linear", "io.github.sadri-dridi/linear-id-ok"}
	if !slices.Equal(names(got), want) {
		t.Errorf("servers %q, want %q", names(got), want)
	}
	if n := len(r.requests()); n != 2 {
		t.Errorf("%d requests, want the two pages", n)
	}
}

// A result is cached for its TTL, and a failure is not.
func TestSearchCaches(t *testing.T) {
	t.Parallel()
	r := newReplay(t, "")
	clk := clocktest.NewFake(time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC))
	c, err := registry.New(r.ts.URL, registry.WithClock(clk), registry.WithCacheTTL(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	r.setFail(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "down", http.StatusBadGateway) })
	if _, err := c.Search(t.Context(), "linear", 20); !errors.Is(err, registry.ErrUnavailable) {
		t.Fatalf("a registry answering 502: %v", err)
	}
	r.setFail(nil)
	first, err := c.Search(t.Context(), "linear", 20)
	if err != nil {
		t.Fatalf("after a failure, which is not cached: %v", err)
	}
	first[0].Remotes[0].URL = "https://scribbled.example.com"
	clk.Advance(59 * time.Second)
	second, err := c.Search(t.Context(), "LINEAR", 20)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(r.requests()); n != 2 {
		t.Errorf("%d requests, want the failed one and one more: the second search is cached", n)
	}
	if second[0].Remotes[0].URL != "https://mcp.linear.app/mcp" {
		t.Errorf("the cache shares memory with a caller: %q", second[0].Remotes[0].URL)
	}
	second[0].Remotes[0].URL = "https://scribbled.example.com"
	if third, err := c.Search(t.Context(), "linear", 20); err != nil || third[0].Remotes[0].URL != "https://mcp.linear.app/mcp" {
		t.Errorf("the cache shares memory with what it returned: %v %v", third, err)
	}
	clk.Advance(time.Second)
	if _, err := c.Search(t.Context(), "linear", 20); err != nil {
		t.Fatal(err)
	}
	if n := len(r.requests()); n != 3 {
		t.Errorf("%d requests, want 3: the result expired", n)
	}
}

// The cache keeps at most its size, dropping the oldest.
func TestSearchCacheSize(t *testing.T) {
	t.Parallel()
	r := newReplay(t, "")
	clk := clocktest.NewFake(time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC))
	c, err := registry.New(r.ts.URL, registry.WithClock(clk), registry.WithCacheSize(1))
	if err != nil {
		t.Fatal(err)
	}
	for _, limit := range []int{20, 4, 20} {
		clk.Advance(time.Second)
		if _, err := c.Search(t.Context(), "linear", limit); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(r.requests()); n != 4 { // 1, then 2 pages, then 1 again
		t.Errorf("%d requests, want 4: a cache of one search", n)
	}
}

// A registry that is down, or answers what is not the API, is
// ErrUnavailable; a cancelled search is the context's error.
func TestSearchUnavailable(t *testing.T) {
	t.Parallel()
	for name, h := range map[string]http.HandlerFunc{
		// A list, but with an error status.
		"status": func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"servers":[]}`))
		},
		"not json": func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("<html>")) },
		"no list":  func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"error":"x"}`)) },
		// A valid list, over the cap.
		"too large": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"servers":[],"pad":"` + strings.Repeat("x", 4<<20) + `"}`))
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := newReplay(t, "")
			r.setFail(h)
			c, err := registry.New(r.ts.URL)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := c.Search(t.Context(), "linear", 20); !errors.Is(err, registry.ErrUnavailable) {
				t.Errorf("err %v, want ErrUnavailable", err)
			}
		})
	}
	t.Run("closed", func(t *testing.T) {
		t.Parallel()
		ts := httptest.NewServer(http.NotFoundHandler())
		ts.Close()
		c, err := registry.New(ts.URL)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.Search(t.Context(), "linear", 20); !errors.Is(err, registry.ErrUnavailable) {
			t.Errorf("err %v, want ErrUnavailable", err)
		}
	})
	t.Run("cancelled", func(t *testing.T) {
		t.Parallel()
		r := newReplay(t, "")
		c, err := registry.New(r.ts.URL)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if _, err := c.Search(ctx, "linear", 20); !errors.Is(err, context.Canceled) {
			t.Errorf("err %v, want context.Canceled", err)
		}
	})
}

// The registry is called outside the SSRF guard, so its answers must not
// send the server elsewhere: a redirect off the registry's origin is not
// followed, and the search fails; one on it is.
func TestSearchRedirects(t *testing.T) {
	t.Parallel()
	var elsewhere atomic.Int64
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		elsewhere.Add(1)
		_, _ = w.Write([]byte(`{"servers":[]}`))
	}))
	t.Cleanup(other.Close)
	r := newReplay(t, "/moved")
	r.setFail(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/off/v0.1/servers":
			http.Redirect(w, req, other.URL+"/latest/meta-data", http.StatusFound)
		case "/on/v0.1/servers":
			http.Redirect(w, req, "/moved/v0.1/servers?"+req.URL.RawQuery, http.StatusFound)
		default:
			_, _ = w.Write([]byte(`{"servers":[{"server":{"name":"a/b","remotes":[{"type":"sse","url":"https://a.example/sse"}]}}]}`))
		}
	})
	off, err := registry.New(r.ts.URL + "/off")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := off.Search(t.Context(), "linear", 20); !errors.Is(err, registry.ErrUnavailable) {
		t.Errorf("a redirect to another host: %v, want ErrUnavailable", err)
	}
	if n := elsewhere.Load(); n != 0 {
		t.Errorf("the other host got %d requests", n)
	}
	on, err := registry.New(r.ts.URL + "/on")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := on.Search(t.Context(), "linear", 20); err != nil || len(got) != 1 {
		t.Errorf("a redirect on the registry's host: %v, %v", names(got), err)
	}
}

// What the registry says is third-party text: links that are not http(s)
// are dropped, and so are remotes whose URL could mislead (format
// characters, odd braces, over the length an add takes); an entry keeps at
// most a few remotes.
func TestSearchSanitizesURLs(t *testing.T) {
	t.Parallel()
	remote := func(u string) string { return `{"type":"streamable-http","url":` + strconv.Quote(u) + `}` }
	var many []string
	for i := range 20 {
		many = append(many, remote(fmt.Sprintf("https://m%d.example/mcp", i)))
	}
	body := `{"servers":[
		{"server":{"name":"a/links","websiteUrl":"javascript:alert(1)","repository":{"url":"data:text/html,x"},"remotes":[` + remote("https://ok.example/mcp") + `]}},
		{"server":{"name":"a/good","websiteUrl":"https://good.example","repository":{"url":"https://github.com/a/good"},"remotes":[` + remote("https://good.example/mcp") + `]}},
		{"server":{"name":"a/bidi","remotes":[` + remote("https://evil.example/\u202emcp") + `]}},
		{"server":{"name":"a/braces","remotes":[` + remote("https://{a b}.example/mcp") + `,` + remote("https://x.example/{bad") + `]}},
		{"server":{"name":"a/long","remotes":[` + remote("https://x.example/"+strings.Repeat("a", 2100)) + `]}},
		{"server":{"name":"a/js","remotes":[` + remote("javascript://x.example/%0aalert(1)") + `]}},
		{"server":{"name":"a/many","remotes":[` + strings.Join(many, ",") + `]}}
	]}`
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) }))
	t.Cleanup(ts.Close)
	c, err := registry.New(ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.Search(t.Context(), "a", 20)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"a/links", "a/good", "a/many"}; !slices.Equal(names(got), want) {
		t.Fatalf("servers %q, want %q", names(got), want)
	}
	if got[0].WebsiteURL != "" || got[0].RepositoryURL != "" {
		t.Errorf("links kept: %q, %q", got[0].WebsiteURL, got[0].RepositoryURL)
	}
	if got[1].WebsiteURL != "https://good.example" || got[1].RepositoryURL != "https://github.com/a/good" {
		t.Errorf("good links dropped: %+v", got[1])
	}
	if n := len(got[2].Remotes); n == 0 || n > 10 {
		t.Errorf("a server with 20 remotes keeps %d", n)
	}
}

func TestSearchInvalid(t *testing.T) {
	t.Parallel()
	r := newReplay(t, "")
	c, err := registry.New(r.ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		q     string
		limit int
	}{{"", 10}, {"   ", 10}, {string(make([]byte, registry.MaxQueryLength+1)), 10}, {"linear", 0}, {"linear", registry.MaxLimit + 1}} {
		if _, err := c.Search(t.Context(), tc.q, tc.limit); !errors.Is(err, registry.ErrInvalid) {
			t.Errorf("Search(%d bytes, %d) = %v, want ErrInvalid", len(tc.q), tc.limit, err)
		}
	}
	if n := len(r.requests()); n != 0 {
		t.Errorf("%d requests for invalid searches", n)
	}
	for _, base := range []string{"ftp://x", "https://", "https://u:p@x", "https://x?q=1", "https://x#f", "::"} {
		if _, err := registry.New(base); err == nil {
			t.Errorf("New(%q) accepted", base)
		}
	}
}
