package rpc_test

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"iter"
	"net"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/rpc"
)

type (
	waitReq  struct{ For time.Duration }
	waitResp struct{ Text string }
)

var (
	wait = rpc.Unary[waitReq, waitResp]{Name: "live/Wait"}
	big  = rpc.Stream[waitReq, waitResp]{Name: "live/Big"}
)

// liveServer serves Wait, which is silent for For (or until its context
// ends, reported on stopped) and then answers, and Big, which sends 64 KiB
// messages until it cannot.
func liveServer(t *testing.T, stopped chan<- error, opts ...rpc.ServerOption) string {
	t.Helper()
	s := rpc.NewServer(opts...)
	wait.Handle(s, func(ctx context.Context, r waitReq) (waitResp, error) {
		select {
		case <-time.After(r.For):
			return waitResp{Text: strings.Repeat("x", 1000)}, nil
		case <-ctx.Done():
			stopped <- ctx.Err()
			return waitResp{}, ctx.Err()
		}
	})
	big.Handle(s, func(ctx context.Context, _ waitReq) iter.Seq2[waitResp, error] {
		return func(yield func(waitResp, error) bool) {
			msg := waitResp{Text: strings.Repeat("y", 64<<10)}
			for yield(msg, nil) {
			}
			stopped <- ctx.Err()
		}
	})
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	return srv.URL
}

func dial(t *testing.T, url string, opts ...rpc.DialOption) *rpc.Client {
	t.Helper()
	c, err := rpc.Dial(url, opts...)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// A call silent for longer than the client's idle timeout lives on the
// server's pings.
func TestPingsKeepASilentCallAlive(t *testing.T) {
	t.Parallel()
	url := liveServer(t, make(chan error, 1), rpc.WithPingInterval(10*time.Millisecond))
	c := dial(t, url, rpc.WithIdleTimeout(100*time.Millisecond))
	got, err := wait.Call(t.Context(), c, waitReq{For: 400 * time.Millisecond})
	if err != nil || len(got.Text) != 1000 {
		t.Fatalf("Wait = %d bytes, %v; want the answer", len(got.Text), err)
	}
}

// Without pings, a silent server is given up on after the idle timeout, as
// unavailable, and its handler is canceled.
func TestIdleTimeoutEndsAStalledCall(t *testing.T) {
	t.Parallel()
	stopped := make(chan error, 1)
	url := liveServer(t, stopped, rpc.WithPingInterval(-1))
	c := dial(t, url, rpc.WithIdleTimeout(50*time.Millisecond))
	start := time.Now()
	_, err := wait.Call(t.Context(), c, waitReq{For: time.Hour})
	if !errors.Is(err, rpc.ErrUnavailable) || !strings.Contains(err.Error(), "sent nothing") {
		t.Fatalf("Wait = %v, want ErrUnavailable for a stall", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("the stall was noticed after %v", d)
	}
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Error("the handler was not canceled")
	}
}

// The idle timeout does not cut a call whose caller takes its time with each
// message: only waiting on the server counts.
func TestIdleTimeoutIgnoresTheCallersOwnTime(t *testing.T) {
	t.Parallel()
	url := liveServer(t, make(chan error, 1), rpc.WithPingInterval(-1))
	c := dial(t, url, rpc.WithIdleTimeout(200*time.Millisecond))
	n := 0
	for _, err := range big.Call(t.Context(), c, waitReq{}) {
		if err != nil {
			t.Fatalf("message %d: %v", n, err)
		}
		if n++; n == 3 {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func TestFrameOverTheLimitIsUnavailable(t *testing.T) {
	t.Parallel()
	url := liveServer(t, make(chan error, 1))
	c := dial(t, url, rpc.WithMaxFrameBytes(500))
	_, err := wait.Call(t.Context(), c, waitReq{})
	if !errors.Is(err, rpc.ErrUnavailable) || !strings.Contains(err.Error(), "over 500 bytes") {
		t.Fatalf("Wait = %v, want ErrUnavailable for a frame over the limit", err)
	}
}

// A caller that stops reading is cut off after the write timeout, and the
// handler stops, instead of blocking on the write forever.
func TestWriteTimeoutCutsACallerThatStopsReading(t *testing.T) {
	t.Parallel()
	stopped := make(chan error, 1)
	url := liveServer(t, stopped, rpc.WithWriteTimeout(50*time.Millisecond), rpc.WithPingInterval(-1))
	conn, err := net.Dial("tcp", strings.TrimPrefix(url, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if tc, ok := conn.(*net.TCPConn); ok {
		_ = tc.SetReadBuffer(4096)
	}
	fmt.Fprintf(conn, "POST %s HTTP/1.1\r\nHost: x\r\nContent-Type: application/json\r\nContent-Length: 2\r\n\r\n{}", rpc.PathPrefix+big.Name)
	if _, err := bufio.NewReader(conn).ReadString('\n'); err != nil { // the status line; then nothing more
		t.Fatal(err)
	}
	select {
	case <-stopped:
	case <-time.After(10 * time.Second):
		t.Fatal("the handler is still sending to a caller that stopped reading")
	}
}
