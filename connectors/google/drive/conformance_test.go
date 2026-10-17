package drive_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/connectors/google/drive"
	"github.com/bsamanvith/agentkit/pkg/toolset"
	"github.com/bsamanvith/agentkit/pkg/toolset/toolsettest"
)

const (
	q3 = "1AbCdEfGhIjKlMnOpQrStUvWxYz012345"

	writeSettings = `{"read_only": false}`
	shareSettings = `{"read_only": false, "allow_share": true}`
)

// connector describes Drive to the T049 test kit. The token is the "key":
// access_token_env names the variable, and the kit checks it travels in one
// header and nowhere else. The cassettes are synthetic until T111 is recorded
// with a test account.
func connector() toolsettest.Connector {
	return toolsettest.Connector{
		New: func(e toolsettest.Env) toolset.Factory {
			return drive.Factory(drive.WithBaseURL(e.BaseURL), drive.WithHTTPClient(e.HTTPClient),
				drive.WithLookupEnv(e.LookupEnv), drive.WithClock(e.Clock), drive.WithLogger(e.Logger))
		},
		KeyEnv: "GOOGLE_ACCESS_TOKEN",
		Enable: []toolsettest.CheckID{"classified"},
		Skip: map[toolsettest.CheckID]string{
			"settings.honored":     "no setting changes a request: read_only, allow_share and narrow_scopes choose tools and scopes, the limits cut output",
			"cassettes.openapi":    "Google publishes Drive as a discovery document, not OpenAPI; the cassettes are written from the v3 reference",
			"limits.rune_boundary": "drive_read_file's text is a raw response body, not a JSON field; TestReadCutsAtARuneBoundary covers it",
		},
		Calls: []toolsettest.Call{
			{
				Name: "search_quarterly_report", Tool: "drive_search_files",
				Input:     json.RawMessage(`{"query": "quarterly report", "max_results": 2}`),
				Cassettes: []string{"search-quarterly-report"},
			},
			{Name: "list_root", Tool: "drive_list_folder", Input: json.RawMessage(`{}`), Cassettes: []string{"list-root"}},
			{Name: "get_q3_report", Tool: "drive_get_file", Input: json.RawMessage(`{"file_id": "` + q3 + `"}`), Cassettes: []string{"get-q3-report"}},
			{
				Name: "read_q3_report", Tool: "drive_read_file", Input: json.RawMessage(`{"file_id": "` + q3 + `"}`),
				Cassettes: []string{"get-q3-report", "export-q3-report"},
			},
			{
				Name: "create_blank_doc", Tool: "drive_create_file", Settings: json.RawMessage(writeSettings),
				Input:     json.RawMessage(`{"name": "Meeting notes", "mime_type": "google_doc", "folder_id": "0BfolderReports"}`),
				Cassettes: []string{"create-blank-doc"},
			},
			{
				Name: "rename_q3_report", Tool: "drive_update_file", Settings: json.RawMessage(writeSettings),
				Input:     json.RawMessage(`{"file_id": "` + q3 + `", "name": "Q3 report (final)"}`),
				Cassettes: []string{"rename-q3-report"},
			},
			{
				Name: "trash_q3_report", Tool: "drive_trash_file", Settings: json.RawMessage(writeSettings),
				Input: json.RawMessage(`{"file_id": "` + q3 + `"}`), Cassettes: []string{"trash-q3-report"},
			},
			{
				Name: "share_q3_report", Tool: "drive_share_file", Settings: json.RawMessage(shareSettings),
				Input:     json.RawMessage(`{"file_id": "` + q3 + `", "email": "grace@example.com", "role": "reader"}`),
				Cassettes: []string{"share-q3-report"},
			},
		},
		Invalid: []toolsettest.Call{
			{Name: "too_many_results", Tool: "drive_search_files", Input: json.RawMessage(`{"max_results": 101}`)},
			{Name: "drive_query_with_query", Tool: "drive_search_files", Input: json.RawMessage(`{"query": "a", "drive_query": "starred"}`)},
			{Name: "folder_id_is_not_an_id", Tool: "drive_list_folder", Input: json.RawMessage(`{"folder_id": "x' or 'a'='a"}`)},
			{Name: "file_id_with_a_slash", Tool: "drive_get_file", Input: json.RawMessage(`{"file_id": "abc/../permissions"}`)},
			{
				Name: "empty_update", Tool: "drive_update_file", Settings: json.RawMessage(writeSettings),
				Input: json.RawMessage(`{"file_id": "` + q3 + `"}`),
			},
			{
				Name: "share_with_anyone", Tool: "drive_share_file", Settings: json.RawMessage(shareSettings),
				Input: json.RawMessage(`{"file_id": "` + q3 + `", "email": "anyone", "role": "reader"}`),
			},
		},
		ErrorBody: func(message string) []byte {
			b, _ := json.Marshal(map[string]any{"error": map[string]any{"code": 400, "message": message, "status": "INVALID_ARGUMENT"}})
			return b
		},
		AttemptTimeout: &toolsettest.AttemptTimeout{Settings: json.RawMessage(`{"timeout": "300ms"}`), Timeout: 300 * time.Millisecond},
	}
}

func TestConformance(t *testing.T) {
	t.Parallel()
	toolsettest.RunConnector(t, connector())
}

func FuzzResponses(f *testing.F) { toolsettest.FuzzResponses(f, connector(), "drive_search_files") }

func FuzzResponsesGet(f *testing.F) { toolsettest.FuzzResponses(f, connector(), "drive_get_file") }

func FuzzResponsesRead(f *testing.F) { toolsettest.FuzzResponses(f, connector(), "drive_read_file") }
