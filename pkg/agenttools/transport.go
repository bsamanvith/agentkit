package agenttools

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
)

// credentialKey is the context key of the forwarded Authorization header.
type credentialKey struct{}

// ForwardCredentials returns next with the Authorization header of each
// request kept in its context, for the agentkit tools to send to the agent
// API. Put it in front of the authentication middleware, which removes the
// header from the request it passes on. Only this package reads what it
// keeps.
func ForwardCredentials(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if v := r.Header.Values("Authorization"); len(v) > 0 {
			r = r.WithContext(context.WithValue(r.Context(), credentialKey{}, v))
		}
		next.ServeHTTP(w, r)
	})
}

// forwarding is the transport of the tools' agent API client: it sets the
// Authorization header ForwardCredentials kept, and only that one.
type forwarding struct{ base http.RoundTripper }

func (f forwarding) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Del("Authorization")
	if v, ok := req.Context().Value(credentialKey{}).([]string); ok {
		req.Header["Authorization"] = append([]string(nil), v...)
	}
	return f.base.RoundTrip(req)
}

// inProcess is a RoundTripper that serves each request with a handler, in
// this process, streaming the response: the body is read as the handler
// writes it, which an event stream needs. Closing the body cancels the
// request's context and makes the handler's writes fail.
type inProcess struct{ h http.Handler }

func (t inProcess) RoundTrip(req *http.Request) (*http.Response, error) {
	ctx, cancel := context.WithCancel(req.Context())
	r := req.Clone(ctx)
	r.RequestURI = req.URL.RequestURI()
	if r.Body == nil {
		r.Body = http.NoBody
	}
	pr, pw := io.Pipe()
	w := &pipeWriter{header: http.Header{}, pw: pw, ready: make(chan struct{})}
	served := make(chan struct{})
	go func() {
		defer close(served)
		defer func() {
			if v := recover(); v != nil {
				w.fail(fmt.Errorf("agenttools: the agent API handler panicked: %v", v))
				return
			}
			w.finish()
		}()
		t.h.ServeHTTP(w, r)
	}()
	select {
	case <-w.ready:
	case <-ctx.Done():
		// The handler sees ctx done too; its writes fail from now on.
		_ = pr.CloseWithError(ctx.Err())
		cancel()
		return nil, ctx.Err()
	}
	if w.err != nil {
		cancel()
		<-served
		return nil, w.err
	}
	return &http.Response{
		Status:        fmt.Sprintf("%d %s", w.status, http.StatusText(w.status)),
		StatusCode:    w.status,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        w.sent,
		Body:          &pipeBody{pr: pr, cancel: cancel, served: served},
		ContentLength: -1,
		Request:       req,
	}, nil
}

// pipeWriter is the handler's http.ResponseWriter (and http.Flusher): the
// status and headers are sent once, at the first WriteHeader, Write or
// Flush, and the body goes through the pipe. Only the handler's goroutine
// calls it; RoundTrip reads status, sent and err after ready is closed.
type pipeWriter struct {
	header http.Header
	pw     *io.PipeWriter
	ready  chan struct{}
	once   sync.Once

	status int
	sent   http.Header
	err    error // set instead of a status when the handler panicked first
}

func (w *pipeWriter) Header() http.Header { return w.header }

func (w *pipeWriter) WriteHeader(code int) {
	if code >= 100 && code < 200 {
		return // informational: nothing to pass on
	}
	w.once.Do(func() {
		w.status, w.sent = code, w.header.Clone()
		close(w.ready)
	})
}

func (w *pipeWriter) Write(b []byte) (int, error) {
	w.WriteHeader(http.StatusOK)
	return w.pw.Write(b)
}

// Flush sends the headers if they were not; the body is never buffered.
func (w *pipeWriter) Flush() { w.WriteHeader(http.StatusOK) }

// finish ends the response of a handler that returned.
func (w *pipeWriter) finish() {
	w.WriteHeader(http.StatusOK)
	_ = w.pw.Close()
}

// fail ends the response of a handler that panicked: an error from
// RoundTrip if nothing was sent yet, else a body that breaks.
func (w *pipeWriter) fail(err error) {
	w.once.Do(func() {
		w.err = err
		close(w.ready)
	})
	_ = w.pw.CloseWithError(err)
}

// pipeBody is the response body: closing it stops the handler and waits
// for it to return, so no goroutine outlives the response.
type pipeBody struct {
	pr     *io.PipeReader
	cancel context.CancelFunc
	served chan struct{}
	once   sync.Once
}

func (b *pipeBody) Read(p []byte) (int, error) { return b.pr.Read(p) }

func (b *pipeBody) Close() error {
	b.once.Do(func() {
		b.cancel()
		_ = b.pr.CloseWithError(errBodyClosed)
		<-b.served
	})
	return nil
}

var errBodyClosed = errors.New("agenttools: response body closed")
