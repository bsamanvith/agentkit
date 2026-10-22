package client_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/a2a/a2atest"
	"github.com/bsamanvith/agentkit/pkg/a2a/client"
	"github.com/bsamanvith/agentkit/pkg/clock"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

const keyEnv = "HELPER_A2A_TOKEN"

// output is a tool's output.
type output struct {
	Notice    string `json:"notice"`
	Agent     string `json:"agent"`
	TaskID    string `json:"task_id"`
	ContextID string `json:"context_id"`
	State     string `json:"state"`
	TimedOut  bool   `json:"timed_out"`
	Answer    string `json:"answer"`
	Message   string `json:"message"`
}

// logs is a logger that keeps every line, debug included.
type logs struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *logs) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *logs) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

func (l *logs) logger() *slog.Logger {
	return slog.New(slog.NewTextHandler(l, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// env is a LookupEnv with the server's key.
func env(name string) (string, bool) {
	if name == keyEnv {
		return a2atest.Key, true
	}
	return "", false
}

type buildOpts struct {
	hc    *http.Client
	clock clock.Clock
	log   *slog.Logger
}

// buildTools builds the toolset from settings.
func buildTools(t *testing.T, settings string, o buildOpts) ([]tool.Tool, error) {
	t.Helper()
	opts := []client.Option{client.WithLookupEnv(env)}
	if o.hc != nil {
		opts = append(opts, client.WithHTTPClient(o.hc))
	}
	if o.clock != nil {
		opts = append(opts, client.WithClock(o.clock))
	}
	if o.log != nil {
		opts = append(opts, client.WithLogger(o.log))
	}
	return client.Factory(opts...).Build(t.Context(), json.RawMessage(settings))
}

// helperTool is the ask_helper tool of the server's agent.
func helperTool(t *testing.T, s *a2atest.Server, extra string, o buildOpts) tool.Tool {
	t.Helper()
	if o.hc == nil {
		o.hc = s.Client()
	}
	tools, err := buildTools(t, `{"agents": [{"name": "helper", "card_url": "`+s.CardURL()+`", "token_env": "`+keyEnv+`"`+extra+`}]}`, o)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 1 {
		t.Fatalf("got %d tools, want 1", len(tools))
	}
	return tools[0]
}

// ask calls a tool and decodes its output.
func ask(ctx context.Context, t *testing.T, tl tool.Tool, input string) (output, error) {
	t.Helper()
	raw, err := tl.Invoke(ctx, json.RawMessage(input))
	if err != nil {
		return output{}, err
	}
	var out output
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("output %s: %v", raw, err)
	}
	if strings.Contains(string(raw), a2atest.Key) {
		t.Errorf("the key is in the output: %s", raw)
	}
	return out, nil
}

// checkKeyOnlyInAuthorization: every request the server got carried the
// key in its Authorization header and nowhere else (the canary invariant).
func checkKeyOnlyInAuthorization(t *testing.T, s *a2atest.Server) {
	t.Helper()
	for _, r := range s.Requests() {
		if got := r.Header.Get("Authorization"); got != "Bearer "+a2atest.Key {
			t.Errorf("%s %s: Authorization %q, want the bearer key", r.Method, r.Path, got)
		}
		if strings.Contains(r.Path+r.Query, a2atest.Key) {
			t.Errorf("%s %s: the key is in the URL", r.Method, r.Path)
		}
		for name, vs := range r.Header {
			if name == "Authorization" {
				continue
			}
			for _, v := range vs {
				if strings.Contains(v, a2atest.Key) {
					t.Errorf("%s %s: the key is in header %s", r.Method, r.Path, name)
				}
			}
		}
		if got := r.Header.Get("A2A-Version"); got != "1.0" {
			t.Errorf("%s %s: A2A-Version %q, want 1.0", r.Method, r.Path, got)
		}
	}
}
