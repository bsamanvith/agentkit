// Package salesforce is the "salesforce" toolset: SOQL queries, text search,
// records and object descriptions from a Salesforce org, and with
// "read_only": false, creating and updating records. It calls the REST API
// (<instance_url>/services/data/v61.0) for one user's org.
//
// # Access
//
// The tools act for the user each call runs for, with the OAuth token of the
// org they connected to the toolset's credential slot
// (`"credential": {"slot": "salesforce", "scope": "user"}`; the "salesforce"
// profile of credentials.oauth2 keeps the token's instance_url and asks for
// a refresh token). Each call sends to that user's instance_url, which must
// be https on a Salesforce domain (*.salesforce.com, *.force.com,
// *.salesforce.mil). Without a slot, "access_token_env" (default
// SALESFORCE_ACCESS_TOKEN) names a variable holding a token and
// "instance_url" the org, for development.
//
// # Tools
//
//   - salesforce_query (read): a SOQL SELECT, a page at a time, records
//     flattened ("Account.Name").
//   - salesforce_search (read): plain text across objects; the connector
//     writes the SOSL and escapes the text.
//   - salesforce_get_record, salesforce_describe_object (read).
//   - salesforce_create_record, salesforce_update_record (write, medium
//     risk, "read_only": false).
//
// Nothing deletes. Text values are cut at "max_value_chars" (4,000), and a
// query returns at most "max_records" (200) records a call. Record content
// is written by other people; the tool descriptions say to treat it as data.
package salesforce
