// Package calendar is the Google Calendar connector: a user's calendars and
// events as a [toolset.Factory] of type "calendar", over the Calendar API v3
// (https://developers.google.com/calendar/api) with plain net/http through
// [common.Client] (T110, E21).
//
// Read tools (always present):
//
//   - calendar_list_calendars: the calendars the user can see.
//   - calendar_list_events: events in a time range (default the next 7 days,
//     at most a year), recurring events expanded into instances, in start
//     order, with a page token to continue.
//   - calendar_get_event: one event, with its description.
//   - calendar_free_busy: busy intervals of up to 20 calendars. A calendar
//     the API cannot read reports its errors; the call does not fail.
//
// Write tools (only with "read_only": false):
//
//   - calendar_create_event and calendar_update_event (write/medium). update
//     is a PATCH: only the fields given change.
//   - calendar_delete_event (delete/high, needs approval).
//
// Guests are never emailed unless the call's send_updates says "all" or
// "externalOnly": the default is "none".
//
// Times are RFC 3339 date-times with an offset, or YYYY-MM-DD dates for
// all-day events (whose end is the day after the last day, as in the API).
// An end before a start, mixed kinds and a range over a year are
// [tool.ErrInvalidInput] and make no request.
//
// Event text is written by other people and can carry prompt injection; the
// tool descriptions say to treat it as data. Descriptions are cut with the
// E21 marker: at max_description_chars for one event, and at 300 in a list.
//
// Auth is a Google access token for the user, from [WithTokenSource] (the
// OAuth core, once the connect API exists) or from the environment variable
// access_token_env names (default GOOGLE_ACCESS_TOKEN). Settings never hold a
// token. [Scopes] returns the OAuth scopes the settings need.
//
// Settings (all optional):
//
//	{
//	  "access_token_env": "GOOGLE_ACCESS_TOKEN",
//	  "base_url": "https://www.googleapis.com/calendar/v3",
//	  "timeout": "30s",
//	  "read_only": true,
//	  "default_calendar_id": "primary",
//	  "time_zone": "Asia/Kolkata",
//	  "max_description_chars": 4000
//	}
//
// time_zone is an IANA name: events are shown in it and the times a tool
// sends carry it. Without it the calendar's own zone applies. Building the
// toolset makes no request.
//
// Errors follow T108: the model's mistakes are [tool.ErrInvalidInput],
// everything else [tool.ErrToolFailed], with the token redacted.
package calendar
