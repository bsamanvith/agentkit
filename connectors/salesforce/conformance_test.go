package salesforce_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/connectors/salesforce"
	"github.com/bsamanvith/agentkit/pkg/toolset"
	"github.com/bsamanvith/agentkit/pkg/toolset/toolsettest"
)

const writeSettings = `{"read_only": false}`

// connector describes Salesforce to the T049 test kit. The token is the
// "key": access_token_env names the variable; base_url (from the kit)
// stands in for the org's instance_url. The cassettes are synthetic,
// written from the REST API reference.
func connector() toolsettest.Connector {
	return toolsettest.Connector{
		New: func(e toolsettest.Env) toolset.Factory {
			return salesforce.Factory(salesforce.WithBaseURL(e.BaseURL), salesforce.WithHTTPClient(e.HTTPClient),
				salesforce.WithLookupEnv(e.LookupEnv), salesforce.WithClock(e.Clock), salesforce.WithLogger(e.Logger))
		},
		KeyEnv: "SALESFORCE_ACCESS_TOKEN",
		Enable: []toolsettest.CheckID{"classified"},
		Skip: map[toolsettest.CheckID]string{
			"settings.honored":  "api_version changes the path, which the cassettes pin to v61.0; TestAPIVersionSetting covers it. read_only chooses tools, the limits cut output",
			"cassettes.openapi": "Salesforce publishes no OpenAPI document for the REST API's core resources; the synthetic cassettes are written from the REST API reference",
		},
		Calls: []toolsettest.Call{
			{
				Name: "query_contacts", Tool: "salesforce_query",
				Input:     json.RawMessage(`{"query": "SELECT Id, Name, Email, Account.Name, Description FROM Contact WHERE Account.Name = 'Northwind Traders'"}`),
				Cassettes: []string{"query-contacts"},
			},
			{
				Name: "search_northwind", Tool: "salesforce_search",
				Input:     json.RawMessage(`{"text": "Northwind", "objects": ["Account", "Contact"], "limit": 5}`),
				Cassettes: []string{"search-northwind"},
			},
			{
				Name: "get_account", Tool: "salesforce_get_record",
				Input:     json.RawMessage(`{"object": "Account", "id": "001D000000IqhSLIAZ", "fields": ["Name", "Industry", "AnnualRevenue", "IsDeleted"]}`),
				Cassettes: []string{"get-account"},
			},
			{
				Name: "describe_opportunity", Tool: "salesforce_describe_object",
				Input:     json.RawMessage(`{"object": "Opportunity"}`),
				Cassettes: []string{"describe-opportunity"},
			},
			{
				Name: "create_contact", Tool: "salesforce_create_record", Settings: json.RawMessage(writeSettings),
				Input:     json.RawMessage(`{"object": "Contact", "fields": {"LastName": "Okafor", "FirstName": "Sam", "Email": "sam.okafor@contoso.example"}}`),
				Cassettes: []string{"create-contact"},
			},
			{
				Name: "update_opportunity", Tool: "salesforce_update_record", Settings: json.RawMessage(writeSettings),
				Input:     json.RawMessage(`{"object": "Opportunity", "id": "006D000000CbP8yIAF", "fields": {"StageName": "Closed Won", "Amount": 48000}}`),
				Cassettes: []string{"update-opportunity"},
			},
		},
		Invalid: []toolsettest.Call{
			{Name: "not_a_select", Tool: "salesforce_query", Input: json.RawMessage(`{"query": "DELETE FROM Contact"}`)},
			{Name: "two_statements", Tool: "salesforce_query", Input: json.RawMessage(`{"query": "SELECT Id FROM Contact; SELECT Id FROM Lead"}`)},
			{Name: "next_elsewhere", Tool: "salesforce_query", Input: json.RawMessage(`{"next": "/services/data/v61.0/sobjects/User"}`)},
			{Name: "object_with_a_slash", Tool: "salesforce_get_record", Input: json.RawMessage(`{"object": "Account/../User", "id": "001D000000IqhSLIAZ"}`)},
			{Name: "short_id", Tool: "salesforce_get_record", Input: json.RawMessage(`{"object": "Account", "id": "001"}`)},
			{Name: "one_letter_search", Tool: "salesforce_search", Input: json.RawMessage(`{"text": "a"}`)},
			{
				Name: "nested_value", Tool: "salesforce_create_record", Settings: json.RawMessage(writeSettings),
				Input: json.RawMessage(`{"object": "Contact", "fields": {"LastName": {"x": 1}}}`),
			},
		},
		ErrorBody: func(message string) []byte {
			b, _ := json.Marshal([]map[string]any{{"message": message, "errorCode": "MALFORMED_QUERY"}})
			return b
		},
		Truncation: &toolsettest.Truncation{
			Call: "query_contacts", Field: "/records/0/Description", Settings: json.RawMessage(`{"max_value_chars": 50}`), Limit: 50,
		},
		AttemptTimeout: &toolsettest.AttemptTimeout{Settings: json.RawMessage(`{"timeout": "300ms"}`), Timeout: 300 * time.Millisecond},
	}
}

func TestConformance(t *testing.T) {
	t.Parallel()
	toolsettest.RunConnector(t, connector())
}

func FuzzResponses(f *testing.F) { toolsettest.FuzzResponses(f, connector(), "salesforce_query") }

func FuzzResponsesSearch(f *testing.F) {
	toolsettest.FuzzResponses(f, connector(), "salesforce_search")
}

func FuzzResponsesGet(f *testing.F) {
	toolsettest.FuzzResponses(f, connector(), "salesforce_get_record")
}

func FuzzResponsesDescribe(f *testing.F) {
	toolsettest.FuzzResponses(f, connector(), "salesforce_describe_object")
}
