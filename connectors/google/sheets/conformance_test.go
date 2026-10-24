package sheets_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/connectors/google/sheets"
	"github.com/bsamanvith/agentkit/pkg/toolset"
	"github.com/bsamanvith/agentkit/pkg/toolset/toolsettest"
)

const (
	budget = "1BxiMVs0XRA5nFMdKvBdBZjgmUUqptlbs74OgvE2upms"

	writeSettings = `{"read_only": false}`
)

// connector describes Sheets to the T049 test kit. The token is the "key":
// access_token_env names the variable. The cassettes are synthetic, written
// from the Sheets API v4 reference.
func connector() toolsettest.Connector {
	return toolsettest.Connector{
		New: func(e toolsettest.Env) toolset.Factory {
			return sheets.Factory(sheets.WithBaseURL(e.BaseURL), sheets.WithHTTPClient(e.HTTPClient),
				sheets.WithLookupEnv(e.LookupEnv), sheets.WithClock(e.Clock), sheets.WithLogger(e.Logger))
		},
		KeyEnv: "GOOGLE_ACCESS_TOKEN",
		Enable: []toolsettest.CheckID{"classified"},
		Skip: map[toolsettest.CheckID]string{
			"settings.honored":  "no setting changes a request: read_only chooses tools, max_cells cuts output",
			"cassettes.openapi": "Google publishes Sheets as a discovery document, not OpenAPI; the cassettes are written from the v4 reference",
		},
		Calls: []toolsettest.Call{
			{Name: "get_budget", Tool: "sheets_get_spreadsheet", Input: json.RawMessage(`{"spreadsheet_id": "` + budget + `"}`), Cassettes: []string{"get-budget"}},
			{
				Name: "read_summary", Tool: "sheets_read_range",
				Input:     json.RawMessage(`{"spreadsheet_id": "` + budget + `", "range": "Summary!A1:C4"}`),
				Cassettes: []string{"read-summary"},
			},
			{
				Name: "append_costs", Tool: "sheets_append_rows", Settings: json.RawMessage(writeSettings),
				Input:     json.RawMessage(`{"spreadsheet_id": "` + budget + `", "range": "'Q3 costs'", "rows": [["2026-10-20", "Hosting", "4200"]]}`),
				Cassettes: []string{"append-costs"},
			},
			{
				Name: "update_total", Tool: "sheets_update_range", Settings: json.RawMessage(writeSettings),
				Input:     json.RawMessage(`{"spreadsheet_id": "` + budget + `", "range": "Summary!B4:C4", "rows": [["=SUM(B2:B3)", "=SUM(C2:C3)"]]}`),
				Cassettes: []string{"update-total"},
			},
			{
				Name: "create_plan", Tool: "sheets_create_spreadsheet", Settings: json.RawMessage(writeSettings),
				Input:     json.RawMessage(`{"title": "Offsite plan", "tabs": ["Agenda", "Travel"]}`),
				Cassettes: []string{"create-plan"},
			},
		},
		Invalid: []toolsettest.Call{
			{Name: "id_with_a_slash", Tool: "sheets_get_spreadsheet", Input: json.RawMessage(`{"spreadsheet_id": "abc/../../files"}`)},
			{Name: "empty_range", Tool: "sheets_read_range", Input: json.RawMessage(`{"spreadsheet_id": "` + budget + `", "range": " "}`)},
			{Name: "unquoted_quote", Tool: "sheets_read_range", Input: json.RawMessage(`{"spreadsheet_id": "` + budget + `", "range": "Bob's tab!A1"}`)},
			{Name: "cells_not_a1", Tool: "sheets_read_range", Input: json.RawMessage(`{"spreadsheet_id": "` + budget + `", "range": "Summary!A1:B2:C3"}`)},
			{
				Name: "no_rows", Tool: "sheets_append_rows", Settings: json.RawMessage(writeSettings),
				Input: json.RawMessage(`{"spreadsheet_id": "` + budget + `", "range": "Summary", "rows": []}`),
			},
			{
				Name: "tab_named_twice", Tool: "sheets_create_spreadsheet", Settings: json.RawMessage(writeSettings),
				Input: json.RawMessage(`{"title": "x", "tabs": ["A", "A"]}`),
			},
		},
		ErrorBody: func(message string) []byte {
			b, _ := json.Marshal(map[string]any{"error": map[string]any{"code": 400, "message": message, "status": "INVALID_ARGUMENT"}})
			return b
		},
		Truncation: &toolsettest.Truncation{
			Call: "read_summary", Field: "/values/1/0", Settings: json.RawMessage(`{"max_cell_chars": 50}`), Limit: 50,
		},
		AttemptTimeout: &toolsettest.AttemptTimeout{Settings: json.RawMessage(`{"timeout": "300ms"}`), Timeout: 300 * time.Millisecond},
	}
}

func TestConformance(t *testing.T) {
	t.Parallel()
	toolsettest.RunConnector(t, connector())
}

func FuzzResponses(f *testing.F) { toolsettest.FuzzResponses(f, connector(), "sheets_read_range") }

func FuzzResponsesGet(f *testing.F) {
	toolsettest.FuzzResponses(f, connector(), "sheets_get_spreadsheet")
}
