package tavily_test

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bsamanvith/agentkit/connectors/tavily"
	"github.com/bsamanvith/agentkit/pkg/tool"
	"github.com/bsamanvith/agentkit/pkg/toolset"
)

// TestNilOptionsMeanDefaults: an option given nil leaves the default in
// place rather than a nil the toolset dereferences on its first call.
func TestNilOptionsMeanDefaults(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results": []}`))
	}))
	t.Cleanup(srv.Close)
	tools, err := tavily.Factory(
		tavily.WithBaseURL(srv.URL),
		tavily.WithHTTPClient(nil),
		tavily.WithLookupEnv(nil),
		tavily.WithClock(nil),
		tavily.WithLogger(nil),
	).Build(

		t.Context(), toolset.BuildInput{Settings: json.RawMessage(`{"auth": "keyless"}`)})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	reg, err := tool.NewRegistry(tools)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Invoke(t.Context(), "tavily_search", json.RawMessage(`{"query": "q"}`)); err != nil {
		t.Errorf("search with nil options: %v", err)
	}
}

// TestTransportErrorLogged: a request that gets no response fails the call
// without the transport's text, and logs that text (redacted) at Warn, so
// a DNS, TLS or proxy failure can be diagnosed.
func TestTransportErrorLogged(t *testing.T) {
	t.Parallel()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close() // hang up before any response
		}
	}()
	t.Cleanup(func() { _ = ln.Close() })
	r := newRig(t, rigOpts{extra: []tavily.Option{tavily.WithBaseURL("http://" + ln.Addr().String())}})
	_, err = r.call("tavily_search", `{"query": "q"}`)
	if err == nil {
		t.Fatal("search against a closed connection succeeded")
	}
	if strings.Contains(err.Error(), "EOF") || strings.Contains(err.Error(), "connection") {
		t.Errorf("the transport's text reached the model: %v", err)
	}
	var line struct {
		Level, Msg, Endpoint, Err string
	}
	found := false
	for _, s := range strings.Split(strings.TrimSpace(r.logs.String()), "\n") {
		if json.Unmarshal([]byte(s), &line) == nil && line.Msg == "tavily request failed" {
			found = true
			if line.Level != "WARN" || line.Endpoint == "" || line.Err == "" {
				t.Errorf("log line %s: want WARN with endpoint and err", s)
			}
		}
	}
	if !found {
		t.Errorf("no \"tavily request failed\" line in the log:\n%s", r.logs.String())
	}
}
