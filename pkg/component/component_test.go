package component_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/internal/testkit"
	"github.com/bsamanvith/agentkit/pkg/component"
)

// events is what happened, in order, across the fakes and the test's
// requests ("shutdown a", "request done").
type events struct {
	mu   sync.Mutex
	list []string
}

func (e *events) add(s string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.list = append(e.list, s)
}

func (e *events) get() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.list)
}

// fake is a component whose readiness the test sets and whose shutdown it
// records.
type fake struct {
	name     string
	handler  http.Handler
	ev       *events
	ready    atomic.Pointer[error] // nil: ready
	shutdown func(ctx context.Context) error
	shutN    atomic.Int32
}

func newFake(name string, ev *events) *fake {
	return &fake{name: name, ev: ev, handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "%s %s", name, r.URL.Path)
	})}
}

func (f *fake) Name() string          { return f.name }
func (f *fake) Handler() http.Handler { return f.handler }

func (f *fake) Ready(ctx context.Context) error {
	if err := f.ready.Load(); err != nil {
		return *err
	}
	return ctx.Err()
}

func (f *fake) setReady(err error) {
	if err == nil {
		f.ready.Store(nil)
		return
	}
	f.ready.Store(&err)
}

func (f *fake) Shutdown(ctx context.Context) error {
	f.ev.add("shutdown " + f.name)
	f.shutN.Add(1)
	if f.shutdown != nil {
		return f.shutdown(ctx)
	}
	return nil
}

// server is a Runtime serving on a real loopback listener.
type server struct {
	url  string
	addr string
	stop func() error // cancels Serve's context and returns what Serve returned
}

// serve starts rt and checks, at the end of the test, the invariants of every
// run: Serve returns once its context is done, and every component was shut
// down exactly once.
func serve(t *testing.T, rt *component.Runtime, fakes ...*fake) *server {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- rt.Serve(ctx, ln) }()
	var once sync.Once
	var serveErr error
	stop := func() error {
		once.Do(func() {
			cancel()
			select {
			case serveErr = <-done:
			case <-time.After(30 * time.Second): // only a broken runtime gets here
				t.Fatal("Serve did not return after its context was done")
			}
		})
		return serveErr
	}
	t.Cleanup(func() {
		_ = stop()
		for _, f := range fakes {
			// Past the deadline the runtime does not wait for Shutdown calls
			// to start, so they may land after Serve returns.
			testkit.Eventually(t, 10*time.Second, func() bool { return f.shutN.Load() > 0 })
			if n := f.shutN.Load(); n != 1 {
				t.Errorf("component %s shut down %d times, want 1", f.name, n)
			}
		}
	})
	return &server{url: "http://" + ln.Addr().String(), addr: ln.Addr().String(), stop: stop}
}

func newRuntime(t *testing.T, mounts []component.Mount, opts ...component.Option) *component.Runtime {
	t.Helper()
	rt, err := component.New(mounts, opts...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return rt
}

func get(t *testing.T, client *http.Client, url string) (int, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(body)
}

type readiness struct {
	Status     string `json:"status"`
	Components []struct {
		Name   string `json:"name"`
		Status string `json:"status"`
		Reason string `json:"reason"`
	} `json:"components"`
}

func readyz(t *testing.T, url string) (int, readiness) {
	t.Helper()
	code, body := get(t, http.DefaultClient, url+"/readyz")
	var r readiness
	if err := json.Unmarshal([]byte(body), &r); err != nil {
		t.Fatalf("/readyz body %q: %v", body, err)
	}
	return code, r
}

// TestMountsOnlyGivenComponents: each component answers at its own pattern,
// unmounted paths are 404, and a component without a pattern is not served
// but still takes part in readiness and shutdown.
func TestMountsOnlyGivenComponents(t *testing.T) {
	t.Parallel()
	ev := &events{}
	a, b, worker := newFake("a", ev), newFake("b", ev), newFake("worker", ev)
	rt := newRuntime(t, []component.Mount{
		{Pattern: "/a/", Component: a},
		{Pattern: "/b/", Component: b},
		{Component: worker},
	})
	ts := httptest.NewServer(rt.Handler())
	t.Cleanup(ts.Close)

	for path, want := range map[string]string{"/a/x": "a /a/x", "/b/": "b /b/"} {
		if code, body := get(t, ts.Client(), ts.URL+path); code != http.StatusOK || body != want {
			t.Errorf("GET %s = %d %q, want 200 %q", path, code, body, want)
		}
	}
	for _, path := range []string{"/worker/", "/c/", "/"} {
		if code, _ := get(t, ts.Client(), ts.URL+path); code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", path, code)
		}
	}
	worker.setReady(errors.New("warming up"))
	if code, r := readyz(t, ts.URL); code != http.StatusServiceUnavailable || len(r.Components) != 3 {
		t.Errorf("/readyz = %d %+v, want 503 listing the three components", code, r)
	}
}

