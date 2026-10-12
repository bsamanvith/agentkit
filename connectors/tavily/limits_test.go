package tavily_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"
)

// hostileSearch is a search body with n results whose every field is size
// runes of s. It also echoes the key everywhere, and an answer.
func hostileSearch(n, size int, s string) []byte {
	field := strings.Repeat(s, size)
	type res struct {
		Title, URL, Content string
		PublishedDate       string `json:"published_date"`
		Score               float64
	}
	body := struct {
		Answer  string `json:"answer"`
		Results []res  `json:"results"`
	}{Answer: canary + field}
	for range n {
		body.Results = append(body.Results, res{field + canary, canary + field, field + canary + field, field, 0.5})
	}
	b, _ := json.Marshal(body)
	return b
}

func hostileExtract(n, size int, s string) []byte {
	field := strings.Repeat(s, size)
	type res struct {
		URL, Title string
		RawContent string `json:"raw_content"`
	}
	type fail struct{ URL, Error string }
	var body struct {
		Results []res  `json:"results"`
		Failed  []fail `json:"failed_results"`
	}
	for range n {
		body.Results = append(body.Results, res{field + canary, field, field + canary + field})
		body.Failed = append(body.Failed, fail{field, canary + field})
	}
	b, _ := json.Marshal(body)
	return b
}

// TestContentTruncated: content over max_content_chars is cut at a rune
// boundary, after exactly that many characters, with a marker saying how many
// were cut and how to get more; content at the limit is kept whole. Other
// fields (here a title over 300) get the short marker.
func TestContentTruncated(t *testing.T) {
	t.Parallel()
	const (
		moreSearch  = "; call tavily_extract on this URL for the full page]"
		moreExtract = "; call tavily_extract again with a query to get the parts most relevant to it]"
		moreAnswer  = "; search again with a narrower query]"
	)
	for _, c := range []struct {
		name   string
		rune   string
		size   int
		limit  int
		marker string // after the kept text
	}{
		{"ascii over", "a", 4001, 4000, "…[truncated 1 character" + moreSearch},
		{"two-byte runes over", "é", 4500, 4000, "…[truncated 500 characters" + moreSearch},
		{"four-byte runes over", "🦫", 16400, 4000, "…[truncated 12,400 characters" + moreSearch},
		{"at the limit", "é", 4000, 4000, ""},
		{"custom limit", "日", 1234567, 100, "…[truncated 1,234,467 characters" + moreSearch},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			settings := fmt.Sprintf(`{"max_content_chars": {"search": %d}}`, c.limit)
			r := newRig(t, rigOpts{settings: settings})
			content := strings.Repeat(c.rune, c.size)
			body, _ := json.Marshal(map[string]any{"results": []map[string]any{{"title": "t", "url": "https://x.test", "content": content}}})
			r.up.queue(reply{status: http.StatusOK, body: body})
			out, err := r.call("tavily_search", `{"query": "go release"}`)
			if err != nil {
				t.Fatal(err)
			}
			var got struct{ Results []struct{ Content string } }
			if err := json.Unmarshal(out, &got); err != nil || len(got.Results) != 1 {
				t.Fatalf("output %.200s: %v, want one result", out, err)
			}
			want := content
			if c.marker != "" {
				want = strings.Repeat(c.rune, c.limit) + c.marker
			}
			if g := got.Results[0].Content; g != want || !utf8.ValidString(g) {
				t.Errorf("content has %d runes ending %q (valid UTF-8 %v), want %d ending %q",
					utf8.RuneCountInString(g), tail(g), utf8.ValidString(g), utf8.RuneCountInString(want), tail(want))
			}
		})
	}
	t.Run("answer, extract and title", func(t *testing.T) {
		t.Parallel()
		r := newRig(t, rigOpts{settings: `{"max_content_chars": {"search": 100, "extract": 200},
			"search_defaults": {"include_answer": true}}`})
		long := strings.Repeat("x", 1500)
		search, _ := json.Marshal(map[string]any{"answer": long, "results": []map[string]any{{"title": long, "url": "https://x.test", "content": "c"}}})
		extract, _ := json.Marshal(map[string]any{"results": []map[string]any{{"url": "https://x.test", "raw_content": long}}})
		r.up.queue(reply{status: http.StatusOK, body: search}, reply{status: http.StatusOK, body: extract})
		out, err := r.call("tavily_search", `{"query": "go release"}`)
		if err != nil {
			t.Fatal(err)
		}
		var s struct {
			Answer  string
			Results []struct{ Title string }
		}
		if err := json.Unmarshal(out, &s); err != nil || len(s.Results) != 1 {
			t.Fatalf("output %.200s: %v, want one result", out, err)
		}
		if want := long[:100] + "…[truncated 1,400 characters" + moreAnswer; s.Answer != want {
			t.Errorf("answer ends %q, want %q", tail(s.Answer), tail(want))
		}
		if want := long[:300] + truncated; s.Results[0].Title != want {
			t.Errorf("title ends %q, want %q", tail(s.Results[0].Title), tail(want))
		}
		out, err = r.call("tavily_extract", `{"urls": ["https://x.test"]}`)
		if err != nil {
			t.Fatal(err)
		}
		var e struct{ Results []struct{ Content string } }
		if err := json.Unmarshal(out, &e); err != nil || len(e.Results) != 1 {
			t.Fatalf("output %.200s: %v, want one result", out, err)
		}
		if want := long[:200] + "…[truncated 1,300 characters" + moreExtract; e.Results[0].Content != want {
			t.Errorf("extract content ends %q, want %q", tail(e.Results[0].Content), tail(want))
		}
	})
}

