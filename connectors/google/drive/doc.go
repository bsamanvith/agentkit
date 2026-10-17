// Package drive is the Google Drive connector (T111, E21): a toolset
// factory, type "drive", whose tools search, read, create, change, share and
// trash the files in a user's Drive.
//
// Tools: drive_search_files, drive_get_file, drive_read_file and
// drive_list_folder (read/low) are always there. With "read_only": false the
// toolset also has drive_create_file and drive_update_file (write/medium) and
// drive_trash_file (delete/high, approval); with "allow_share": true as well,
// drive_share_file (send/high, approval). Nothing deletes permanently, and
// nothing shares with "anyone with the link" or a whole domain.
//
// The tools act for the user whose token the factory's [WithTokenSource]
// yields (the OAuth core, T095, once the connect API exists) or, for
// development and one operator-owned account, the variable "access_token_env"
// names. Requests, errors, retries and settings are common's (T108).
//
// Scopes: [Scopes] lists what the settings' tools need. Drive's broad scopes
// are restricted by Google: outside users need app verification before they
// work. drive.file ("narrow_scopes": true) sees only files the app created or
// the user opened with it, and needs no verification for those.
//
// Content is somebody's document and can carry prompt injection; the tool
// descriptions say to treat it as data.
package drive
