package agentapi_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/store/scoped"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

// FuzzPostRun sends arbitrary bodies to POST /v1/runs. Whatever the body, the
// server answers with one of the documented statuses and a well-formed body,
// never panics, and starts a run exactly when the body is a JSON object with
// only the known fields and a message: then the model is asked that message.
func FuzzPostRun(f *testing.F) {
	for _, seed := range []string{
		`{"message":"hi"}`,
		`{"message":"hi","conversation_id":"conv_1"}`,
		`{"message":""}`,
		`{"message":"hi","extra":1}`,
		`{"message":"hi"}{}`,
		`{"message":"\u0000\ud800 é"}`,
		`{"message":["hi"]}`,
		`{"conversation_id":null}`,
		`{"message":"hi","agent":"default"}`,
		`{"message":"hi","agent":"other"}`,
		`null`,
		`[]`,
		``,
		`{"message":"` + strings.Repeat("x", 300) + `"}`,
	} {
		f.Add([]byte(seed), true)
		f.Add([]byte(seed), false)
	}
	reg, err := tool.NewRegistry(nil)
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, body []byte, asJSON bool) {
		h, err := harness.New(echo(t), reg)
		if err != nil {
			t.Fatal(err)
		}
		srv, err := agentapi.New(h, agentapi.WithStore(scoped.Memory(nil)), agentapi.WithMaxBodyBytes(256), agentapi.WithKeepAlive(0))
		if err != nil {
			t.Fatal(err)
		}
		handler := srv.Handler()
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/runs", strings.NewReader(string(body)))
		if asJSON {
			req.Header.Set("Accept", "application/json")
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		var want agentapi.RunRequest
		dec := json.NewDecoder(strings.NewReader(string(body)))
		dec.DisallowUnknownFields()
		valid := len(body) <= 256 && dec.Decode(&want) == nil && strings.TrimSpace(want.Message) != "" &&
			(want.Agent == "" || want.Agent == agentapi.DefaultAgent) // another agent is 404
		if valid {
			_, err := dec.Token()
			valid = errors.Is(err, io.EOF) // nothing but space after the object
		}
		switch rec.Code {
		case http.StatusOK:
			if want.ConversationID != "" || !valid {
				t.Fatalf("started a run for %q", body)
			}
			if !asJSON {
				if !strings.Contains(rec.Body.String(), "event: run_done") {
					t.Fatalf("stream for %q: %s", body, rec.Body)
				}
				return
			}
			var run agentapi.Run
			if err := json.Unmarshal(rec.Body.Bytes(), &run); err != nil || run.Status != agentapi.StatusDone || run.Answer != "echo: "+want.Message {
				t.Fatalf("run for %q = %s (%v), want the answer to %q", body, rec.Body, err, want.Message)
			}
		case http.StatusBadRequest, http.StatusRequestEntityTooLarge, http.StatusNotFound:
			if valid && want.ConversationID == "" {
				t.Fatalf("%q refused with %d: %s", body, rec.Code, rec.Body)
			}
			var e agentapi.ErrorResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil || e.Error == nil || e.Error.Code == "" {
				t.Fatalf("error body for %q: %s", body, rec.Body)
			}
		default:
			t.Fatalf("%q: status %d: %s", body, rec.Code, rec.Body)
		}
	})
}

