package wsclient_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/agentclient/wsclient"
)

type closeBody struct {
	io.Reader
	closed atomic.Bool
}

func (b *closeBody) Close() error {
	b.closed.Store(true)
	return nil
}

// RoundTrip closes a command's request body, as http.RoundTripper says,
// whether the command goes over the connection, fails to (here the server
// has no WebSocket endpoint), or goes to the fallback (a body that is not
// JSON, whose 400 is the API's).
func TestRequestBodiesAreClosed(t *testing.T) {
	t.Parallel()
	ts := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(ts.Close)
	tr, err := wsclient.New(ts.URL, wsclient.WithFallback(ts.Client().Transport), wsclient.WithDialClient(ts.Client()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tr.Close() })
	for _, body := range []string{`{"answers":[{"id":"a","approved":true}]}`, `not json`} {
		b := &closeBody{Reader: strings.NewReader(body)}
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, ts.URL+"/v1/runs/run_1/input", b)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := tr.RoundTrip(req)
		if err == nil {
			_ = resp.Body.Close()
		}
		if !b.closed.Load() {
			t.Errorf("the body %q was not closed (RoundTrip: %v)", body, err)
		}
	}
}
