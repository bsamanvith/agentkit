package rpc_test

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/rpc"
)

// The test service: a unary method, a stream, and the errors they report.

var (
	errOdd   = errors.New("odd number")
	errLarge = errors.New("number too large")
	codes    = []rpc.Code{{Name: "calc.odd", Err: errOdd}, {Name: "calc.large", Err: errLarge}}
)

type (
	addReq   struct{ A, B int }
	addResp  struct{ Sum int }
	countReq struct{ To int }
	countMsg struct{ N int }
	detail   struct{ Done int }
)

var (
	add   = rpc.Unary[addReq, addResp]{Name: "calc/Add", Errors: codes}
	count = rpc.Stream[countReq, countMsg]{Name: "calc/Count", Errors: codes}
)

// service is what the handlers report, for the test to look at.
type service struct {
	countStopped chan error // the context error when Count stops early
	deadline     chan bool  // whether Add's context had a deadline
}

func newServer(opts ...rpc.ServerOption) (*rpc.Server, *service) {
	svc := &service{countStopped: make(chan error, 1), deadline: make(chan bool, 1)}
	s := rpc.NewServer(opts...)
	add.Handle(s, func(ctx context.Context, r addReq) (addResp, error) {
		_, ok := ctx.Deadline()
		select {
		case svc.deadline <- ok:
		default:
		}
		switch {
		case r.A < 0:
			panic("negative")
		case r.A == 1000:
			<-ctx.Done()
			return addResp{}, ctx.Err()
		case r.A > 100 && r.A%2 == 1:
			return addResp{}, fmt.Errorf("both: %w, %w", errLarge, errOdd)
		case r.A%2 == 1:
			return addResp{}, rpc.WithData(fmt.Errorf("a=%d: %w", r.A, errOdd), detail{Done: r.A})
		case r.A == 42:
			return addResp{}, errors.New("not one of the codes")
		}
		return addResp{Sum: r.A + r.B}, nil
	})
	count.Handle(s, func(ctx context.Context, r countReq) iter.Seq2[countMsg, error] {
		return func(yield func(countMsg, error) bool) {
			for n := 1; n <= r.To; n++ {
				if n == 13 && r.To == 20 { // TestErrorsKeepTheirIdentity
					yield(countMsg{}, errOdd)
					return
				}
				if !yield(countMsg{N: n}, nil) {
					// The caller stopped: its context ends soon after.
					select {
					case <-ctx.Done():
						svc.countStopped <- ctx.Err()
					case <-time.After(5 * time.Second):
						svc.countStopped <- nil
					}
					return
				}
			}
		}
	})
	return s, svc
}

// transports runs a test over HTTP and in process: they must behave the same.
func transports(t *testing.T, test func(t *testing.T, conn rpc.Conn, svc *service)) {
	t.Run("HTTP", func(t *testing.T) {
		t.Parallel()
		s, svc := newServer()
		srv := httptest.NewServer(s)
		t.Cleanup(srv.Close)
		conn, err := rpc.Dial(srv.URL)
		if err != nil {
			t.Fatal(err)
		}
		test(t, conn, svc)
	})
	t.Run("Pipe", func(t *testing.T) {
		t.Parallel()
		s, svc := newServer()
		test(t, rpc.Pipe(s), svc)
	})
}

func TestUnaryAnswers(t *testing.T) {
	transports(t, func(t *testing.T, conn rpc.Conn, _ *service) {
		got, err := add.Call(t.Context(), conn, addReq{A: 2, B: 3})
		if err != nil || got.Sum != 5 {
			t.Fatalf("Add(2, 3) = %+v, %v; want 5", got, err)
		}
	})
}

func TestStreamAnswersInOrder(t *testing.T) {
	transports(t, func(t *testing.T, conn rpc.Conn, _ *service) {
		var got []int
		for m, err := range count.Call(t.Context(), conn, countReq{To: 5}) {
			if err != nil {
				t.Fatalf("Count: %v", err)
			}
			got = append(got, m.N)
		}
		if want := []int{1, 2, 3, 4, 5}; !slices.Equal(got, want) {
			t.Errorf("Count(5) = %v, want %v", got, want)
		}
	})
}

