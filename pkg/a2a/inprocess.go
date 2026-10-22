package a2a

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sync"
)

// The A2A server calls the agent API in process: each call is an HTTP
// request handed straight to the API's handler, never sent over a network.
// The caller's credential goes along unchanged, so the API authenticates it,
// scopes it to its tenant and checks its scopes and agents exactly as it
// does for its own clients; this package never reads it.

// caller is what an A2A request passes to the API's calls it makes.
type caller struct {
	header    http.Header // the credential headers of the A2A request
	remote    string      // its RemoteAddr, for the API's logs
	challenge *string     // set to the WWW-Authenticate of a 401 from the API
}

type callerKey struct{}

// credentialHeaders are the request headers that carry a credential or
// identify the request, and go to the agent API. Nothing else does.
var credentialHeaders = []string{"Authorization", "X-Request-Id"}

func withCaller(ctx context.Context, r *http.Request) (context.Context, *caller) {
	c := &caller{header: http.Header{}, remote: r.RemoteAddr, challenge: new(string)}
	for _, h := range credentialHeaders {
		if v := r.Header.Values(h); len(v) > 0 {
			c.header[h] = v
		}
	}
	return context.WithValue(ctx, callerKey{}, c), c
}

func callerFrom(ctx context.Context) *caller {
	c, _ := ctx.Value(callerKey{}).(*caller)
	return c
}

// inProcess is an http.RoundTripper that serves each request with h, in
// this process. A streamed response is read as the handler writes it.
type inProcess struct{ h http.Handler }

func (t inProcess) RoundTrip(req *http.Request) (*http.Response, error) {
	ctx, cancel := context.WithCancel(req.Context())
	in := req.Clone(ctx)
	in.RequestURI = req.URL.RequestURI()
	if c := callerFrom(ctx); c != nil {
		for k, v := range c.header {
			in.Header[k] = v
		}
		in.RemoteAddr = c.remote
	}
	if in.Body == nil {
		in.Body = http.NoBody
	}
	pr, pw := io.Pipe()
	w := &pipeWriter{header: http.Header{}, pw: pw, ready: make(chan struct{})}
	go func() {
		defer func() {
			if p := recover(); p != nil {
				w.WriteHeader(http.StatusInternalServerError)
				pw.CloseWithError(errors.New("a2a: the agent API's handler panicked"))
				return
			}
			w.WriteHeader(http.StatusOK) // a handler that wrote nothing
			pw.Close()
		}()
		t.h.ServeHTTP(w, in)
	}()
	select {
	case <-w.ready:
	case <-ctx.Done():
		cancel()
		pr.CloseWithError(ctx.Err())
		return nil, ctx.Err()
	}
	if w.status == http.StatusUnauthorized {
		if c := callerFrom(ctx); c != nil {
			*c.challenge = w.sent.Get("WWW-Authenticate")
		}
	}
	return &http.Response{
		Status: http.StatusText(w.status), StatusCode: w.status,
		Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
		Header: w.sent, Body: &pipeBody{pr: pr, cancel: cancel},
		ContentLength: -1, Request: req,
	}, nil
}

// pipeWriter is the handler's http.ResponseWriter: the body goes into the
// pipe, which the response's Body reads.
type pipeWriter struct {
	header http.Header
	pw     *io.PipeWriter

	once   sync.Once
	ready  chan struct{}
	status int
	sent   http.Header // the header as of WriteHeader
}

func (w *pipeWriter) Header() http.Header { return w.header }

func (w *pipeWriter) WriteHeader(status int) {
	w.once.Do(func() {
		w.status, w.sent = status, w.header.Clone()
		close(w.ready)
	})
}

func (w *pipeWriter) Write(b []byte) (int, error) {
	w.WriteHeader(http.StatusOK)
	return w.pw.Write(b)
}

// Flush is a no-op: the pipe hands every write to the reader at once.
func (w *pipeWriter) Flush() {}

// pipeBody is a response body; closing it ends the request, as a client
// that goes away does.
type pipeBody struct {
	pr     *io.PipeReader
	cancel context.CancelFunc
}

func (b *pipeBody) Read(p []byte) (int, error) { return b.pr.Read(p) }

func (b *pipeBody) Close() error {
	b.cancel()
	return b.pr.CloseWithError(errors.New("a2a: response body closed"))
}