func TestNewRejects(t *testing.T) {
	t.Parallel()
	ev := &events{}
	nilHandler := newFake("nil-handler", ev)
	nilHandler.handler = nil
	for name, tc := range map[string]struct {
		mounts []component.Mount
		opts   []component.Option
	}{
		"no components":         {},
		"nil component":         {mounts: []component.Mount{{Pattern: "/a/"}}},
		"empty name":            {mounts: []component.Mount{{Pattern: "/a/", Component: newFake("", ev)}}},
		"duplicate name":        {mounts: []component.Mount{{Pattern: "/a/", Component: newFake("a", ev)}, {Pattern: "/b/", Component: newFake("a", ev)}}},
		"same pattern twice":    {mounts: []component.Mount{{Pattern: "/a/", Component: newFake("a", ev)}, {Pattern: "/a/", Component: newFake("b", ev)}}},
		"runtime's own pattern": {mounts: []component.Mount{{Pattern: "GET /readyz", Component: newFake("a", ev)}}},
		"malformed pattern":     {mounts: []component.Mount{{Pattern: "/a/{", Component: newFake("a", ev)}}},
		"nil handler":           {mounts: []component.Mount{{Pattern: "/a/", Component: nilHandler}}},
		"zero shutdown timeout": {mounts: []component.Mount{{Pattern: "/a/", Component: newFake("a", ev)}}, opts: []component.Option{component.WithShutdownTimeout(0)}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := component.New(tc.mounts, tc.opts...); !errors.Is(err, component.ErrInvalidConfig) {
				t.Errorf("New = %v, want ErrInvalidConfig", err)
			}
		})
	}
}

