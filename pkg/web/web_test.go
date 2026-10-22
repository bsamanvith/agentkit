package web_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"io/fs"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
		// A dot isn't a file: an agent's name or an ID may have one.
		{"/agents/research.v2", "200", index, "text/html; charset=utf-8", "no-cache, no-store, must-revalidate"},
		{"/c/conv.1", "200", index, "text/html; charset=utf-8", "no-cache, no-store, must-revalidate"},
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
		{"/app.js.map", "404", "", "", "no-cache"},
		// A packed file is served by its name only (Pack), never as name.gz.
		{"/theme.js.gz", "404", "", "", "no-cache"},
		{"/index.html.gz", "404", "", "", "no-cache"},
		{"/logo.PNG", "404", "", "", "no-cache"},
		{"/agents/x.js", "404", "", "", "no-cache"},
		{"/assets", "404", "", "", "no-cache"},
		{"/assets/", "404", "", "", "no-cache"},
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
	// Every directive named, so none falls back to default-src unseen.
	want := map[string]string{
		"default-src": "'self'", "script-src": "'self'", "style-src": "'self'",
		"img-src": "'self'", "font-src": "'self'", "connect-src": "'self'",
		"object-src": "'none'", "base-uri": "'none'", "form-action": "'self'",
		"frame-ancestors": "'none'",
	}
	got := map[string]string{}
	for _, d := range strings.Split(web.CSP, "; ") {
		name, value, _ := strings.Cut(d, " ")
		got[name] = value
	}
	if !maps.Equal(got, want) {
		t.Errorf("CSP %q\nwant %v", web.CSP, want)
	}
}