// FuzzConversationList sends arbitrary queries to GET /v1/conversations over
// n conversations. The answer is 200, or 400 for a bad limit or cursor, or
// 404 for an unknown agent; a 200 page is a contiguous window of the
// conversations newest first, as long as the limit allows, with a cursor
// exactly when more follow; and following the cursors from it sees every
// later conversation once.
func FuzzConversationList(f *testing.F) {
	for _, seed := range []string{
		"", "limit=1", "limit=3&cursor=", "limit=0", "limit=101", "limit=x", "limit=2&limit=0",
		"cursor=Y29udmVyc2F0aW9ucy1iZWZvcmU6Mw",  // conversations-before:3
		"cursor=Y29udmVyc2F0aW9ucy1iZWZvcmU6MDM", // conversations-before:03, not canonical
		"cursor=Y29udmVyc2F0aW9ucy1iZWZvcmU6OTk5", "cursor=bm9wZQ", "cursor=%00", "agent=default&limit=2",
		"agent=nobody", "agent=&limit=4",
	} {
		f.Add(seed, uint8(5))
		f.Add(seed, uint8(0))
	}
	reg, err := tool.NewRegistry(nil)
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, query string, n uint8) {
		n %= 12
		h, err := harness.New(echo(t), reg)
		if err != nil {
			t.Fatal(err)
		}
		srv, err := agentapi.New(h, agentapi.WithStore(scoped.Memory(nil)), agentapi.WithKeepAlive(0))
		if err != nil {
			t.Fatal(err)
		}
		handler := srv.Handler()
		var newest []string // newest first
		for i := range n {
			req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/runs", strings.NewReader(fmt.Sprintf(`{"message":"m%d"}`, i)))
			req.Header.Set("Accept", "application/json")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			var run agentapi.Run
			if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &run) != nil {
				t.Fatalf("run = %d %s", rec.Code, rec.Body)
			}
			newest = append([]string{run.ConversationID}, newest...)
		}
		list := func(rawQuery string) (int, wire.ConversationList) {
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/conversations", nil)
			req.URL.RawQuery = rawQuery
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			var page wire.ConversationList
			if rec.Code == http.StatusOK && json.Unmarshal(rec.Body.Bytes(), &page) != nil {
				t.Fatalf("?%s: 200 with %s", rawQuery, rec.Body)
			}
			return rec.Code, page
		}

		q, _ := url.ParseQuery(query)
		code, page := list(query)
		limit, err := strconv.Atoi(q.Get("limit"))
		if q.Get("limit") == "" {
			limit, err = agentapi.DefaultListLimit, nil
		}
		agentOK := q.Get("agent") == "" || q.Get("agent") == agentapi.DefaultAgent
		switch {
		case err != nil || limit < 1 || limit > agentapi.MaxListLimit:
			if code != http.StatusBadRequest {
				t.Fatalf("?%s (bad limit) = %d", query, code)
			}
			return
		case !agentOK:
			if code != http.StatusNotFound && (code != http.StatusBadRequest || q.Get("cursor") == "") {
				t.Fatalf("?%s (unknown agent) = %d", query, code)
			}
			return
		case code == http.StatusBadRequest && q.Get("cursor") != "":
			return // a cursor the server did not give
		case code != http.StatusOK:
			t.Fatalf("?%s = %d", query, code)
		}
		seen := ids(page.Conversations)
		start := len(newest)
		if len(seen) > 0 {
			start = slices.Index(newest, seen[0])
		} else if q.Get("cursor") == "" {
			start = 0
		}
		if start < 0 || len(seen) > limit || !slices.Equal(seen, newest[start:start+len(seen)]) ||
			(len(seen) < limit && start+len(seen) != len(newest)) || (page.NextCursor != "") != (start+len(seen) < len(newest)) {
			t.Fatalf("?%s over %v = %v, cursor %q", query, newest, seen, page.NextCursor)
		}
		for cursor := page.NextCursor; cursor != ""; {
			q.Set("cursor", cursor)
			code, next := list(q.Encode())
			if code != http.StatusOK || len(next.Conversations) > limit {
				t.Fatalf("following cursor %q (limit %d): %d, %d conversations", cursor, limit, code, len(next.Conversations))
			}
			seen = append(seen, ids(next.Conversations)...)
			cursor = next.NextCursor
		}
		if !slices.Equal(seen, newest[start:]) {
			t.Fatalf("?%s then its cursors saw %v, want %v", query, seen, newest[start:])
		}
	})
}