// TestHealthzAsSoonAsListening: /healthz answers 200 while no component is
// ready, over HTTP/1.1 and over cleartext HTTP/2 (h2c, for ConnectRPC).
func TestHealthzAsSoonAsListening(t *testing.T) {
	t.Parallel()
	ev := &events{}
	a := newFake("a", ev)
	a.setReady(errors.New("never ready"))
	srv := serve(t, newRuntime(t, []component.Mount{{Pattern: "/a/", Component: a}}), a)

	if code, _ := get(t, http.DefaultClient, srv.url+"/healthz"); code != http.StatusOK {
		t.Errorf("/healthz = %d, want 200", code)
	}
	h2 := new(http.Protocols)
	h2.SetUnencryptedHTTP2(true)
	h2c := &http.Client{Transport: &http.Transport{Protocols: h2}}
	t.Cleanup(h2c.CloseIdleConnections)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.url+"/healthz", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := h2c.Do(req)
	if err != nil {
		t.Fatalf("h2c GET /healthz: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.ProtoMajor != 2 {
		t.Errorf("h2c /healthz = %d over %s, want 200 over HTTP/2", resp.StatusCode, resp.Proto)
	}
	if code, _ := readyz(t, srv.url); code != http.StatusServiceUnavailable {
		t.Errorf("/readyz = %d while not ready, want 503", code)
	}
}

// TestReadyz: 503 with the reason until every component is ready, then 200;
// a degraded component counts as ready and its reason is shown.
func TestReadyz(t *testing.T) {
	t.Parallel()
	ev := &events{}
	a, b := newFake("a", ev), newFake("b", ev)
	a.setReady(errors.New("connecting to the store"))
	b.setReady(errors.New("loading toolsets"))
	ts := httptest.NewServer(newRuntime(t, []component.Mount{{Pattern: "/a/", Component: a}, {Pattern: "/b/", Component: b}}).Handler())
	t.Cleanup(ts.Close)

	type comp struct{ name, status, reason string }
	check := func(t *testing.T, wantCode int, wantStatus string, want ...comp) {
		t.Helper()
		code, r := readyz(t, ts.URL)
		var got []comp
		for _, c := range r.Components {
			got = append(got, comp{c.Name, c.Status, c.Reason})
		}
		if code != wantCode || r.Status != wantStatus || !slices.Equal(got, want) {
			t.Errorf("/readyz = %d %s %v, want %d %s %v", code, r.Status, got, wantCode, wantStatus, want)
		}
	}

	check(t, 503, "not ready", comp{"a", "not ready", "connecting to the store"}, comp{"b", "not ready", "loading toolsets"})
	a.setReady(nil)
	check(t, 503, "not ready", comp{"a", "ready", ""}, comp{"b", "not ready", "loading toolsets"})
	b.setReady(fmt.Errorf("%w: optional toolset gmail skipped", component.ErrDegraded))
	check(t, 200, "ready", comp{"a", "ready", ""}, comp{"b", "degraded", "degraded: optional toolset gmail skipped"})
	b.setReady(nil)
	check(t, 200, "ready", comp{"a", "ready", ""}, comp{"b", "ready", ""})
	a.setReady(errors.New("lost the store"))
	check(t, 503, "not ready", comp{"a", "not ready", "lost the store"}, comp{"b", "ready", ""})
}

func TestVersion(t *testing.T) {
	t.Parallel()
	ev := &events{}
	info := component.BuildInfo{Version: "1.2.3", Commit: "abc123", Date: "2026-10-11T00:00:00Z"}
	ts := httptest.NewServer(newRuntime(t, []component.Mount{{Pattern: "/a/", Component: newFake("a", ev)}}, component.WithBuildInfo(info)).Handler())
	t.Cleanup(ts.Close)
	code, body := get(t, ts.Client(), ts.URL+"/version")
	var got component.BuildInfo
	if err := json.Unmarshal([]byte(body), &got); err != nil || code != http.StatusOK || got != info {
		t.Errorf("/version = %d %q (%v), want %+v", code, body, err, info)
	}
}

// TestGracefulShutdown: once Serve's context is done, a request in flight
// completes (its context is not cancelled), and only then are the components
// shut down, in reverse order. Serve returns nil.
func TestGracefulShutdown(t *testing.T) {
	t.Parallel()
	ev := &events{}
	a, b, c := newFake("a", ev), newFake("b", ev), newFake("c", ev)
	started, release := make(chan struct{}), make(chan struct{})
	b.handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		select {
		case <-release:
			fmt.Fprint(w, "finished")
			ev.add("request done")
		case <-r.Context().Done(): // a real handler stops when its request is cancelled
			http.Error(w, "cancelled", http.StatusServiceUnavailable)
		}
	})
	srv := serve(t, newRuntime(t, []component.Mount{
		{Pattern: "/a/", Component: a}, {Pattern: "/b/", Component: b}, {Component: c},
	}), a, b, c)

	type result struct {
		code int
		body string
		err  error
	}
	resc := make(chan result, 1)
	go func() {
		resp, err := http.Get(srv.url + "/b/slow") //nolint:noctx // ends with the server
		if err != nil {
			resc <- result{err: err}
			return
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		resc <- result{resp.StatusCode, string(body), err}
	}()
	<-started
	stopped := make(chan error, 1)
	go func() { stopped <- srv.stop() }()
	// The shutdown has begun once the listener refuses new connections.
	testkit.Eventually(t, 10*time.Second, func() bool {
		conn, err := net.Dial("tcp", srv.addr)
		if err == nil {
			_ = conn.Close()
		}
		return err != nil
	})
	if got := ev.get(); len(got) != 0 {
		t.Errorf("before the request finished: %v, want nothing shut down", got)
	}
	close(release)

	if r := <-resc; r.err != nil || r.code != http.StatusOK || r.body != "finished" {
		t.Errorf("in-flight request = %d %q %v, want 200 finished", r.code, r.body, r.err)
	}
	if err := <-stopped; err != nil {
		t.Errorf("Serve = %v, want nil", err)
	}
	if got, want := ev.get(), []string{"request done", "shutdown c", "shutdown b", "shutdown a"}; !slices.Equal(got, want) {
		t.Errorf("events = %v, want %v", got, want)
	}
}

