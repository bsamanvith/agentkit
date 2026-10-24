// Package hubspot is the "hubspot" toolset: search and read a HubSpot
// account's CRM records (contacts, companies, deals, tickets) and, with
// "read_only": false, create and update them and add notes. It calls the CRM
// API v3 (https://api.hubapi.com) for one user's account.
//
// # Access
//
// The tools act for the user each call runs for, with the OAuth token of the
// HubSpot account they connected to the toolset's credential slot
// (`"credential": {"slot": "hubspot", "scope": "user"}`; the "hubspot"
// profile of credentials.oauth2 has HubSpot's endpoints). [Scopes] lists the
// scopes a configuration needs. Without a slot, "access_token_env" (default
// HUBSPOT_ACCESS_TOKEN) names a variable holding a private app's token, for
// development and for one shared account.
//
// # Tools
//
//   - hubspot_search (read): records of one object type matching a query
//     and property filters, a page at a time.
//   - hubspot_get_record (read): one record's properties and the ids of the
//     records associated with it.
//   - hubspot_create_record, hubspot_update_record (write, medium risk,
//     "read_only": false): set a record's properties.
//   - hubspot_create_note (write, medium risk, "read_only": false): a note
//     on one or more records.
//
// Nothing deletes. Property values are cut at "max_value_chars" (default
// 4,000) with a marker. Record content is written by other people; the tool
// descriptions say to treat it as data.
package hubspot
