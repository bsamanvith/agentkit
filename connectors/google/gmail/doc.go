// Package gmail is the toolset factory for Gmail: an agent searches, reads,
// labels and drafts the connected user's mail, and with a setting, sends it.
// It calls the Gmail API v1 (https://gmail.googleapis.com) with plain
// net/http, through the Google connectors' shared client
// ([github.com/bsamanvith/agentkit/connectors/google/common]), acting as the
// user "me".
//
// # Tools
//
//	gmail_search_messages  read   {"query", "label_ids", "max_results", "include_spam_trash", "page_token"}
//	gmail_read_message     read   {"id", "max_body_chars"}
//	gmail_read_thread      read   {"id", "max_body_chars"}
//	gmail_list_labels      read   {}
//	gmail_modify_labels    write  {"ids", "add_label_ids", "remove_label_ids"}    (allow_modify)
//	gmail_create_draft     write  {"to", "cc", "bcc", "subject", "body", "reply_to_message_id"}
//	gmail_send_draft       send   {"draft_id"}                                    (allow_send)
//	gmail_send_message     send   {"to", "cc", "bcc", "subject", "body", "reply_to_message_id"}  (allow_send)
//
// A search returns each message's id, sender, recipients, subject, date and
// snippet, and a next_page_token. Reading returns the headers, the body as
// text (the text/plain part, else HTML reduced to text, cut at max_body_chars
// with a marker that says how to get more) and the attachments' names, types
// and sizes; attachment contents are never fetched. The send tools are
// send/high and ask for approval; the others are read/low or write/medium.
//
// The mail is written by other people. Tool descriptions tell the model to
// treat it as data, not instructions; nothing is filtered.
//
// Writing a message: addresses are parsed with net/mail, a line break in any
// header field is refused (it would inject a header), there are at most 100
// recipients, and a non-ASCII subject is encoded. With reply_to_message_id the
// message joins the original's conversation (threadId, In-Reply-To,
// References), and the recipient and subject default to the original's.
//
// # Settings
//
// All optional:
//
//	{"access_token_env": "GOOGLE_ACCESS_TOKEN",
//	 "base_url": "https://gmail.googleapis.com",
//	 "timeout": "30s",
//	 "allow_send": false,       turns on gmail_send_draft and gmail_send_message
//	 "allow_modify": true,      gmail_modify_labels; false for a toolset that only reads and drafts
//	 "max_body_chars": 8000}    default cap on a body, 100 to 100,000
//
// # Access
//
// The tools act with one user's OAuth access token. [WithTokenSource]
// supplies it per call (the OAuth core and the credential vault, once the
// connect flow exists). Without one, access_token_env names an environment
// variable holding a token: for development and one operator-owned account,
// and it expires within an hour. A literal token in settings is refused.
// [Scopes] returns the OAuth scopes a toolset's settings need: gmail.readonly
// and gmail.compose, plus gmail.modify and gmail.send when those tools are on.
// Gmail's scopes are restricted: for users outside the Google Cloud project's
// organisation the app needs Google's verification.
//
// # Errors
//
// As for every Google connector (see the common package): a 400 or 422 gives
// [tool.ErrInvalidInput] with Google's message; a 401 says the account must
// be reconnected; a 429 or a quota 403 is retried once; the rest give
// [tool.ErrToolFailed]. A write is never retried after it may have run.
package gmail