func TestErrorsKeepTheirIdentity(t *testing.T) {
	transports(t, func(t *testing.T, conn rpc.Conn, _ *service) {
		_, err := add.Call(t.Context(), conn, addReq{A: 3})
		if !errors.Is(err, errOdd) || errors.Is(err, errLarge) {
			t.Errorf("err = %v, want errOdd only", err)
		}
		if err == nil || err.Error() != "a=3: odd number" {
			t.Errorf("message = %v, want the handler's", err)
		}

		_, err = add.Call(t.Context(), conn, addReq{A: 101})
		if !errors.Is(err, errOdd) || !errors.Is(err, errLarge) {
			t.Errorf("err = %v, want both errOdd and errLarge", err)
		}

		var n int
		for _, err = range count.Call(t.Context(), conn, countReq{To: 20}) {
			if err == nil {
				n++
			}
		}
		if n != 12 || !errors.Is(err, errOdd) {
			t.Errorf("Count(20) = %d messages then %v, want 12 then errOdd", n, err)
		}
	})
}

func TestUncodedErrorKeepsItsMessageOnly(t *testing.T) {
	transports(t, func(t *testing.T, conn rpc.Conn, _ *service) {
		_, err := add.Call(t.Context(), conn, addReq{A: 42})
		var we *rpc.Error
		if !errors.As(err, &we) || we.Message != "not one of the codes" || len(we.Codes) != 0 {
			t.Fatalf("err = %#v, want an *rpc.Error with the message and no codes", err)
		}
		if errors.Is(err, errOdd) || errors.Is(err, rpc.ErrUnavailable) {
			t.Errorf("err = %v matches a code it does not have", err)
		}
	})
}

func TestErrorCarriesData(t *testing.T) {
	transports(t, func(t *testing.T, conn rpc.Conn, _ *service) {
		_, err := add.Call(t.Context(), conn, addReq{A: 7})
		var we *rpc.Error
		var d detail
		if !errors.As(err, &we) || !we.Data(&d) || d.Done != 7 {
			t.Fatalf("err = %v, data %+v; want detail{Done: 7}", err, d)
		}
	})
}

func TestUnknownMethod(t *testing.T) {
	other := rpc.Unary[addReq, addResp]{Name: "calc/Multiply"}
	transports(t, func(t *testing.T, conn rpc.Conn, _ *service) {
		_, err := other.Call(t.Context(), conn, addReq{})
		if !errors.Is(err, rpc.ErrUnknownMethod) {
			t.Errorf("err = %v, want ErrUnknownMethod", err)
		}
	})
}

func TestPanicIsInternal(t *testing.T) {
	transports(t, func(t *testing.T, conn rpc.Conn, _ *service) {
		_, err := add.Call(t.Context(), conn, addReq{A: -1})
		if !errors.Is(err, rpc.ErrInternal) {
			t.Errorf("err = %v, want ErrInternal", err)
		}
	})
}

func TestCallerStoppingCancelsTheHandler(t *testing.T) {
	transports(t, func(t *testing.T, conn rpc.Conn, svc *service) {
		for m, err := range count.Call(t.Context(), conn, countReq{To: 1 << 20}) {
			if err != nil {
				t.Fatal(err)
			}
			if m.N == 3 {
				break
			}
		}
		if err := <-svc.countStopped; !errors.Is(err, context.Canceled) {
			t.Errorf("handler's context = %v, want canceled once the caller stopped", err)
		}
	})
}

func TestDeadlineReachesTheHandler(t *testing.T) {
	transports(t, func(t *testing.T, conn rpc.Conn, svc *service) {
		ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
		defer cancel()
		_, err := add.Call(ctx, conn, addReq{A: 1000})
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("err = %v, want DeadlineExceeded", err)
		}
		if !<-svc.deadline {
			t.Error("the handler's context had no deadline")
		}
	})
}

