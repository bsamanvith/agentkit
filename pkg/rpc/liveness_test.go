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
	"sync"
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
//
// The idle timer is a fake the test moves: each time the client waits on the
// server again (it heard a ping), by three quarters of the timeout, four
// times, while the handler is silent; then the handler answers. Without the
// pings, or without the client counting them, the timer would fire.
func TestPingsKeepASilentCallAlive(t *testing.T) {
	t.Parallel()
	const idle = time.Hour // never reached in real time
	release := make(chan struct{})
	s := rpc.NewServer(rpc.WithPingInterval(time.Millisecond))
	hold.Handle(s, func(ctx context.Context, _ waitReq) (waitResp, error) {
		select {
		case <-release:
			return waitResp{Text: "done"}, nil
		case <-ctx.Done():
			return waitResp{}, ctx.Err()
		}
	})
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	timer := newFakeIdle()
	c := dial(t, srv.URL, rpc.WithIdleTimeout(idle), rpc.WithIdleTimers(timer.afterFunc))
	type result struct {
		resp waitResp
		err  error
	}
	done := make(chan result, 1)
	go func() {
		resp, err := hold.Call(t.Context(), c, waitReq{})
		done <- result{resp, err}
	}()
	waits := timer.waits(0)
	for range 4 {
		waits = timer.waits(waits)
		timer.advance(idle * 3 / 4)
	}
	close(release)
	if r := <-done; r.err != nil || r.resp.Text != "done" {
		t.Fatalf("Hold = %+v, %v; want the answer", r.resp, r.err)
	}
}

var hold = rpc.Unary[waitReq, waitResp]{Name: "live/Hold"}

// fakeIdle is a client's idle timer on a fake clock, which moves only on
// advance.
type fakeIdle struct {
	mu       sync.Mutex
	now      time.Duration
	deadline time.Duration
	armed    bool
	f        func()
	resets   int
	changed  chan struct{} // closed and replaced on every Reset
}

func newFakeIdle() *fakeIdle { return &fakeIdle{changed: make(chan struct{})} }

// afterFunc is the client's time.AfterFunc. A client makes one timer per
// call; this fake serves one call.
func (f *fakeIdle) afterFunc(d time.Duration, fn func()) rpc.IdleTimer {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.f = fn
	f.arm(d)
	return f
}

func (f *fakeIdle) arm(d time.Duration) {
	f.armed, f.deadline = true, f.now+d
	f.resets++
	close(f.changed)
	f.changed = make(chan struct{})
}

func (f *fakeIdle) Reset(d time.Duration) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	was := f.armed
	f.arm(d)
	return was
}

func (f *fakeIdle) Stop() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	was := f.armed
	f.armed = false
	return was
}

// waits returns once the client has started waiting on the server more than
// n times, with how many times it has. A client that never waits again
// (nothing arrives) is given up on after a while: the clock then moves
// anyway, and a correct client would have heard a ping long before.
func (f *fakeIdle) waits(n int) int {
	giveUp := time.After(10 * time.Second)
	for {
		f.mu.Lock()
		resets, changed := f.resets, f.changed
		f.mu.Unlock()
		if resets > n {
			return resets
		}
		select {
		case <-changed:
		case <-giveUp:
			return resets
		}
	}
}

// advance moves the clock by d, firing the timer if it falls due.
func (f *fakeIdle) advance(d time.Duration) {
	f.mu.Lock()
	f.now += d
	fire := f.armed && f.now >= f.deadline
	if fire {
		f.armed = false
	}
	fn := f.f
	f.mu.Unlock()
	if fire {
		fn()
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
	const idle = time.Hour // on a fake clock that moves only while the caller has a message
	timer := newFakeIdle()
	c := dial(t, url, rpc.WithIdleTimeout(idle), rpc.WithIdleTimers(timer.afterFunc))
	n := 0
	for _, err := range big.Call(t.Context(), c, waitReq{}) {
		if err != nil {
			t.Fatalf("message %d: %v", n, err)
		}
		if n++; n == 3 {
			break
		}
		timer.advance(2 * idle)
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
	// The status line, then nothing more. On a busy machine the first
	// frame's write can outlast the timeout before a byte is sent, and the
	// server cuts the caller then: the read ends with EOF, and the handler
	// must stop all the same.
	_, _ = bufio.NewReader(conn).ReadString('\n')
	select {
	case <-stopped:
	case <-time.After(10 * time.Second):
		t.Fatal("the handler is still sending to a caller that stopped reading")
	}
}