// TestShutdownDeadline: a request that never ends and a component whose
// Shutdown hangs cannot hold the process past the shutdown timeout. The
// other components are still shut down, and Serve reports the deadline.
func TestShutdownDeadline(t *testing.T) {
	t.Parallel()
	for _, stuck := range []string{"request", "component"} {
		t.Run(stuck, func(t *testing.T) {
			t.Parallel()
			ev := &events{}
			a, b, c := newFake("a", ev), newFake("b", ev), newFake("c", ev)
			hang := make(chan struct{})
			t.Cleanup(func() { close(hang) })
			started := make(chan struct{})
			if stuck == "request" {
				a.handler = http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
					close(started)
					<-hang // ignores its context
				})
			} else {
				close(started)
				b.shutdown = func(context.Context) error {
					<-hang // ignores its context
					return nil
				}
			}
			srv := serve(t, newRuntime(t, []component.Mount{
				{Pattern: "/a/", Component: a}, {Pattern: "/b/", Component: b}, {Pattern: "/c/", Component: c},
			}, component.WithShutdownTimeout(100*time.Millisecond)), a, b, c)
			if stuck == "request" {
				go func() {
					resp, err := http.Get(srv.url + "/a/") //nolint:noctx // ends with the server
					if err == nil {
						_ = resp.Body.Close()
					}
				}()
			}
			<-started

			err := srv.stop()
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("Serve = %v, want context.DeadlineExceeded", err)
			}
			if cutOff := errors.Is(err, component.ErrRequestsCutOff); cutOff != (stuck == "request") {
				t.Errorf("Serve = %v: ErrRequestsCutOff %v, want %v", err, cutOff, stuck == "request")
			}
			for _, f := range []*fake{a, b, c} {
				testkit.Eventually(t, 10*time.Second, func() bool { return f.shutN.Load() > 0 })
			}
			got := ev.get()
			if stuck == "request" {
				// Every Shutdown got an expired context, so they race.
				slices.Sort(got)
				if want := []string{"shutdown a", "shutdown b", "shutdown c"}; !slices.Equal(got, want) {
					t.Errorf("events = %v, want %v in any order", got, want)
				}
			} else if want := []string{"shutdown c", "shutdown b", "shutdown a"}; !slices.Equal(got, want) {
				t.Errorf("events = %v, want %v", got, want)
			}
		})
	}
}

// TestShutdownErrors: a component that fails to shut down does not stop the
// others, and its error reaches the caller.
func TestShutdownErrors(t *testing.T) {
	t.Parallel()
	ev := &events{}
	a, b := newFake("a", ev), newFake("b", ev)
	errFlush := errors.New("flush failed")
	b.shutdown = func(context.Context) error { return errFlush }
	srv := serve(t, newRuntime(t, []component.Mount{{Pattern: "/a/", Component: a}, {Pattern: "/b/", Component: b}}), a, b)
	if err := srv.stop(); !errors.Is(err, errFlush) {
		t.Errorf("Serve = %v, want %v", err, errFlush)
	}
	if got, want := ev.get(), []string{"shutdown b", "shutdown a"}; !slices.Equal(got, want) {
		t.Errorf("events = %v, want %v", got, want)
	}
}

// TestListenerFailure: when the listener fails, Serve returns its error and
// the components are still shut down.
func TestListenerFailure(t *testing.T) {
	t.Parallel()
	ev := &events{}
	a := newFake("a", ev)
	rt := newRuntime(t, []component.Mount{{Pattern: "/a/", Component: a}})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_ = ln.Close()
	done := make(chan error, 1)
	go func() { done <- rt.Serve(t.Context(), ln) }()
	select {
	case err := <-done:
		if !errors.Is(err, net.ErrClosed) {
			t.Errorf("Serve = %v, want net.ErrClosed", err)
		}
	case <-time.After(30 * time.Second): // only a broken runtime gets here
		t.Fatal("Serve did not return when its listener failed")
	}
	if got := ev.get(); !slices.Equal(got, []string{"shutdown a"}) {
		t.Errorf("events = %v, want [shutdown a]", got)
	}
	if err := rt.Serve(t.Context(), ln); err == nil {
		t.Error("second Serve = nil, want an error")
	}
}
