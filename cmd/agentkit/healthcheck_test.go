package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/component"
)

// probe is a component whose readiness the test sets.
type probe struct{ ready error }

func (p probe) Name() string                       { return "probe" }
func (p probe) Handler() http.Handler              { return http.NotFoundHandler() }
func (p probe) Ready(context.Context) error        { return p.ready }
func (p probe) Shutdown(ctx context.Context) error { return nil }

// runtimeServer serves the real component runtime (/healthz, /readyz) with
// one component whose Ready returns ready, and returns its port.
func runtimeServer(t *testing.T, ready error) string {
	t.Helper()
	rt, err := component.New([]component.Mount{{Pattern: "/probe/", Component: probe{ready}}})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(rt.Handler())
	t.Cleanup(srv.Close)
	return port(t, srv.Listener.Addr())
}

func port(t *testing.T, a net.Addr) string {
	t.Helper()
	_, p, err := net.SplitHostPort(a.String())
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// healthcheck runs agentkit healthcheck with AGENTKIT_ADDR=addr. Whatever the
// outcome, it checks what holds for every run: exit 0 prints nothing, exit 1
// says why on stderr, and the command returns well within its timeout.
func healthcheck(t *testing.T, addr string, args ...string) int {
	t.Helper()
	var stdout, stderr bytes.Buffer
	c := cli{stdout: &stdout, stderr: &stderr, getenv: func(k string) string {
		if k == envAddr {
			return addr
		}
		return ""
	}}
	done := make(chan int, 1)
	go func() { done <- c.main(t.Context(), append([]string{"healthcheck"}, args...)) }()
	var code int
	select {
	case code = <-done:
	case <-time.After(10 * time.Second): // 5x the default timeout
		t.Fatalf("agentkit healthcheck %q with %s=%s did not return within 10s", args, envAddr, addr)
	}
	switch {
	case code == 0 && stdout.Len()+stderr.Len() > 0:
		t.Errorf("agentkit healthcheck %q exited 0 but printed:\n%s%s", args, &stdout, &stderr)
	case code == 1 && stderr.Len() == 0:
		t.Errorf("agentkit healthcheck %q exited 1 without saying why", args)
	}
	return code
}

// TestHealthcheck: exit 0 exactly when the server on $AGENTKIT_ADDR is up
// (and, with -ready, ready), whatever form the listen address takes.
func TestHealthcheck(t *testing.T) {
	t.Parallel()
	ready := runtimeServer(t, nil)
	degraded := runtimeServer(t, fmt.Errorf("optional toolset failed: %w", component.ErrDegraded))
	unready := runtimeServer(t, errors.New("store unreachable"))

	// A port nothing listens on.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	down := port(t, ln.Addr())
	ln.Close()

	// A port where something other than agentkit answers.
	other := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(other.Close)
	wrong := port(t, other.Listener.Addr())

	for name, tc := range map[string]struct {
		addr string
		args []string
		code int
	}{
		"up":                          {addr: "127.0.0.1:" + ready, code: 0},
		"up, ready":                   {addr: "127.0.0.1:" + ready, args: []string{"-ready"}, code: 0},
		"up, listening on all (:p)":   {addr: ":" + ready, code: 0},
		"up, listening on 0.0.0.0":    {addr: "0.0.0.0:" + ready, args: []string{"-ready"}, code: 0},
		"degraded counts as ready":    {addr: "127.0.0.1:" + degraded, args: []string{"-ready"}, code: 0},
		"unready but live":            {addr: "127.0.0.1:" + unready, code: 0},
		"unready":                     {addr: "127.0.0.1:" + unready, args: []string{"-ready"}, code: 1},
		"down":                        {addr: "127.0.0.1:" + down, code: 1},
		"down, ready":                 {addr: ":" + down, args: []string{"-ready"}, code: 1},
		"wrong port":                  {addr: "127.0.0.1:" + wrong, code: 1},
		"no fixed port":               {addr: "127.0.0.1:0", code: 1},
		"unparsable address":          {addr: "localhost", code: 1},
		"positional argument":         {addr: "127.0.0.1:" + ready, args: []string{"extra"}, code: 2},
		"non-positive timeout":        {addr: "127.0.0.1:" + ready, args: []string{"-timeout", "0s"}, code: 2},
		"timeout flag still succeeds": {addr: "127.0.0.1:" + ready, args: []string{"-timeout", "5s"}, code: 0},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if code := healthcheck(t, tc.addr, tc.args...); code != tc.code {
				t.Errorf("agentkit healthcheck %q with %s=%s = exit %d, want %d", tc.args, envAddr, tc.addr, code, tc.code)
			}
		})
	}
}

// TestHealthcheckGivesUp: a server that accepts the request and never answers
// fails the check after the default timeout instead of hanging the probe.
func TestHealthcheckGivesUp(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) }) // runs first, so Close does not wait on a stuck handler
	start := time.Now()
	if code := healthcheck(t, srv.Listener.Addr().String()); code != 1 {
		t.Fatalf("agentkit healthcheck against a server that never answers = exit %d, want 1", code)
	}
	if d := time.Since(start); d < defaultHealthTimeout/2 {
		t.Errorf("gave up after %s, before the %s timeout", d, defaultHealthTimeout)
	}
}
