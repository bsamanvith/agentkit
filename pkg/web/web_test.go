package web_test

import (
	"context"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/bsamanvith/agentkit/pkg/web"
)

const index = `<!doctype html><html><head><script src="/theme.js"></script>` +
	`<script type="module" src="/assets/app-abc123.js"></script></head><body></body></html>`

func build() fstest.MapFS {
	return fstest.MapFS{
		"index.html":                 {Data: []byte(index)},
		"theme.js":                   {Data: []byte("// theme")},
		"favicon.svg":                {Data: []byte("<svg/>")},
		"assets/app-abc123.js":       {Data: []byte("console.log(1)")},
		"assets/app-abc123.css":      {Data: []byte("body{}")},
		"assets/geist-latin-x.woff2": {Data: []byte("wOF2")},
	}
}

func get(t *testing.T, h http.Handler, method, target string, hdr ...string) *http.Response {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), method, target, nil)
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Result()
}

func body(t *testing.T, resp *http.Response) string {
	t.Helper()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestServe: what each kind of path gets, and its caching (T055's rules).
func TestServe(t *testing.T) {
	t.Parallel()
	h := web.New(build()).Handler()
	tests := []struct {
		path, wantStatus, wantBody, wantType, wantCache string
	}{
		{"/", "200", index, "text/html; charset=utf-8", "no-cache, no-store, must-revalidate"},
		{"/index.html", "200", index, "text/html; charset=utf-8", "no-cache, no-store, must-revalidate"},
		// The app's routes get index.html, so they work on reload.
		{"/new", "200", index, "text/html; charset=utf-8", "no-cache, no-store, must-revalidate"},
		{"/c/conv_123", "200", index, "text/html; charset=utf-8", "no-cache, no-store, must-revalidate"},
		{"/agents/researcher", "200", index, "text/html; charset=utf-8", "no-cache, no-store, must-revalidate"},
		{"/assets/app-abc123.js", "200", "console.log(1)", "text/javascript; charset=utf-8", "public, max-age=31536000, immutable"},
		{"/assets/app-abc123.css", "200", "body{}", "text/css; charset=utf-8", "public, max-age=31536000, immutable"},
		{"/assets/geist-latin-x.woff2", "200", "wOF2", "font/woff2", "public, max-age=31536000, immutable"},
		{"/theme.js", "200", "// theme", "text/javascript; charset=utf-8", "no-cache"},
		{"/favicon.svg", "200", "<svg/>", "image/svg+xml", "no-cache"},
		// A stale tab asking for an old hash gets a 404, not HTML it would
		// run as a script.
		{"/assets/app-old999.js", "404", "", "", "no-cache"},
		{"/assets/nothing", "404", "", "", "no-cache"},
		{"/robots.txt", "404", "", "", "no-cache"},
		{"/../index.html", "200", index, "text/html; charset=utf-8", "no-cache, no-store, must-revalidate"},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			t.Parallel()
			resp := get(t, h, http.MethodGet, tt.path)
			got := body(t, resp)
			if s := resp.Status[:3]; s != tt.wantStatus {
				t.Fatalf("status %s, want %s", resp.Status, tt.wantStatus)
			}
			if tt.wantBody != "" && got != tt.wantBody {
				t.Errorf("body %q, want %q", got, tt.wantBody)
			}
			if tt.wantType != "" && resp.Header.Get("Content-Type") != tt.wantType {
				t.Errorf("Content-Type %q, want %q", resp.Header.Get("Content-Type"), tt.wantType)
			}
			if c := resp.Header.Get("Cache-Control"); c != tt.wantCache {
				t.Errorf("Cache-Control %q, want %q", c, tt.wantCache)
			}
		})
	}
}

// TestSecurityHeaders: every response carries them, errors included.
func TestSecurityHeaders(t *testing.T) {
	t.Parallel()
	h := web.New(build()).Handler()
	for _, r := range []struct{ method, path string }{
		{"GET", "/"},
		{"GET", "/c/x"},
		{"GET", "/assets/app-abc123.js"},
		{"GET", "/assets/gone.js"},
		{"HEAD", "/"},
		{"POST", "/"},
		{"GET", "/v1/agents"},
	} {
		resp := get(t, h, r.method, r.path)
		for k, want := range map[string]string{
			"Content-Security-Policy": web.CSP,
			"X-Content-Type-Options":  "nosniff",
			"Referrer-Policy":         "no-referrer",
		} {
			if got := resp.Header.Get(k); got != want {
				t.Errorf("%s %s: %s = %q, want %q", r.method, r.path, k, got, want)
			}
		}
	}
	directives := strings.Split(web.CSP, "; ")
	for _, want := range []string{"default-src 'self'", "script-src 'self'", "style-src 'self'", "frame-ancestors 'none'"} {
		if !slices.Contains(directives, want) {
			t.Errorf("CSP %q lacks %q", web.CSP, want)
		}
	}
	if strings.Contains(web.CSP, "unsafe-") {
		t.Errorf("CSP %q allows unsafe sources", web.CSP)
	}
}

