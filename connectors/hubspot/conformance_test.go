package hubspot_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/connectors/hubspot"
	"github.com/bsamanvith/agentkit/pkg/toolset"
	"github.com/bsamanvith/agentkit/pkg/toolset/toolsettest"
)

const writeSettings = `{"read_only": false}`

// connector describes HubSpot to the T049 test kit. The token is the
// "key": access_token_env names the variable. The cassettes are synthetic,
// written from the CRM API v3 reference.
func connector() toolsettest.Connector {
	return toolsettest.Connector{
		New: func(e toolsettest.Env) toolset.Factory {
			return hubspot.Factory(hubspot.WithBaseURL(e.BaseURL), hubspot.WithHTTPClient(e.HTTPClient),
				hubspot.WithLookupEnv(e.LookupEnv), hubspot.WithClock(e.Clock), hubspot.WithLogger(e.Logger))
		},
		KeyEnv: "HUBSPOT_ACCESS_TOKEN",
		Enable: []toolsettest.CheckID{"classified"},
		Skip: map[toolsettest.CheckID]string{
			"settings.honored":  "no setting changes a request: read_only chooses tools, max_value_chars cuts output",
			"cassettes.openapi": "HubSpot's per-object OpenAPI documents are not at a stable public URL; the synthetic cassettes are written from the CRM v3 reference",
		},
		Calls: []toolsettest.Call{
			{
				Name: "search_contacts", Tool: "hubspot_search",
				Input: json.RawMessage(`{"object": "contacts", "query": "northwind", "limit": 2,
					"filters": [{"property": "lifecyclestage", "operator": "EQ", "value": "customer"}],
					"properties": ["email", "firstname", "lastname", "jobtitle"]}`),
				Cassettes: []string{"search-contacts"},
			},
			{
				Name: "get_company", Tool: "hubspot_get_record",
				Input:     json.RawMessage(`{"object": "companies", "id": "9001", "properties": ["name", "domain", "description"], "associations": ["contacts", "deals"]}`),
				Cassettes: []string{"get-company"},
			},
			{
				Name: "create_contact", Tool: "hubspot_create_record", Settings: json.RawMessage(writeSettings),
				Input:     json.RawMessage(`{"object": "contacts", "properties": {"email": "sam.okafor@contoso.example", "firstname": "Sam", "lastname": "Okafor"}}`),
				Cassettes: []string{"create-contact"},
			},
			{
				Name: "update_deal", Tool: "hubspot_update_record", Settings: json.RawMessage(writeSettings),
				Input:     json.RawMessage(`{"object": "deals", "id": "5501", "properties": {"dealstage": "closedwon", "amount": "48000"}}`),
				Cassettes: []string{"update-deal"},
			},
			{
				Name: "create_note", Tool: "hubspot_create_note", Settings: json.RawMessage(writeSettings),
				Input: json.RawMessage(`{"body": "Call with Ana: renewal approved, PO to follow next week.",
					"records": [{"object": "contacts", "id": "1051"}, {"object": "deals", "id": "5501"}]}`),
				Cassettes: []string{"create-note"},
			},
		},
		Invalid: []toolsettest.Call{
			{Name: "unknown_object", Tool: "hubspot_search", Input: json.RawMessage(`{"object": "leads", "query": "x"}`)},
			{Name: "unknown_operator", Tool: "hubspot_search", Input: json.RawMessage(`{"object": "deals", "filters": [{"property": "amount", "operator": "BETWEEN", "value": "1"}]}`)},
			{Name: "value_on_has_property", Tool: "hubspot_search", Input: json.RawMessage(`{"object": "deals", "filters": [{"property": "amount", "operator": "HAS_PROPERTY", "value": "1"}]}`)},
			{Name: "property_with_a_dot", Tool: "hubspot_search", Input: json.RawMessage(`{"object": "contacts", "properties": ["email", "a.b"]}`)},
			{Name: "id_not_digits", Tool: "hubspot_get_record", Input: json.RawMessage(`{"object": "contacts", "id": "1051/../../owners"}`)},
			{Name: "unknown_association", Tool: "hubspot_get_record", Input: json.RawMessage(`{"object": "contacts", "id": "1051", "associations": ["owners"]}`)},
			{
				Name: "no_properties", Tool: "hubspot_update_record", Settings: json.RawMessage(writeSettings),
				Input: json.RawMessage(`{"object": "deals", "id": "5501", "properties": {}}`),
			},
			{
				Name: "note_on_nothing", Tool: "hubspot_create_note", Settings: json.RawMessage(writeSettings),
				Input: json.RawMessage(`{"body": "x", "records": []}`),
			},
		},
		ErrorBody: func(message string) []byte {
			b, _ := json.Marshal(map[string]any{"status": "error", "message": message, "category": "VALIDATION_ERROR"})
			return b
		},
		Truncation: &toolsettest.Truncation{
			Call: "get_company", Field: "/properties/description", Settings: json.RawMessage(`{"max_value_chars": 50}`), Limit: 50,
		},
		AttemptTimeout: &toolsettest.AttemptTimeout{Settings: json.RawMessage(`{"timeout": "300ms"}`), Timeout: 300 * time.Millisecond},
	}
}

func TestConformance(t *testing.T) {
	t.Parallel()
	toolsettest.RunConnector(t, connector())
}

func FuzzResponses(f *testing.F) { toolsettest.FuzzResponses(f, connector(), "hubspot_search") }

func FuzzResponsesGet(f *testing.F) {
	toolsettest.FuzzResponses(f, connector(), "hubspot_get_record")
}