func tail(s string) string {
	if r := []rune(s); len(r) > 90 {
		return string(r[len(r)-90:])
	}
	return s
}

// TestBodyLimit: the edges of the 8 MB limit the suite does not try
// (TestConformance/errors/oversized_body is a 200 well over it): an error
// status over the limit, and a body of exactly the limit.
func TestBodyLimit(t *testing.T) {
	t.Parallel()
	t.Run("400 over the limit", func(t *testing.T) {
		t.Parallel()
		r := newRig(t, rigOpts{})
		body := []byte(`{"detail": {"error": "` + strings.Repeat("x", 8<<20) + `"}}`)
		r.up.queue(reply{status: http.StatusBadRequest, body: body})
		if _, err := r.call("tavily_search", `{"query": "go release"}`); !isToolFailed(err) {
			t.Errorf("err = %v, want ErrToolFailed", err)
		}
	})
	t.Run("just under the limit", func(t *testing.T) {
		t.Parallel()
		r := newRig(t, rigOpts{})
		pad := 8<<20 - len(`{"results": [], "pad": ""}`)
		r.up.queue(reply{status: http.StatusOK, body: []byte(`{"results": [], "pad": "` + strings.Repeat("x", pad) + `"}`)})
		if _, err := r.call("tavily_search", `{"query": "go release"}`); err != nil {
			t.Errorf("err = %v for a body of exactly 8 MB", err)
		}
	})
}

// maxOutputBytes is a bound on a tool's output whatever Tavily sends:
// every capped field at its cap, each rune escaped to at most 6 bytes, plus
// the JSON around it.
func maxOutputBytes(tool string) int {
	const perRune, perItem, marker = 6, 200, 160 // the longest marker, escaped
	field := func(runes int) int { return perRune*runes + marker }
	if tool == "tavily_search" {
		item := field(300) + field(2048) + field(64) + field(4000) + perItem
		return 20*item + field(4000) + perItem
	}
	item := field(2048) + field(300) + field(20000) + perItem
	failed := field(2048) + field(300) + perItem
	return 10*item + 10*failed + perItem
}

// TestOutputBounded: a body under the limit but full of huge, hostile items
// still gives an output within maxOutputBytes, with no more items than were
// asked for, and the key redacted (the rig checks the key).
func TestOutputBounded(t *testing.T) {
	t.Parallel()
	t.Run("search", func(t *testing.T) {
		t.Parallel()
		r := newRig(t, rigOpts{settings: `{"search_defaults": {"include_answer": true}}`})
		r.up.queue(reply{status: http.StatusOK, body: hostileSearch(60, 2500, "<")})
		out, err := r.call("tavily_search", `{"query": "go release", "max_results": 20}`)
		if err != nil {
			t.Fatal(err)
		}
		var got struct{ Results []json.RawMessage }
		_ = json.Unmarshal(out, &got)
		if len(got.Results) != 20 || len(out) > maxOutputBytes("tavily_search") {
			t.Errorf("%d results, %d bytes; want 20 results within %d bytes", len(got.Results), len(out), maxOutputBytes("tavily_search"))
		}
	})
	t.Run("extract", func(t *testing.T) {
		t.Parallel()
		r := newRig(t, rigOpts{})
		r.up.queue(reply{status: http.StatusOK, body: hostileExtract(12, 25000, "é")})
		out, err := r.call("tavily_extract", `{"urls": ["https://a.test", "https://b.test", "https://c.test"]}`)
		if err != nil {
			t.Fatal(err)
		}
		var got struct{ Results, Failed []json.RawMessage }
		_ = json.Unmarshal(out, &got)
		if len(got.Results) != 3 || len(got.Failed) != 3 || len(out) > maxOutputBytes("tavily_extract") {
			t.Errorf("%d results, %d failed, %d bytes; want 3, 3, within %d bytes", len(got.Results), len(got.Failed), len(out), maxOutputBytes("tavily_extract"))
		}
	})
}

// TestKeyRedacted: Tavily echoing the key in any field of a result, or in
// the error shapes the suite's canary check does not send (422, extract
// 400), never gets it to the model. The rig's check after the
// test is what fails on a leak; this test makes the upstream try.
func TestKeyRedacted(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name  string
		tool  string
		input string
		reply reply
	}{
		{"422 echoes the key", "tavily_search", `{"query": "go release"}`, reply{status: 422, body: []byte(`{"detail": [{"loc": ["body", "` + canary + `"], "msg": "` + canary + `"}]}`)}},
		{"extract 400 echoes the key", "tavily_extract", `{"urls": ["https://a.test"]}`, reply{status: 400, body: []byte(`{"detail": {"error": "x", "failed_results": [{"url": "` + canary + `", "error": "` + canary + `"}]}}`)}},
		{"search results echo the key", "tavily_search", `{"query": "go release"}`, reply{status: 200, body: hostileSearch(2, 10, "a")}},
		{"extract results echo the key", "tavily_extract", `{"urls": ["https://a.test"]}`, reply{status: 200, body: hostileExtract(1, 10, "a")}},
		{"key split by escapes", "tavily_search", `{"query": "go release"}`, reply{status: 400, body: []byte(`{"detail": {"error": "` + strings.ReplaceAll(canary, "-", `-`) + `"}}`)}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t, rigOpts{settings: `{"search_defaults": {"include_answer": true}}`})
			r.up.queue(c.reply)
			out, err := r.call(c.tool, c.input)
			text := string(out)
			if err != nil {
				text = err.Error()
			}
			if !strings.Contains(text, "[redacted]") {
				t.Errorf("output %q: want the key replaced with [redacted]", text)
			}
		})
	}
}
