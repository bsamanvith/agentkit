package parallel_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/connectors/parallel"
	"github.com/bsamanvith/agentkit/pkg/clock/clocktest"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

// FuzzDecodeSearch serves hostile bodies to parallel_search: whatever the
// upstream sends, the result is E10's shape or a classified error, never a
// panic, and never carries the key.
func FuzzDecodeSearch(f *testing.F) {
	fuzzDecode(f, "parallel_search", `{"query": "q", "max_results": 3}`, "search-defaults", "search-filters")
}

// FuzzDecodeExtract does the same for parallel_extract, in full-content mode
// (which falls back to excerpts when a page has no full content).
func FuzzDecodeExtract(f *testing.F) {
	fuzzDecode(f, "parallel_extract", `{"urls": ["https://a.example/", "https://b.example/"]}`, "extract-excerpts", "extract-full")
}

// fuzzStatuses are the statuses a fuzzed response can have; 429 is left out
// because its retry waits on the fake clock.
var fuzzStatuses = []int{200, 200, 200, 400, 422, 401, 500}

func fuzzDecode(f *testing.F, toolName, input string, cassettes ...string) {
	for _, c := range cassettes {
		f.Add(uint8(0), cassetteBody(f, c))
	}
	for _, seed := range []string{
		``, `{}`, `null`, `[]`, `{"results": null, "errors": null}`,
		`{"results": [{"url": "", "excerpts": null}]}`,
		`{"results": [{"url": "https://x/", "title": 7}]}`,
		`{"results": [{"url": "https://x/", "excerpts": ["` + canary + `"], "full_content": "é` + canary + `"}], "errors": [{"url": "u", "error_type": "` + canary + `"}]}`,
		`{"warnings": [{"type": "warning", "message": "` + canary + `"}]}`,
	} {
		f.Add(uint8(0), []byte(seed))
	}
	f.Add(uint8(3), []byte(`{"type": "error", "error": {"ref_id": "r", "message": "`+canary+`"}}`))
	f.Add(uint8(4), []byte(`{"detail": [{"msg": "bad"}]}`))

	// The connector is built once: building generates the tools' schemas,
	// which would otherwise dominate each run. Each run's response travels
	// in the request's context (a fuzz worker runs one input at a time).
	client := &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		fr := r.Context().Value(fuzzReplyKey{}).(fuzzReply)
		return &http.Response{
			StatusCode: fr.status, Header: http.Header{"Content-Type": {"application/json"}},
			Body: io.NopCloser(bytes.NewReader(fr.body)), Request: r,
		}, nil
	})}
	var logs logBuffer
	settings := `{"max_content_chars": {"search": 40, "extract": 40}, "extract_defaults": {"full_content": true}}`
	tools, err := parallel.Factory(
		parallel.WithHTTPClient(client),
		parallel.WithLookupEnv(lookup(map[string]string{"PARALLEL_API_KEY": canary})),
		parallel.WithClock(clocktest.NewFake(time.Time{})),
		parallel.WithLogger(slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))),
	).Build(f.Context(), json.RawMessage(settings))
	if err != nil {
		f.Fatal(err)
	}
	var tl tool.Tool
	for _, x := range tools {
		if x.Definition().Name == toolName {
			tl = x
		}
	}
	f.Fuzz(func(t *testing.T, statusIndex uint8, body []byte) {
		status := fuzzStatuses[int(statusIndex)%len(fuzzStatuses)]
		logs.reset()
		ctx := context.WithValue(t.Context(), fuzzReplyKey{}, fuzzReply{status, body})
		out, err := tl.Invoke(ctx, json.RawMessage(input))
		switch {
		case err != nil:
			if !errors.Is(err, tool.ErrToolFailed) && !errors.Is(err, tool.ErrInvalidInput) {
				t.Errorf("unclassified error: %v", err)
			}
			if strings.Contains(err.Error(), canary) {
				t.Errorf("the key leaked into the error: %v", err)
			}
		case status != 200:
			t.Errorf("HTTP %d gave output %s, want an error", status, out)
		default:
			checkOutput(t, toolName, out, 40)
		}
		if strings.Contains(logs.String(), canary) {
			t.Errorf("the key leaked into the log: %s", logs.String())
		}
	})
}

type (
	fuzzReplyKey struct{}
	fuzzReply    struct {
		status int
		body   []byte
	}
)

// cassetteBody returns the response body of a cassette.
func cassetteBody(f *testing.F, name string) []byte {
	raw, err := os.ReadFile(filepath.Join("testdata", "cassettes", name+".res.http"))
	if err != nil {
		f.Fatal(err)
	}
	_, body, ok := bytes.Cut(raw, []byte("\n\n"))
	if !ok {
		f.Fatalf("cassette %s has no body", name)
	}
	return body
}
