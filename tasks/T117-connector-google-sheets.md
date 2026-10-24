---
id: T117
title: "connectors/google/sheets: read, append and update spreadsheet values"
epic: E22
status: done
depends_on: [T116]
---

# T117: `connectors/google/sheets`

## Context

[E22](E22-connectors-and-tool-picker.md) asks for Sheets beside Gmail and Drive. Drive can export a sheet's first tab as CSV; Sheets reads any tab and range and writes cells. Google Sheets API v4 (`https://sheets.googleapis.com/v4`). Family rules: [E21](E21-google-connectors.md), [`write-connector`](../.agents/skills/write-connector/SKILL.md).

## Scope

- **Factory:** `sheets.Factory(opts...)`, type `sheets`, tools `sheets_*`, `common.Settings` plus `read_only` (default true) and `max_cells` (default 2,000, at most 20,000).
- **Tools:**
  - `sheets_get_spreadsheet` (read/low): `spreadsheet_id` → title, tabs (`title`, `sheet_id`, rows, columns).
  - `sheets_read_range` (read/low): `spreadsheet_id`, `range` in A1 notation (`Sheet1!A1:D20`; a tab name alone reads the tab) → `values` as rows of strings, cut at `max_cells` with a marker naming the next range.
  - `sheets_append_rows` (write/medium, needs `read_only: false`): `spreadsheet_id`, `range` (the tab), `rows`; `valueInputOption=USER_ENTERED`.
  - `sheets_update_range` (write/medium, needs `read_only: false`): `spreadsheet_id`, `range`, `rows`.
  - `sheets_create_spreadsheet` (write/medium, needs `read_only: false`): `title`, optional tab names.
- **Scopes:** `spreadsheets.readonly`, or `spreadsheets` with writes on; `sheets.Scopes(settings)`.
- **Wiring:** `catalog.go`, the deploy configs (T120), QUICKSTART.

## Out of scope

- Formatting, charts, formulas beyond what `USER_ENTERED` parses, deleting tabs or sheets.

## Acceptance criteria

- [x] **Kit:** passes `toolsettest.RunConnector` with synthetic cassettes and a fuzz target per read tool — tests: `TestConformance`, `FuzzResponses*`
- [x] A1 ranges are checked before any request (empty, a tab name with a quote, a range over the cell cap) — test: `TestRangeValidation`
- [x] A read over `max_cells` is cut with a marker naming the next range — test: `TestReadCutsAtMaxCells`
- [x] Default settings expose only the read tools; writes are medium risk and need `read_only: false` — test: `TestGatingFollowsSettings`
- [x] Append and update send `USER_ENTERED` and the rows as given — test: `TestWritesSendUserEnteredValues`
- [x] A slot token is used per caller (shared with T116's test) — test: `TestUsesSlotToken`

## Notes

- Settings also take `max_cell_chars` (default 5,000): one cell's text is cut like any other long content, so a huge cell cannot use the read's whole budget.
- The read cut is on whole rows, so `next_range` always starts a row; `TestNextRange` covers ranges with and without an end row and with a quoted tab.
- Wired into `catalog.go` and `credentialEnv`; the deploy configs come with T120. Size: [docs/SIZES.md](../docs/SIZES.md) row of 2026-10-24.
