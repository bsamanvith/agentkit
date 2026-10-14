// Package web is the web component: it serves the console, a single-page
// app built from web/apps/console and embedded in the binary, at / on the
// same origin as /v1 (E11, T078).
//
// The build is committed in dist/ (make web writes it; CI fails when it is
// stale), so go build needs no Node.js.
//
// Every response carries a strict Content-Security-Policy (no inline script
// or style, nothing from other origins), X-Content-Type-Options: nosniff and
// Referrer-Policy: no-referrer. index.html is never cached; hashed files
// under /assets/ are cached for a year; other files (favicon, theme.js) are
// revalidated. Text files (scripts, styles, HTML, SVG) are gzipped once, at
// start, and sent compressed to clients that accept it: a third of the bytes
// on a slow connection. Paths that don't end in a file's extension (.js,
// .css, .png, ...) get index.html, so the app's routes (/c/{id}, /agents,
// /agents/research.v2) work on reload; a missing file with one, or anything
// under /assets/, is 404, so a stale tab never gets HTML for a script. /v1/ and /mcp/ belong to other components and are
// never answered with the app.
package web

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"io/fs"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"
)

// Name is the component's name in AGENTKIT_COMPONENTS and /readyz.
const Name = "web"

// CSP is the Content-Security-Policy of every response. connect-src 'self'
// covers /v1, the only API the console calls. Nothing is allowed inline or
// as a data: URI (icons are inline SVG elements, not images).
const CSP = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self'; " +
	"font-src 'self'; connect-src 'self'; object-src 'none'; base-uri 'none'; " +
	"form-action 'self'; frame-ancestors 'none'"

const (
	cacheIndex  = "no-cache, no-store, must-revalidate"
	cacheHashed = "public, max-age=31536000, immutable"
	cacheOther  = "no-cache"
)

//go:embed all:dist
var embedded embed.FS

// Dist returns the embedded console build.
func Dist() fs.FS {
	sub, err := fs.Sub(embedded, "dist")
	if err != nil {
		panic(err) // the embed pattern guarantees dist exists
	}
	return sub
}

// types are the media types of the files a build has, set explicitly:
// Go's own table lacks some (woff2), and the image has no /etc/mime.types.
var types = map[string]string{
	".html":  "text/html; charset=utf-8",
	".js":    "text/javascript; charset=utf-8",
	".css":   "text/css; charset=utf-8",
	".json":  "application/json",
	".svg":   "image/svg+xml",
	".png":   "image/png",
	".ico":   "image/x-icon",
	".woff2": "font/woff2",
	".txt":   "text/plain; charset=utf-8",
}

// moreFiles are file extensions the build doesn't have: a request for one is
// for a file too, never an app route.
var moreFiles = map[string]bool{
	".map": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true, ".avif": true,
	".woff": true, ".ttf": true, ".otf": true, ".xml": true, ".webmanifest": true, ".wasm": true,
}

// isFile reports whether a path asks for a file, by its extension. Any other
// path, dots and all, is an app route.
func isFile(name string) bool {
	ext := strings.ToLower(path.Ext(name))
	_, ok := types[ext]
	return ok || moreFiles[ext]
}

// file is one file of the build, read once.
type file struct {
	body  []byte
	etag  string
	ctype string
	gz    []byte // body gzipped, when that is smaller; nil otherwise
}

// compressible reports whether a media type is worth gzipping: text, not
// fonts or images that are compressed already.
func compressible(ctype string) bool {
	return strings.HasPrefix(ctype, "text/") || ctype == "image/svg+xml" || ctype == "application/json"
}

func gzipped(b []byte) []byte {
	var buf bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression) // a valid level never errs
	_, _ = zw.Write(b)                                       // a bytes.Buffer never errs
	_ = zw.Close()
	if buf.Len() >= len(b) {
		return nil
	}
	return buf.Bytes()
}

