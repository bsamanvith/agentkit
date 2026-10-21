package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

func envOf(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

// serve loads the shipped scripts, answers on its address, passes the
// healthcheck, logs each request, and stops cleanly when its context ends.
func TestServeAndHealthcheck(t *testing.T) {
	t.Parallel()
	env := envOf(map[string]string{
		envDir:  filepath.Join("..", "..", "deploy", "local", "fakellm"),
		envAddr: "127.0.0.1:0",
	})
	ctx, cancel := context.WithCancel(t.Context())
	addrc := make(chan string, 1)
	var logs bytes.Buffer // slog serializes writes; read after serve returns
	done := make(chan error, 1)
	go func() { done <- run(ctx, []string{"serve"}, env, io.Discard, &logs, func(a string) { addrc <- a }) }()
	var addr string
	select {
	case addr = <-addrc:
	case err := <-done:
		t.Fatal(err)
	}

	if err := run(t.Context(), []string{"healthcheck"}, envOf(map[string]string{envShared: addr}), io.Discard, io.Discard, nil); err != nil {
		t.Errorf("healthcheck: %v", err)
	}
	body := `{"model":"m","max_tokens":10,"messages":[{"role":"user","content":"hello"}]}`
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "http://"+addr+"/v1/messages", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !bytes.Contains(got, []byte("Hello from fakellm")) {
		t.Errorf("%d %s", resp.StatusCode, got)
	}

	cancel()
	if err := <-done; err != nil {
		t.Errorf("serve: %v", err)
	}
	if !strings.Contains(logs.String(), `msg="fakellm request" script=default`) {
		t.Errorf("request not logged:\n%s", logs.String())
	}
	if err := run(t.Context(), []string{"healthcheck"}, envOf(map[string]string{envAddr: addr}), io.Discard, io.Discard, nil); err == nil {
		t.Error("healthcheck passed with the server stopped")
	}
}

// Bad configuration fails at startup, not on the first request.
func TestServeRefusesBadConfig(t *testing.T) {
	t.Parallel()
	shipped := filepath.Join("..", "..", "deploy", "local", "fakellm")
	for name, env := range map[string]map[string]string{
		"missing dir":    {envDir: filepath.Join(t.TempDir(), "nope"), envAddr: "127.0.0.1:0"},
		"missing script": {envDir: shipped, envScript: "nope", envAddr: "127.0.0.1:0"},
	} {
		if err := run(t.Context(), nil, envOf(env), io.Discard, io.Discard, func(string) { t.Errorf("%s: serving", name) }); err == nil {
			t.Errorf("%s: started", name)
		}
	}
	if err := run(t.Context(), []string{"frobnicate"}, envOf(nil), io.Discard, io.Discard, nil); !errors.Is(err, errUsage) {
		t.Errorf("unknown command: %v", err)
	}
	var out bytes.Buffer
	if err := run(t.Context(), []string{"version"}, envOf(nil), &out, io.Discard, nil); err != nil || !strings.HasPrefix(out.String(), "fakellm ") {
		t.Errorf("version: %v %q", err, out.String())
	}
}
