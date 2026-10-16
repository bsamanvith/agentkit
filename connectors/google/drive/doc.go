// Package drive is the Google Drive connector: a toolset factory whose tools
// search, read, create, change, copy, trash and share the user's Drive files.
//
// Tools (all prefixed "drive_"): search, get_file, read_file, create_file,
// update_file, copy_file, trash_file, list_permissions and share_file. Reads
// are read/low; writes are write/medium; share_file is write/high and, unless
// the toolset's settings allow it, cannot make a file public.
//
// The connector has no credentials of its own. Every request carries the
// calling user's OAuth access token, which a [TokenSource] supplies (the
// OAuth core, pkg/mcp/oauth with its "google" profile, is the real one). Use
// the toolset's exclude list for a read-only toolset, and the narrower
// [ScopeReadonly] when only reads are included.
package drive