// TestNotTheApp: paths of other components, and methods other than GET and
// HEAD, never get the app.
func TestNotTheApp(t *testing.T) {
	t.Parallel()
	h := web.New(build()).Handler()
	for _, p := range []string{"/v1", "/v1/", "/v1/agents", "/mcp/", "/mcp/exa"} {
		if resp := get(t, h, http.MethodGet, p); resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s: %s, want 404", p, resp.Status)
		}
	}
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		resp := get(t, h, m, "/")
		if resp.StatusCode != http.StatusMethodNotAllowed || resp.Header.Get("Allow") != "GET, HEAD" {
			t.Errorf("%s /: %s, Allow %q; want 405, GET, HEAD", m, resp.Status, resp.Header.Get("Allow"))
		}
	}
}

func TestRevalidation(t *testing.T) {
	t.Parallel()
	h := web.New(build()).Handler()
	for _, p := range []string{"/", "/theme.js", "/assets/app-abc123.js"} {
		first := get(t, h, http.MethodGet, p)
		etag := first.Header.Get("ETag")
		if etag == "" {
			t.Fatalf("GET %s: no ETag", p)
		}
		if again := get(t, h, http.MethodGet, p, "If-None-Match", etag); again.StatusCode != http.StatusNotModified {
			t.Errorf("GET %s with its ETag: %s, want 304", p, again.Status)
		}
	}
	if resp := get(t, h, http.MethodHead, "/"); resp.StatusCode != http.StatusOK || body(t, resp) != "" {
		t.Errorf("HEAD /: %s with a body", resp.Status)
	}
}

func TestReady(t *testing.T) {
	t.Parallel()
	if err := web.New(build()).Ready(t.Context()); err != nil {
		t.Errorf("Ready with index.html = %v", err)
	}
	empty := web.New(fstest.MapFS{"assets/x.js": {Data: []byte("x")}})
	if err := empty.Ready(context.Background()); err == nil {
		t.Error("Ready without index.html = nil, want an error")
	}
	if resp := get(t, empty.Handler(), http.MethodGet, "/"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET / without index.html: %s, want 404", resp.Status)
	}
}

var (
	refs         = regexp.MustCompile(`(?:src|href)="(/[^"]*)"`)
	scriptTag    = regexp.MustCompile(`(?is)<script\b([^>]*)>(.*?)</script>`)
	inlineStyle  = regexp.MustCompile(`(?i)<style\b|\sstyle="`)
	inlineHandle = regexp.MustCompile(`(?i)\son[a-z]+=`)
)

// TestEmbeddedBuild checks the committed build (make web): it is ready,
// everything index.html loads is in it, and nothing in index.html needs
// what the CSP forbids (inline script, inline style, handlers).
func TestEmbeddedBuild(t *testing.T) {
	t.Parallel()
	dist := web.Dist()
	c := web.New(dist)
	if err := c.Ready(t.Context()); err != nil {
		t.Fatalf("embedded build: %v", err)
	}
	html, err := fs.ReadFile(dist, "index.html")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range refs.FindAllStringSubmatch(string(html), -1) {
		if resp := get(t, c.Handler(), http.MethodGet, m[1]); resp.StatusCode != http.StatusOK ||
			strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") {
			t.Errorf("index.html loads %s: %s %s", m[1], resp.Status, resp.Header.Get("Content-Type"))
		}
	}
	for _, m := range scriptTag.FindAllStringSubmatch(string(html), -1) {
		if !strings.Contains(m[1], "src=") || strings.TrimSpace(m[2]) != "" {
			t.Errorf("index.html has an inline script, which the CSP blocks: %.80s", m[0])
		}
	}
	if inlineStyle.Match(html) || inlineHandle.Match(html) {
		t.Error("index.html has an inline style or event handler, which the CSP blocks")
	}
}
