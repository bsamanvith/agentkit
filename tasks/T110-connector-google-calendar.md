---
id: T110
title: "connectors/google/calendar: list calendars and events, create, update and delete events, free/busy"
epic: E21
status: todo
depends_on: [T108]
---

# T110: `connectors/google/calendar`

## Context

An agent that schedules needs to see a user's calendar and change it carefully. Google Calendar API v3 (`https://www.googleapis.com/calendar/v3`). Decisions: [E21](E21-google-connectors.md). Shared code: [T108](T108-google-common.md); the layout and settings follow [T109](T109-connector-gmail.md).

## Scope

- **Factory:** `calendar.Factory(opts...)`, type `calendar`, tools `calendar_*`.
- **Settings:** `common.Settings`, `read_only` (default true: only the read tools), `default_calendar_id` (default `primary`), `time_zone` (an IANA name; default the calendar's own).
- **Tools:**
  - `calendar_list_calendars` (read/low): `{calendars: [{id, summary, primary, access_role, time_zone}]}`.
  - `calendar_list_events` (read/low): `calendar_id`, `time_min`, `time_max` (RFC 3339; default now to +7 days), `query`, `max_results` (default 20, at most 100), `page_token`. Recurring events are expanded (`singleEvents=true`, ordered by start). Returns `{events: [{id, summary, start, end, all_day, location, attendees (email, status), organizer, html_link, description (cut)}], next_page_token?}`.
  - `calendar_get_event` (read/low): `calendar_id`, `event_id`.
  - `calendar_free_busy` (read/low): `time_min`, `time_max`, `calendar_ids` (at most 20). Returns busy intervals per calendar.
  - `calendar_create_event` (write/medium): `summary`, `start`, `end` (RFC 3339 date-time, or `YYYY-MM-DD` for all-day), `attendees`, `description`, `location`, `add_meet_link`. **`send_updates` defaults to `none`**: inviting guests by email needs `all` or `externalOnly` said explicitly.
  - `calendar_update_event` (write/medium): `event_id` and the fields to change (PATCH semantics); `send_updates` as above.
  - `calendar_delete_event` (delete/high, approval): `event_id`, `send_updates`.
- **Scopes:** `calendar.readonly`; `calendar.events` for the write tools.

## Out of scope

- Creating or sharing calendars, ACLs, reminders settings, push notifications, calendar-wide settings.

## Acceptance criteria

- [ ] **Kit:** passes `toolsettest.RunConnector` and fuzz targets; synthetic cassettes validated against Calendar's discovery document.
- [ ] **Time:** RFC 3339 with offsets and all-day dates parse and are sent as the API wants (`dateTime` with `timeZone`, or `date`); `end` before `start`, a range over one year and a bad value are `ErrInvalidInput` and make no request.
- [ ] **Events:** recurring events appear as instances; all-day events have `all_day: true` and date-only `start`/`end`.
- [ ] **Guests** are never emailed unless `send_updates` says so (checked on the request sent).
- [ ] **Gating:** with default settings no write or delete tool exists; `calendar_delete_event` is delete/high with approval.
- [ ] **Free/busy** merges nothing and reports per-calendar errors from the response instead of failing the call.
- [ ] **Descriptions** are cut with the E21 marker.
- [ ] **Mutation list** recorded.
