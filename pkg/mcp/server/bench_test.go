package server_test

import (
	"bytes"
	"io"
	"net/http"
	"testing"
)

// BenchmarkCallTool measures a stateless tools/call over real loopback HTTP
// from many goroutines: `go test -bench CallTool -benchmem ./pkg/mcp/server`.
// Most of the allocation is the SDK decoding the JSON-RPC message (see T018
// Notes); this guards against our layer adding to it.
func BenchmarkCallTool(b *testing.B) {
	ts := start(b, twoToolsets(b))
	body := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo","arguments":{"text":"hi"}}}`)
	client := &http.Client{Transport: &http.Transport{MaxIdleConnsPerHost: 256}}
	b.Cleanup(client.CloseIdleConnections)

	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			req, _ := http.NewRequestWithContext(b.Context(), "POST", ts.URL+"/mcp/a", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Accept", "application/json, text/event-stream")
			resp, err := client.Do(req)
			if err != nil {
				b.Error(err)
				return
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				b.Errorf("status %d", resp.StatusCode)
				return
			}
		}
	})
}
