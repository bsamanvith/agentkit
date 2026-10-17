package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/rpc"
	"github.com/bsamanvith/agentkit/pkg/tool/toolrpc"
)

// A dependency that takes the connection and never answers fails startup
// after the wait, instead of hanging it.
func TestWaitForGivesUpOnAHungPeer(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	hung := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	t.Cleanup(hung.Close)
	t.Cleanup(func() { close(release) }) // first: Close waits for the handlers
	conn, err := rpc.Dial(hung.URL)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err = waitFor(t.Context(), 300*time.Millisecond, slog.New(slog.DiscardHandler), "toolsets", toolrpc.NewClient(conn).Toolsets)
	if !errors.Is(err, rpc.ErrUnavailable) || !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want ErrUnavailable after the wait", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("waitFor took %s with a 300ms wait", d)
	}
}