func TestToken(t *testing.T) {
	t.Parallel()
	s, _ := newServer(rpc.WithToken("s3cret"))
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	for _, tc := range []struct {
		name  string
		token string
		ok    bool
	}{{"none", "", false}, {"wrong", "nope", false}, {"right", "s3cret", true}} {
		conn, err := rpc.Dial(srv.URL, rpc.WithBearer(tc.token))
		if err != nil {
			t.Fatal(err)
		}
		_, err = add.Call(t.Context(), conn, addReq{A: 2, B: 2})
		if tc.ok != (err == nil) || (!tc.ok && !errors.Is(err, rpc.ErrUnauthenticated)) {
			t.Errorf("%s token: err = %v", tc.name, err)
		}
	}
}

func TestBadRequest(t *testing.T) {
	t.Parallel()
	s, _ := newServer(rpc.WithMaxRequestBytes(64))
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	conn, err := rpc.Dial(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	big := rpc.Unary[string, addResp]{Name: "calc/Add"}
	if _, err := big.Call(t.Context(), conn, strings.Repeat("x", 100)); !errors.Is(err, rpc.ErrBadRequest) {
		t.Errorf("request over the limit: err = %v, want ErrBadRequest", err)
	}
	wrongType := rpc.Unary[string, addResp]{Name: "calc/Add"}
	if _, err := wrongType.Call(t.Context(), rpc.Pipe(s), "a string"); !errors.Is(err, rpc.ErrBadRequest) {
		t.Errorf("request of the wrong shape: err = %v, want ErrBadRequest", err)
	}
}

func TestNotThisProtocolIsUnavailable(t *testing.T) {
	t.Parallel()
	for name, h := range map[string]http.HandlerFunc{
		"an HTML page": func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "<h1>bad gateway</h1>", http.StatusBadGateway)
		},
		"a stream cut short": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", rpc.ContentType)
			fmt.Fprintln(w, `{"data":{"N":1}}`)
		},
	} {
		srv := httptest.NewServer(h)
		t.Cleanup(srv.Close)
		conn, err := rpc.Dial(srv.URL)
		if err != nil {
			t.Fatal(err)
		}
		var n int
		for _, err = range count.Call(t.Context(), conn, countReq{To: 3}) {
			if err == nil {
				n++
			}
		}
		if !errors.Is(err, rpc.ErrUnavailable) {
			t.Errorf("%s: err = %v after %d messages, want ErrUnavailable", name, err, n)
		}
	}
}

func TestServerDown(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	conn, err := rpc.Dial(url)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := add.Call(t.Context(), conn, addReq{}); !errors.Is(err, rpc.ErrUnavailable) {
		t.Errorf("err = %v, want ErrUnavailable", err)
	}
}

func TestDialRejectsBadURLs(t *testing.T) {
	t.Parallel()
	for _, u := range []string{"", "runner:8080", "ftp://runner", "http://", "http://a/?q=1", "http://u:p@a/"} {
		if _, err := rpc.Dial(u); err == nil {
			t.Errorf("Dial(%q) succeeded", u)
		}
	}
	if _, err := rpc.Dial("http://runner:8080/"); err != nil {
		t.Errorf("Dial: %v", err)
	}
}

func TestHandleRejectsBadOrDuplicateNames(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"Add", "calc/", "/Add", "calc/Add/x", "calc/A dd"} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("Handle(%q) did not panic", name)
				}
			}()
			rpc.Unary[addReq, addResp]{Name: name}.Handle(rpc.NewServer(), nil)
		}()
	}
	s, _ := newServer()
	defer func() {
		if recover() == nil {
			t.Error("registering calc/Add twice did not panic")
		}
	}()
	add.Handle(s, nil)
}

func TestMethodsListsWhatIsServed(t *testing.T) {
	t.Parallel()
	s, _ := newServer()
	got := s.Methods()
	slices.Sort(got)
	if want := []string{"calc/Add", "calc/Count"}; !slices.Equal(got, want) {
		t.Errorf("Methods() = %v, want %v", got, want)
	}
}