// TestNotTheApp: paths of other components, and methods other than GET and
// HEAD, never get the app.
func TestNotTheApp(t *testing.T) {
	t.Parallel()
	h := web.New(build()).Handler()
	// Unclean paths too: ServeMux redirects them in agentkit serve, but the
	// handler's own rule must not depend on it.
	for _, p := range []string{"/v1", "/v1/", "/v1/agents", "/mcp", "/mcp/", "/mcp/exa", "//v1/agents", "/x/../v1/agents", "/./mcp/a", "/rpc/runner/Run"} {
		for _, m := range []string{http.MethodGet, http.MethodPost, http.MethodDelete} {
			if resp := get(t, h, m, p); resp.StatusCode != http.StatusNotFound {
				t.Errorf("%s %s: %s, want 404", m, p, resp.Status)
			}
		}
	}
	// Paths that only start like a reserved one are the app's.
	for _, p := range []string{"/v1x", "/mcpx/a", "/c/v1", "/rpcx"} {
		if resp := get(t, h, http.MethodGet, p); resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s: %s, want the app", p, resp.Status)
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
	html := []byte(body(t, get(t, c.Handler(), http.MethodGet, "/index.html")))
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

// TestEmbeddedBuildIsPacked keeps the console's text files out of the binary
// uncompressed (docs/SIZES.md): the committed build, unpacked and packed
// again, is byte for byte what is committed. A build written by Vite without
// the pack step (make web runs it), one with a file both as it is and
// packed, or one packed some other way (gzip -9, another level) fails here.
func TestEmbeddedBuildIsPacked(t *testing.T) {
	t.Parallel()
	before := tree(t, web.Dist())
	dir := t.TempDir()
	for name, b := range before {
		if orig, ok := strings.CutSuffix(name, ".gz"); ok {
			name, b = orig, gunzip(t, b)
		}
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(b), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := web.Pack(dir); err != nil {
		t.Fatal(err)
	}
	if after := tree(t, os.DirFS(dir)); !maps.Equal(before, after) {
		t.Errorf("pkg/web/dist is not packed: run make web (it runs go run ./pkg/web/pack.go pkg/web/dist)\nbefore %v\nafter  %v", keys(before), keys(after))
	}
	if !slices.ContainsFunc(keys(before), func(n string) bool { return strings.HasSuffix(n, ".js.gz") }) {
		t.Errorf("pkg/web/dist has no packed script: %v", keys(before))
	}
}

// tree returns every file of files, by name.
func tree(t *testing.T, files fs.FS) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := fs.WalkDir(files, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(files, name)
		out[name] = string(b)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func keys(m map[string]string) []string { return slices.Sorted(maps.Keys(m)) }

func gz(t *testing.T, s string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write([]byte(s)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func gunzip(t *testing.T, b string) string {
	t.Helper()
	zr, err := gzip.NewReader(strings.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	return string(got)
}

// TestPack: the text files that shrink become name.gz, the rest stay; the
// packed build serves what the unpacked one holds, with the same ETags, and
// packing twice changes nothing.
func TestPack(t *testing.T) {
	t.Parallel()
	script := strings.Repeat("console.log('agentkit');\n", 200)
	files := build()
	files["assets/big-abc123.js"] = &fstest.MapFile{Data: []byte(script)}
	files["index.html"] = &fstest.MapFile{Data: []byte(index + strings.Repeat("<!-- -->", 50))}
	files["assets/data.tar.gz"] = &fstest.MapFile{Data: []byte("not ours")}
	dir := t.TempDir()
	if err := os.CopyFS(dir, files); err != nil {
		t.Fatal(err)
	}
	if err := web.Pack(dir); err != nil {
		t.Fatal(err)
	}
	packed := tree(t, os.DirFS(dir))
	want := []string{
		"assets/app-abc123.css", "assets/app-abc123.js", "assets/big-abc123.js.gz", "assets/data.tar.gz",
		"assets/geist-latin-x.woff2", "favicon.svg", "index.html.gz", "theme.js",
	}
	if got := keys(packed); !slices.Equal(got, want) {
		t.Fatalf("packed build %v, want %v", got, want)
	}
	if got := gunzip(t, packed["assets/big-abc123.js.gz"]); got != script {
		t.Errorf("big-abc123.js.gz holds %.40q, want the script", got)
	}
	if err := web.Pack(dir); err != nil {
		t.Fatal(err)
	}
	if again := tree(t, os.DirFS(dir)); !maps.Equal(again, packed) {
		t.Errorf("packing twice changed the build: %v", keys(again))
	}

	plainC, gzdC := web.New(files), web.New(os.DirFS(dir))
	for name, c := range map[string]*web.Component{"unpacked": plainC, "packed": gzdC} {
		if err := c.Ready(t.Context()); err != nil {
			t.Fatalf("the %s build: Ready = %v", name, err)
		}
	}
	plain, gzd := plainC.Handler(), gzdC.Handler()
	for name := range files {
		p := "/" + name
		a, b := get(t, plain, http.MethodGet, p), get(t, gzd, http.MethodGet, p)
		if a.StatusCode != http.StatusOK || b.StatusCode != http.StatusOK {
			t.Errorf("GET %s: %s unpacked, %s packed; want 200 from both", p, a.Status, b.Status)
		}
		if body(t, a) != body(t, b) || a.Header.Get("ETag") != b.Header.Get("ETag") ||
			a.Header.Get("Content-Type") != b.Header.Get("Content-Type") {
			t.Errorf("GET %s: the packed build serves it otherwise (ETag %q, %q)", p, a.Header.Get("ETag"), b.Header.Get("ETag"))
		}
	}
}

// TestCompression: packed text files go gzipped to clients that accept it,
// with their own ETag, and decompressed to the others; files that aren't
// packed (fonts, files that don't shrink) go as they are.
func TestCompression(t *testing.T) {
	t.Parallel()
	script := strings.Repeat("console.log('agentkit');\n", 200)
	files := build()
	files["assets/big-abc123.js.gz"] = &fstest.MapFile{Data: gz(t, script)}
	h := web.New(files).Handler()

	plain := get(t, h, http.MethodGet, "/assets/big-abc123.js")
	if plain.Header.Get("Content-Encoding") != "" || body(t, plain) != script || plain.Header.Get("Vary") != "Accept-Encoding" ||
		plain.Header.Get("Content-Type") != "text/javascript; charset=utf-8" {
		t.Fatalf("without Accept-Encoding: Content-Encoding %q, Vary %q, Content-Type %q",
			plain.Header.Get("Content-Encoding"), plain.Header.Get("Vary"), plain.Header.Get("Content-Type"))
	}
	for _, ae := range []string{"gzip", "gzip, deflate, br, zstd", "br;q=1.0, gzip;q=0.8", "*", "GZIP"} {
		resp := get(t, h, http.MethodGet, "/assets/big-abc123.js", "Accept-Encoding", ae)
		if resp.Header.Get("Content-Encoding") != "gzip" {
			t.Errorf("Accept-Encoding %q: Content-Encoding %q, want gzip", ae, resp.Header.Get("Content-Encoding"))
			continue
		}
		if got := gunzip(t, body(t, resp)); got != script {
			t.Errorf("Accept-Encoding %q: the gzipped body is not the file", ae)
		}
		if etag := resp.Header.Get("ETag"); etag == plain.Header.Get("ETag") || etag == "" {
			t.Errorf("Accept-Encoding %q: ETag %q, want one of its own", ae, etag)
		} else if again := get(t, h, http.MethodGet, "/assets/big-abc123.js", "Accept-Encoding", ae, "If-None-Match", etag); again.StatusCode != http.StatusNotModified {
			t.Errorf("Accept-Encoding %q with its ETag: %s, want 304", ae, again.Status)
		}
	}
	for _, ae := range []string{"identity", "gzip;q=0", "*;q=0", "gzip; q=0.000", "*, gzip;q=0", "br"} {
		if resp := get(t, h, http.MethodGet, "/assets/big-abc123.js", "Accept-Encoding", ae); resp.Header.Get("Content-Encoding") != "" {
			t.Errorf("Accept-Encoding %q: Content-Encoding %q, want none", ae, resp.Header.Get("Content-Encoding"))
		}
	}
	// The name.gz itself is not a file of the build.
	if resp := get(t, h, http.MethodGet, "/assets/big-abc123.js.gz"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET /assets/big-abc123.js.gz: %s, want 404", resp.Status)
	}
	// A font is compressed already; a tiny file grows when gzipped.
	for _, p := range []string{"/assets/geist-latin-x.woff2", "/theme.js"} {
		resp := get(t, h, http.MethodGet, p, "Accept-Encoding", "gzip")
		if resp.Header.Get("Content-Encoding") != "" || resp.Header.Get("Vary") != "" {
			t.Errorf("GET %s: Content-Encoding %q, Vary %q; want neither", p, resp.Header.Get("Content-Encoding"), resp.Header.Get("Vary"))
		}
	}
}

// TestBadPack: a build that has a file both as it is and packed, or a
// packed file that isn't gzip, is not ready.
func TestBadPack(t *testing.T) {
	t.Parallel()
	dup := build()
	dup["theme.js.gz"] = &fstest.MapFile{Data: gz(t, "// theme")}
	bad := build()
	bad["assets/big-abc123.js.gz"] = &fstest.MapFile{Data: []byte("not gzip")}
	for name, files := range map[string]fstest.MapFS{"both": dup, "not gzip": bad} {
		if err := web.New(files).Ready(t.Context()); err == nil {
			t.Errorf("%s: Ready = nil, want an error", name)
		}
	}
}
