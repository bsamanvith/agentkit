package exa_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"testing"

	"github.com/bsamanvith/agentkit/connectors/exa"
	"github.com/bsamanvith/agentkit/pkg/tool"
	"github.com/bsamanvith/agentkit/pkg/toolset"
)

// FuzzDecodeSearch feeds hostile 200 bodies to exa_search, in both content
// modes: it must never panic, and must return the E10 shape (within the
// caps, without the key) or a tool failure.
func FuzzDecodeSearch(f *testing.F) {
	for _, name := range []string{"search-highlights", "search-text", "search-deep"} {
		f.Add(cassetteBody(f, name), false)
	}
	f.Add([]byte(`{"results": [{"title": null, "author": 7}], "output": {"content": {"a": 1}}}`), true)
	f.Add([]byte(`{"results": [{"text": "\ud800 broken", "highlights": ["`+canaryKey+`"]}]}`), true)
	f.Add([]byte(`null`), false)
	tools := fuzzTools(f)
	f.Fuzz(func(t *testing.T, body []byte, text bool) {
		tl := tools.search
		if text {
			tl = tools.searchText
		}
		checkFuzzCall(t, tl, body, `{"query": "q"}`)
	})
}

// FuzzDecodeContents does the same for exa_extract's /contents decoding.
func FuzzDecodeContents(f *testing.F) {
	for _, name := range []string{"contents-partial", "contents-query"} {
		f.Add(cassetteBody(f, name), false)
	}
	f.Add([]byte(`{"results": [{"id": "https://a.example/"}], "statuses": [{"id": "https://a.example/", "status": "error", "error": null}]}`), true)
	f.Add([]byte(`{"statuses": [{"id": "", "status": "error", "error": {"tag": "", "httpStatusCode": 99999}}]}`), false)
	tools := fuzzTools(f)
	f.Fuzz(func(t *testing.T, body []byte, focused bool) {
		input := `{"urls": ["https://a.example/", "https://b.example/x"]}`
		if focused {
			input = `{"urls": ["https://a.example/"], "query": "q"}`
		}
		checkFuzzCall(t, tools.extract, body, input)
	})
}

type fuzzToolset struct{ search, searchText, extract tool.Tool }

// fuzzTools builds the tools once over a transport that answers every
// request with the body in its context, so iterations share no state and
// make no network calls.
func fuzzTools(f *testing.F) fuzzToolset {
	build := func(settings string) map[string]tool.Tool {
		fac := exa.Factory(
			exa.WithHTTPClient(&http.Client{Transport: bodyTransport{}}),
			exa.WithLookupEnv(func(string) (string, bool) { return canaryKey, true }),
		)
		tools, err := fac.Build(f.Context(), toolset.BuildInput{Settings: json.RawMessage(settings)})
		if err != nil {
			f.Fatal(err)
		}
		m := map[string]tool.Tool{}
		for _, tl := range tools {
			m[tl.Definition().Name] = tl
		}
		return m
	}
	hl, tx := build(`{"search_defaults": {"model_chooses_type": true}}`), build(`{"search_defaults": {"contents": "text"}}`)
	return fuzzToolset{search: hl["exa_search"], searchText: tx["exa_search"], extract: hl["exa_extract"]}
}

type bodyKey struct{}

// bodyTransport answers 200 with the body stored in the request's context.
type bodyTransport struct{}

func (bodyTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	body, _ := r.Context().Value(bodyKey{}).([]byte)
	if r.Body != nil {
		_ = r.Body.Close()
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": {"application/json"}},
		Body:       io.NopCloser(bytes.NewReader(body)),
		Request:    r,
	}, nil
}

func checkFuzzCall(t *testing.T, tl tool.Tool, body []byte, input string) {
	ctx := contextWithBody(t, body)
	out, err := tl.Invoke(ctx, json.RawMessage(input))
	checkNoKey(t, "output", string(out))
	if err != nil {
		checkNoKey(t, "error", err.Error())
		if !errors.Is(err, tool.ErrToolFailed) || !errors.Is(err, exa.ErrUpstream) {
			t.Fatalf("err = %v, want ErrToolFailed and ErrUpstream", err)
		}
		return
	}
	checkShape(t, tl.Definition().Name, out, 0)
}

func contextWithBody(t *testing.T, body []byte) context.Context {
	return context.WithValue(t.Context(), bodyKey{}, body)
}
