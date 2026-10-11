package component_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"

	"github.com/bsamanvith/agentkit/pkg/component"
)

// greeter is a minimal component: a handler, readiness, and nothing to
// release on shutdown.
type greeter struct{ warm bool }

func (g *greeter) Name() string { return "greeter" }

func (g *greeter) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, "hello") })
}

func (g *greeter) Ready(context.Context) error {
	if !g.warm {
		return errors.New("warming up")
	}
	return nil
}

func (g *greeter) Shutdown(context.Context) error { return nil }

func Example() {
	g := &greeter{}
	rt, err := component.New([]component.Mount{{Pattern: "/greet/", Component: g}},
		component.WithBuildInfo(component.BuildInfo{Version: "1.0.0"}))
	if err != nil {
		fmt.Println(err)
		return
	}
	// In a binary: rt.Serve(ctx, listener), with ctx cancelled on SIGTERM.
	h := rt.Handler()
	for _, path := range []string{"/healthz", "/readyz", "/greet/"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		fmt.Print(rec.Code, " ", rec.Body.String())
		if path == "/greet/" {
			fmt.Println()
		}
	}
	g.warm = true
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	fmt.Print(rec.Code, " ", rec.Body.String())
	// Output:
	// 200 {"status":"ok"}
	// 503 {"components":[{"name":"greeter","status":"not ready","reason":"warming up"}],"status":"not ready"}
	// 200 hello
	// 200 {"components":[{"name":"greeter","status":"ready"}],"status":"ready"}
}
