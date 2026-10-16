package gmail_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/connectors/google/gmail"
	"github.com/bsamanvith/agentkit/pkg/toolset"
	"github.com/bsamanvith/agentkit/pkg/toolset/toolsettest"
)

// connector describes Gmail to the connector test kit. Google's access token
// stands in for the API key: it is sent as a bearer token, in one header, and
// must never reach a URL, a body, an output, an error or a log line.
//
// The cassettes are synthetic, written from the Gmail API reference, until
// they are recorded with a test account (T109 Notes).
func connector() toolsettest.Connector {
	send := json.RawMessage(`{"allow_send": true}`)
	return toolsettest.Connector{
		New: func(e toolsettest.Env) toolset.Factory {
			return gmail.Factory(gmail.WithBaseURL(e.BaseURL), gmail.WithHTTPClient(e.HTTPClient),
				gmail.WithLookupEnv(e.LookupEnv), gmail.WithClock(e.Clock), gmail.WithLogger(e.Logger))
		},
		KeyEnv: "GOOGLE_ACCESS_TOKEN",
		Enable: []toolsettest.CheckID{"classified"},
		Skip: map[toolsettest.CheckID]string{
			"settings.honored":     "the only setting that changes behaviour, max_body_chars, changes the output and not the request; TestBodyLimitSetting covers it",
			"limits.rune_boundary": "bodies are base64url in Gmail's responses, which the kit cannot fill with its multi-byte text; TestBodyIsCutAtARuneBoundary covers it",
			"cassettes.openapi":    "Google publishes Discovery documents for Gmail, not OpenAPI; the cassettes are checked against the API reference by hand until they are recorded",
		},
		Calls: []toolsettest.Call{
			{
				Name: "search_unread_from_ada", Tool: "gmail_search_messages",
				Input:     json.RawMessage(`{"query": "from:ada is:unread", "max_results": 2}`),
				Cassettes: []string{"search-list", "search-get-m1", "search-get-m2"},
			},
			{
				Name: "read_message", Tool: "gmail_read_message", Input: json.RawMessage(`{"id": "m1"}`),
				Cassettes: []string{"read-m1"},
			},
			{
				Name: "read_thread", Tool: "gmail_read_thread", Input: json.RawMessage(`{"id": "t1"}`),
				Cassettes: []string{"read-thread-t1"},
			},
			{Name: "list_labels", Tool: "gmail_list_labels", Input: json.RawMessage(`{}`), Cassettes: []string{"list-labels"}},
			{
				Name: "mark_read_archive_star", Tool: "gmail_modify_labels",
				Input:     json.RawMessage(`{"ids": ["m1", "m2"], "add_label_ids": ["STARRED"], "remove_label_ids": ["UNREAD", "INBOX"]}`),
				Cassettes: []string{"modify-labels"},
			},
			{
				Name: "draft_new", Tool: "gmail_create_draft",
				Input:     json.RawMessage(`{"to": ["ada@example.com"], "subject": "Lunch on Friday?", "body": "Hi Ada,\n\nAre you free for lunch on Friday?\n\nBest,\nGrace"}`),
				Cassettes: []string{"draft-create"},
			},
			{
				Name: "draft_reply", Tool: "gmail_create_draft",
				Input:     json.RawMessage(`{"reply_to_message_id": "m1", "body": "Friday works for me. Café at noon?"}`),
				Cassettes: []string{"reply-original", "draft-reply"},
			},
			{
				Name: "send_message", Tool: "gmail_send_message", Settings: send,
				Input:     json.RawMessage(`{"to": ["ada@example.com"], "subject": "Lunch on Friday?", "body": "Hi Ada,\n\nAre you free for lunch on Friday?\n\nBest,\nGrace"}`),
				Cassettes: []string{"send-message"},
			},
			{
				Name: "send_draft", Tool: "gmail_send_draft", Settings: send,
				Input:     json.RawMessage(`{"draft_id": "r-draft-1"}`),
				Cassettes: []string{"send-draft"},
			},
		},
		Invalid: []toolsettest.Call{
			{Name: "max_results_too_big", Tool: "gmail_search_messages", Input: json.RawMessage(`{"query": "x", "max_results": 51}`)},
			{Name: "id_with_a_slash", Tool: "gmail_read_message", Input: json.RawMessage(`{"id": "../drafts"}`)},
			{Name: "thread_id_with_a_query", Tool: "gmail_read_thread", Input: json.RawMessage(`{"id": "t1?format=raw"}`)},
			{Name: "trash_label", Tool: "gmail_modify_labels", Input: json.RawMessage(`{"ids": ["m1"], "add_label_ids": ["TRASH"]}`)},
			{Name: "no_change", Tool: "gmail_modify_labels", Input: json.RawMessage(`{"ids": ["m1"]}`)},
			{Name: "no_recipient", Tool: "gmail_create_draft", Input: json.RawMessage(`{"subject": "Hi", "body": "Hello"}`)},
			{Name: "bad_address", Tool: "gmail_create_draft", Input: json.RawMessage(`{"to": ["not an address"], "body": "Hello"}`)},
			{Name: "header_injection_in_subject", Tool: "gmail_create_draft", Input: json.RawMessage(`{"to": ["a@example.com"], "subject": "Hi\r\nBcc: evil@example.com", "body": "Hello"}`)},
			{Name: "header_injection_in_address", Tool: "gmail_create_draft", Input: json.RawMessage(`{"to": ["a@example.com\nBcc: evil@example.com"], "body": "Hello"}`)},
			{Name: "empty_body", Tool: "gmail_create_draft", Input: json.RawMessage(`{"to": ["a@example.com"], "body": "  "}`)},
			{Name: "send_header_injection", Tool: "gmail_send_message", Settings: send, Input: json.RawMessage(`{"to": ["a@example.com"], "subject": "x\ny", "body": "Hello"}`)},
			{Name: "send_draft_id_with_a_slash", Tool: "gmail_send_draft", Settings: send, Input: json.RawMessage(`{"draft_id": "a/send"}`)},
		},
		ErrorBody: func(message string) []byte {
			b, _ := json.Marshal(map[string]any{"error": map[string]any{
				"code": 400, "message": message, "status": "INVALID_ARGUMENT",
				"errors": []map[string]string{{"message": message, "reason": "invalid"}},
			}})
			return b
		},
		AttemptTimeout: &toolsettest.AttemptTimeout{Settings: json.RawMessage(`{"timeout": "300ms"}`), Timeout: 300 * time.Millisecond},
	}
}

func TestConformance(t *testing.T) {
	t.Parallel()
	toolsettest.RunConnector(t, connector())
}

func FuzzSearchResponses(f *testing.F) {
	toolsettest.FuzzResponses(f, connector(), "gmail_search_messages")
}

func FuzzReadMessageResponses(f *testing.F) {
	toolsettest.FuzzResponses(f, connector(), "gmail_read_message")
}

func FuzzReadThreadResponses(f *testing.F) {
	toolsettest.FuzzResponses(f, connector(), "gmail_read_thread")
}
