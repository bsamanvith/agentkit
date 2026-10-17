package calendar_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/connectors/google/calendar"
	"github.com/bsamanvith/agentkit/pkg/toolset"
	"github.com/bsamanvith/agentkit/pkg/toolset/toolsettest"
)

const window = `"time_min": "2026-10-19T00:00:00+05:30", "time_max": "2026-10-26T00:00:00+05:30"`

// connector describes the Calendar connector to the T049 test kit. The suite
// builds every tool, so Settings turns the write tools on. Google's APIs
// cannot be recorded without an account, so the cassettes are synthetic,
// written from the API reference and checked against its discovery document
// (TestCassettesFitTheDiscoveryDocument).
func connector() toolsettest.Connector {
	return toolsettest.Connector{
		New: func(e toolsettest.Env) toolset.Factory {
			return calendar.Factory(calendar.WithBaseURL(e.BaseURL), calendar.WithHTTPClient(e.HTTPClient),
				calendar.WithLookupEnv(e.LookupEnv), calendar.WithClock(e.Clock), calendar.WithLogger(e.Logger))
		},
		Settings: json.RawMessage(`{"read_only": false}`),
		KeyEnv:   "GOOGLE_ACCESS_TOKEN",
		Skip: map[toolsettest.CheckID]string{
			"cassettes.openapi": "Google publishes a discovery document, not OpenAPI; TestCassettesFitTheDiscoveryDocument checks the cassettes against it",
		},
		Enable: []toolsettest.CheckID{"classified", "schema_honesty.dead_fields"},
		Calls: []toolsettest.Call{
			{Name: "list_calendars", Tool: "calendar_list_calendars", Input: json.RawMessage(`{}`), Cassettes: []string{"list-calendars"}},
			{Name: "list_events", Tool: "calendar_list_events", Input: json.RawMessage(`{` + window + `}`), Cassettes: []string{"list-events"}},
			{Name: "get_event", Tool: "calendar_get_event", Input: json.RawMessage(`{"event_id": "7h2k9q0s1d"}`), Cassettes: []string{"get-event"}},
			{Name: "get_event_with_many_guests", Tool: "calendar_get_event", Input: json.RawMessage(`{"event_id": "allhands01"}`), Cassettes: []string{"get-event-all-hands"}},
			{
				Name: "free_busy", Tool: "calendar_free_busy", Cassettes: []string{"free-busy"},
				Input: json.RawMessage(`{` + window + `, "calendar_ids": ["asha@example.com", "nobody@example.invalid"]}`),
			},
			{
				Name: "create_event", Tool: "calendar_create_event", Cassettes: []string{"create-event"},
				Input: json.RawMessage(`{"summary": "Lunch with Ravi", "start": "2026-10-23T13:00:00+05:30", "end": "2026-10-23T14:00:00+05:30",
					"location": "Cafe", "attendees": ["ravi@example.com"], "description": "Catch up on the roadmap.", "add_meet_link": true}`),
			},
			{
				Name: "update_event", Tool: "calendar_update_event", Cassettes: []string{"update-event"},
				Input: json.RawMessage(`{"event_id": "n3wev3nt01", "summary": "Late lunch with Ravi",
					"start": "2026-10-23T14:00:00+05:30", "end": "2026-10-23T15:00:00+05:30",
					"description": "Moved an hour later.", "add_meet_link": true}`),
			},
			{Name: "delete_event", Tool: "calendar_delete_event", Input: json.RawMessage(`{"event_id": "n3wev3nt01"}`), Cassettes: []string{"delete-event"}},
		},
		Invalid: []toolsettest.Call{
			{Name: "end_before_start", Tool: "calendar_create_event", Input: json.RawMessage(`{"summary": "x", "start": "2026-10-23T14:00:00+05:30", "end": "2026-10-23T13:00:00+05:30"}`)},
			{Name: "time_without_offset", Tool: "calendar_create_event", Input: json.RawMessage(`{"summary": "x", "start": "2026-10-23T13:00:00", "end": "2026-10-23T14:00:00"}`)},
			{Name: "mixed_kinds", Tool: "calendar_create_event", Input: json.RawMessage(`{"summary": "x", "start": "2026-10-23", "end": "2026-10-24T14:00:00+05:30"}`)},
			{Name: "range_over_a_year", Tool: "calendar_list_events", Input: json.RawMessage(`{"time_min": "2026-01-01T00:00:00Z", "time_max": "2027-06-01T00:00:00Z"}`)},
			{Name: "bad_attendee", Tool: "calendar_create_event", Input: json.RawMessage(`{"summary": "x", "start": "2026-10-23", "attendees": ["not an email"]}`)},
			{Name: "bad_send_updates", Tool: "calendar_delete_event", Input: json.RawMessage(`{"event_id": "e", "send_updates": "everyone"}`)},
			{Name: "empty_update", Tool: "calendar_update_event", Input: json.RawMessage(`{"event_id": "e"}`)},
			{Name: "too_many_calendars", Tool: "calendar_free_busy", Input: manyCalendars(21)},
			{Name: "blank_event_id", Tool: "calendar_get_event", Input: json.RawMessage(`{"event_id": "  "}`)},
		},
		ErrorBody: func(message string) []byte {
			b, _ := json.Marshal(map[string]any{"error": map[string]any{"code": 400, "message": message, "status": "INVALID_ARGUMENT"}})
			return b
		},
		SettingCalls: []toolsettest.Call{
			{
				Name: "list_events_in_a_zone", Tool: "calendar_list_events", Input: json.RawMessage(`{` + window + `}`),
				Settings: json.RawMessage(`{"time_zone": "Asia/Kolkata"}`),
			},
			{
				Name: "create_event_in_a_zone", Tool: "calendar_create_event",
				Input:    json.RawMessage(`{"summary": "Lunch", "start": "2026-10-23T13:00:00+05:30", "end": "2026-10-23T14:00:00+05:30"}`),
				Settings: json.RawMessage(`{"time_zone": "Asia/Kolkata"}`),
			},
			{
				Name: "list_events_of_another_calendar", Tool: "calendar_list_events", Input: json.RawMessage(`{` + window + `}`),
				Settings: json.RawMessage(`{"default_calendar_id": "team-ops@example.com"}`),
			},
		},
		Truncation: &toolsettest.Truncation{
			Call: "get_event", Field: "/description", Limit: 100,
			Settings: json.RawMessage(`{"max_description_chars": 100}`),
		},
		AttemptTimeout: &toolsettest.AttemptTimeout{Settings: json.RawMessage(`{"timeout": "300ms"}`), Timeout: 300 * time.Millisecond},
	}
}

func manyCalendars(n int) json.RawMessage {
	ids := make([]string, n)
	for i := range ids {
		ids[i] = "c" + string(rune('a'+i)) + "@example.com"
	}
	b, _ := json.Marshal(map[string]any{"calendar_ids": ids})
	return b
}

func TestConformance(t *testing.T) {
	t.Parallel()
	toolsettest.RunConnector(t, connector())
}

func FuzzResponsesListEvents(f *testing.F) {
	toolsettest.FuzzResponses(f, connector(), "calendar_list_events")
}

func FuzzResponsesGetEvent(f *testing.F) {
	toolsettest.FuzzResponses(f, connector(), "calendar_get_event")
}

func FuzzResponsesFreeBusy(f *testing.F) {
	toolsettest.FuzzResponses(f, connector(), "calendar_free_busy")
}
