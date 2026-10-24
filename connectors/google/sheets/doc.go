// Package sheets is the Google Sheets connector (T117, E22): a toolset
// factory, type "sheets", whose tools read a user's spreadsheets by range
// and, with settings, write cells and create spreadsheets.
//
// Tools: sheets_get_spreadsheet and sheets_read_range (read/low) are always
// there. With "read_only": false the toolset also has sheets_append_rows,
// sheets_update_range and sheets_create_spreadsheet (write/medium). Nothing
// deletes tabs, sheets or files.
//
// The tools act for the user whose Google account is connected to the
// toolset's credential slot (T116), or whose token [WithTokenSource]
// yields, or, for development, the variable "access_token_env" names.
// Requests, errors, retries and settings are common's (T108).
//
// Scopes: [Scopes] lists what the settings' tools need: spreadsheets.readonly,
// or spreadsheets with the write tools on. Cells are written by other people
// and can carry prompt injection; the tool descriptions say to treat them as
// data.
package sheets
