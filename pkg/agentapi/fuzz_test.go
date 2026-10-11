package agentapi_test

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/harness"
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
		srv, err := agentapi.New(h, agentapi.WithMaxBodyBytes(256), agentapi.WithKeepAlive(0))
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
		valid := len(body) <= 256 && dec.Decode(&want) == nil && strings.TrimSpace(want.Message) != ""
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