// acceptsGzip reports whether a request's Accept-Encoding allows gzip
// (RFC 9110 §12.5.3): gzip, or else "*", listed without q=0.
func acceptsGzip(r *http.Request) bool {
	gzipQ, starQ := -1.0, -1.0
	for _, v := range r.Header.Values("Accept-Encoding") {
		for part := range strings.SplitSeq(v, ",") {
			coding, params, _ := strings.Cut(part, ";")
			q := 1.0
			if k, val, ok := strings.Cut(params, "="); ok && strings.EqualFold(strings.TrimSpace(k), "q") {
				if f, err := strconv.ParseFloat(strings.TrimSpace(val), 64); err == nil {
					q = f
				}
			}
			switch coding = strings.TrimSpace(coding); {
			case strings.EqualFold(coding, "gzip"):
				gzipQ = q
			case coding == "*":
				starQ = q
			}
		}
	}
	if gzipQ >= 0 {
		return gzipQ > 0
	}
	return starQ > 0
}

// Component serves a console build. It implements component.Component.
type Component struct {
	files map[string]file // by path, "assets/index-x.js"
	err   error           // why the build can't be served, if it can't
}

// New serves the build in files (Dist for the embedded one), read into
// memory once. A build without index.html is served but not ready.
func New(files fs.FS) *Component {
	c := &Component{files: map[string]file{}}
	c.err = fs.WalkDir(files, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(files, name)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(b)
		ctype, ok := types[path.Ext(name)]
		if !ok {
			ctype = http.DetectContentType(b)
		}
		f := file{body: b, etag: `"` + hex.EncodeToString(sum[:12]) + `"`, ctype: ctype}
		if compressible(ctype) {
			f.gz = gzipped(b)
		}
		c.files[name] = f
		return nil
	})
	if _, ok := c.files["index.html"]; c.err == nil && !ok {
		c.err = errors.New("the console build has no index.html (run make web)")
	}
	return c
}

// Name returns Name.
func (c *Component) Name() string { return Name }

// Ready reports whether the build has its index.html.
func (c *Component) Ready(context.Context) error { return c.err }

// Shutdown does nothing: the component holds no resources.
func (c *Component) Shutdown(context.Context) error { return nil }

// Handler serves the build; mount it at "/".
func (c *Component) Handler() http.Handler { return http.HandlerFunc(c.serve) }

// reserved are the paths of other components: a request there that no
// component took is a 404, never the app, whatever its method. They are
// matched on the cleaned path, so //v1/x and /a/../v1/x are reserved too.
var reserved = []string{"/v1", "/mcp"}

func isReserved(clean string) bool {
	for _, prefix := range reserved {
		if clean == prefix || strings.HasPrefix(clean, prefix+"/") {
			return true
		}
	}
	return false
}

func (c *Component) serve(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Content-Security-Policy", CSP)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	clean := path.Clean("/" + r.URL.Path)
	if isReserved(clean) {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		h.Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	name := strings.TrimPrefix(clean, "/")
	if name == "" {
		name = "index.html"
	}
	f, ok := c.files[name]
	if !ok && name != "assets" && !strings.HasPrefix(name, "assets/") && !isFile(name) {
		f, ok = c.files["index.html"] // an app route
		name = "index.html"
	}
	if !ok {
		h.Set("Cache-Control", cacheOther)
		http.NotFound(w, r)
		return
	}
	switch {
	case name == "index.html":
		h.Set("Cache-Control", cacheIndex)
	case strings.HasPrefix(name, "assets/"):
		h.Set("Cache-Control", cacheHashed)
	default:
		h.Set("Cache-Control", cacheOther)
	}
	h.Set("Content-Type", f.ctype)
	body, etag := f.body, f.etag
	if f.gz != nil {
		h.Set("Vary", "Accept-Encoding")
		if acceptsGzip(r) {
			// Its own ETag: the two encodings are different representations.
			body, etag = f.gz, strings.TrimSuffix(f.etag, `"`)+`-gz"`
			h.Set("Content-Encoding", "gzip")
		}
	}
	h.Set("ETag", etag)
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(body))
}
