// Package a2atest runs agentkit's own A2A server (package a2a) in process,
// over the agent API on a fake model, for tests of A2A clients: one agent,
// "helper", behind an API key, on an httptest server that records every
// request it gets.
package a2atest

import (
	"context"
	"iter"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/a2a"
	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/auth"
	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/llm/llmtest"
	"github.com/bsamanvith/agentkit/pkg/store/scoped"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

// Agent is the name of the server's agent.
const Agent = "helper"

// Key is the API key the server takes. It is long and unusual, so a test can
// look for it anywhere (a canary).
const Key = "a2atest-canary-key-7f3c9e1b5d2a4068"

// Server is the A2A server and what it was asked.
type Server struct {
	tb  testing.TB
	ts  *httptest.Server
	api *agentapi.Server
	srv *a2a.Server

	release   chan struct{}
	releaseMu sync.Once
	calls     atomic.Int64

	mu       sync.Mutex
	requests []Request
}

// Request is one request the server got.
type Request struct {
	Method, Path, Query string
	Header              http.Header
}

// model answers "wait ...": once Release is called, "released"; anything
// else: "echo: <prompt>".
type model struct{ s *Server }

func (m model) Generate(ctx context.Context, req llm.Request) iter.Seq2[llm.Event, error] {
	m.s.calls.Add(1)
	prompt := req.Messages[len(req.Messages)-1].Text()
	reply := llmtest.Text("echo: " + prompt)
	wait := strings.HasPrefix(prompt, "wait")
	if wait {
		reply = llmtest.Text("released")
	}
	inner := llmtest.NewFake(m.s.tb, reply).Generate(ctx, req)
	return func(yield func(llm.Event, error) bool) {
		if wait {
			select {
			case <-m.s.release:
			case <-ctx.Done():
				yield(llm.Event{}, ctx.Err())
				return
			}
		}
		for ev, err := range inner {
			if !yield(ev, err) {
				return
			}
		}
	}
}

// NewServer starts the server; it stops when the test ends.
func NewServer(tb testing.TB) *Server {
	tb.Helper()
	s := &Server{tb: tb, release: make(chan struct{})}
	none, err := tool.NewRegistry(nil)
	if err != nil {
		tb.Fatal(err)
	}
	h, err := harness.New(model{s}, none)
	if err != nil {
		tb.Fatal(err)
	}
	e, err := auth.KeyEntry(auth.APIKey{ID: "a", Tenant: "t", Scopes: []string{"*"}, Toolsets: []string{"*"}}, Key)
	if err != nil {
		tb.Fatal(err)
	}
	authn, err := auth.NewAPIKeys([]auth.APIKey{e})
	if err != nil {
		tb.Fatal(err)
	}
	s.api, err = agentapi.NewAgents([]agentapi.Agent{{Name: Agent, Description: "Answers questions, quickly.", Harness: h}},
		agentapi.WithStore(scoped.Memory(nil)), agentapi.WithAuthenticator(authn), agentapi.WithKeepAlive(0))
	if err != nil {
		tb.Fatal(err)
	}
	s.srv, err = a2a.New(s.api.Handler(), a2a.WithKeepAlive(0),
		a2a.WithSecuritySchemes(map[string]a2a.SecurityScheme{"bearer": {HTTPAuth: &a2a.HTTPAuthSecurityScheme{Scheme: "Bearer"}}}))
	if err != nil {
		tb.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/v1/", s.api.Handler())
	mux.Handle(a2a.PathPrefix, s.srv.Handler())
	mux.Handle(a2a.WellKnownPath, s.srv.Handler())
	s.ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.requests = append(s.requests, Request{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Header: r.Header.Clone()})
		s.mu.Unlock()
		mux.ServeHTTP(w, r)
	}))
	tb.Cleanup(func() {
		s.Release()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = s.srv.Shutdown(ctx)
		s.ts.Close()
		_ = s.api.Shutdown(ctx)
	})
	return s
}

// URL is the server's base URL.
func (s *Server) URL() string { return s.ts.URL }

// CardURL is the agent's card.
func (s *Server) CardURL() string {
	return s.ts.URL + a2a.PathPrefix + Agent + "/.well-known/agent-card.json"
}

// Client is an HTTP client of the server.
func (s *Server) Client() *http.Client { return s.ts.Client() }

// Release lets the "wait" runs answer.
func (s *Server) Release() { s.releaseMu.Do(func() { close(s.release) }) }

// ModelCalls is how many times the model was asked: one per task here.
func (s *Server) ModelCalls() int64 { return s.calls.Load() }

// Requests are the requests the server got so far.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Request, len(s.requests))
	for i, r := range s.requests {
		r.Header = r.Header.Clone()
		out[i] = r
	}
	return out
}
