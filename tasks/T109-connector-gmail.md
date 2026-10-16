---
id: T109
title: "connectors/google/gmail: search, read, label, draft and send mail"
epic: E21
status: todo
depends_on: [T108]
---

# T109: `connectors/google/gmail`

## Context

Mail is the first thing people ask an agent to look at. This connector gives an agent a user's Gmail through the Gmail API v1 (`https://gmail.googleapis.com`), acting as `me`. Decisions: [E21](E21-google-connectors.md). Shared code: [T108](T108-google-common.md).

## Scope

- **Factory:** `gmail.Factory(opts...)`, type `gmail`, options as T108's notes.
- **Settings** (all optional): `access_token_env`, `base_url`, `timeout` from `common.Settings`; plus
  - `allow_send` (default false): turns on `gmail_send_message` and `gmail_send_draft`;
  - `allow_modify` (default true): turns on `gmail_modify_labels`;
  - `max_body_chars` (default 8,000, at most 100,000): the cap on a message body.
- **Tools** (`gmail_*`):
  - `gmail_search_messages` (read/low): `query` in Gmail search syntax, `label_ids`, `max_results` (default 10, at most 50), `include_spam_trash`, `page_token`. Returns `{messages: [{id, thread_id, from, to, subject, date, snippet, label_ids}], next_page_token?}`. One `messages.list` plus `messages.get?format=metadata` per message, up to 5 at a time.
  - `gmail_read_message` (read/low): `id`, optional `max_body_chars`. Returns headers (`from`, `to`, `cc`, `subject`, `date`, `message_id`, `reply_to`), `label_ids`, `body` as text, `attachments: [{filename, mime_type, size, attachment_id}]` (names and sizes, never contents). The body is the `text/plain` part; HTML-only mail is reduced to text. Cut with the E21 marker.
  - `gmail_read_thread` (read/low): `id`, optional `max_body_chars` (shared across the thread). Messages oldest first.
  - `gmail_list_labels` (read/low): `{labels: [{id, name, type}]}`.
  - `gmail_modify_labels` (write/medium): `ids` (at most 50), `add_label_ids`, `remove_label_ids`. Marks read (`UNREAD`), archives (`INBOX`), stars (`STARRED`). `TRASH` and `SPAM` are refused: no deleting through this tool.
  - `gmail_create_draft` (write/medium): `to`, `cc`, `bcc`, `subject`, `body` (plain text), optional `reply_to_message_id` (sets `threadId`, `In-Reply-To` and `References`, defaults the subject to `Re: …` and `to` to the original's `Reply-To` or `From`). Returns `{draft_id, message_id, thread_id}`.
  - `gmail_send_draft` (send/high, approval): `draft_id`.
  - `gmail_send_message` (send/high, approval): the draft's inputs, sent at once.
- **Composing:** RFC 5322 built by us (`net/mail` to validate addresses, `mime` to encode a non-ASCII subject, UTF-8 body). Any CR or LF in a header value is refused (header injection); at most 100 recipients; body at most 100,000 characters.
- **Scopes** (exported constants): `gmail.readonly` for the read tools; `gmail.modify` for `gmail_modify_labels`; `gmail.compose` for drafts; `gmail.send` for sending. A tool left out by settings does not add its scope. Gmail's scopes are restricted: outside users need Google's app verification (recorded in Notes).

## Out of scope

- Attachment contents, sending attachments, HTML mail composition, filters, settings, push notifications (watch), delete and empty-trash.
- Per-user token plumbing (T096): this task takes a `TokenSource`.

## Acceptance criteria

- [ ] **Kit:** passes `toolsettest.RunConnector` with `access_token_env` as the key, and a `FuzzResponses` target per read tool; cassettes (synthetic until recorded) validated against Gmail's discovery document.
- [ ] **Search** returns the fields above in the order Gmail gave, follows `nextPageToken` as `next_page_token`, and makes at most 1 + 50 requests.
- [ ] **Read** returns plain text from multipart/alternative, from HTML-only mail, from nested multipart/mixed with attachments, and from a base64url body with non-ASCII text; attachments are listed, not fetched.
- [ ] **Truncation** is at a rune boundary with a marker that names `max_body_chars`.
- [ ] **Compose:** a draft's raw message round-trips: parsed back, its headers and body equal the input (non-ASCII subject and body included); a CR/LF in any header field, a bad address, 101 recipients and an empty message are `ErrInvalidInput` and make no request.
- [ ] **Reply** carries `threadId`, `In-Reply-To` and `References` of the original.
- [ ] **Gating:** with default settings the send tools and, with `allow_modify: false`, `gmail_modify_labels` are not in the toolset; the send tools are classified send/high with approval.
- [ ] **Labels:** `TRASH` and `SPAM` are refused; an empty change is refused.
- [ ] **Errors and limits** as E21 (kit suite).
- [ ] **Mutation list** recorded.

## Notes

- Live check needs a test Google account with a token (`GMAIL_TEST_ACCESS_TOKEN`); the live test skips without it. Cassette recording is the same. Leave `status: review` until done.
