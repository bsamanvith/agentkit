---
id: T109
title: "connectors/google/gmail: search, read, label, draft and send mail"
epic: E21
status: review
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

- [x] **Kit:** passes `toolsettest.RunConnector` with `GOOGLE_ACCESS_TOKEN` as the key, and a `FuzzResponses` target per read tool — test: `gmail_test.TestConformance`, `FuzzSearchResponses`, `FuzzReadMessageResponses`, `FuzzReadThreadResponses`. Cassettes are **synthetic**, written from the API reference; not validated against the discovery document (the kit's check is OpenAPI-only, skipped with that reason). Left unticked below: recording them.
- [x] **Search** returns the fields above in the order Gmail gave, follows `nextPageToken` as `next_page_token`, and makes at most 1 + 50 requests.
- [x] **Read** returns plain text from multipart/alternative, from HTML-only mail, from nested multipart/mixed with attachments, and from a base64url body with non-ASCII text; attachments are listed, not fetched.
- [x] **Truncation** is at a rune boundary with a marker that names `max_body_chars`.
- [x] **Compose:** a draft's raw message round-trips: parsed back, its headers and body equal the input (non-ASCII subject and body included); a CR/LF in any header field, a bad address, 101 recipients and an empty message are `ErrInvalidInput` and make no request.
- [x] **Reply** carries `threadId`, `In-Reply-To` and `References` of the original.
- [x] **Gating:** with default settings the send tools and, with `allow_modify: false`, `gmail_modify_labels` are not in the toolset; the send tools are classified send/high with approval.
- [x] **Labels:** `TRASH` and `SPAM` are refused; an empty change is refused.
- [x] **Errors and limits** as E21 (kit suite).
- [x] **Mutation list** recorded (Notes).

## Notes

- Live check needs a test Google account with a token (`GMAIL_TEST_ACCESS_TOKEN`); the live test skips without it. Cassette recording is the same. Leave `status: review` until done.

### Tests that cover the criteria

| Criterion | Test |
|---|---|
| Search: fields, order, `next_page_token`, 1 + N requests, five at a time | `TestSearchFetchesEachMessageOnceFewAtATimeAndKeepsGmailsOrder`, `TestSearchFailsWholeWhenOneMessageFails` |
| Read: plain, HTML-only, alternative, mixed with attachments, non-ASCII, bad base64 | `TestReadMessageFindsTheTextInEveryMessageShape` |
| Truncation | `TestBodyIsCutAtARuneBoundaryAndSaysHowToGetMore`, `TestBodyLimitSetting`, `TestReadThreadSharesTheBodyBudget` |
| Compose round trip | `TestDraftRoundTripsHeadersAndNonASCIIText` |
| Invalid input makes no request | kit `invalid` checks, `TestInvalidMessagesMakeNoRequest` |
| Reply | `TestReplyKeepsTheConversationAndDefaultsRecipientAndSubject` |
| Gating and classification | `TestSendToolsAndModifyAreGatedBySettingsAndClassified`, `TestScopesFollowTheSettings` |
| Labels | `TestModifyLabels` |
| Token per call | `TestTokenComesFromTheSourcePerCallAndNeverFromTheEnvironmentWhenGiven`, `TestBuildNeedsATokenAndRefusesBadSettings` |

### Mutation list (2026-10-16)

| Planted bug | Caught by |
|---|---|
| line-break check on a subject removed | `TestInvalidMessagesMakeNoRequest`, kit `invalid` |
| `TRASH`/`SPAM` allowed in modify | `TestModifyLabels`, kit `invalid` |
| send tools always built | `TestSendToolsAndModifyAreGatedBySettingsAndClassified` |
| modify tool always built | same |
| send tools not approval-gated | same |
| HTML preferred over plain | `TestReadMessageFindsTheTextInEveryMessageShape`, kit golden |
| snippet entities not unescaped | `TestSearchFetches...`, kit golden |
| metadata fan-out unbounded (500) | `TestSearchFetchesEachMessageOnceFewAtATimeAndKeepsGmailsOrder` |
| search results out of order | same |
| reply drops the References chain / the thread id | `TestReplyKeepsTheConversation...` |
| subject not RFC 2047 encoded | `TestDraftRoundTripsHeadersAndNonASCIIText` |
| body not cut | `TestBodyIsCut...`, `TestBodyLimitSetting`, `TestReadThreadSharesTheBodyBudget` |
| attachments not listed | `TestReadMessageFindsTheTextInEveryMessageShape` |

**Equivalent mutant:** removing the line-break check on addresses survives, because `net/mail` refuses an address with a line break by itself. The check stays as a second line of defence against header injection.

### State and what is left

- **Status `review`.** Cassettes are synthetic (hand-written from the API reference). Left to do with a Google test account: a live test (`//go:build live`, skipped without `GMAIL_TEST_ACCESS_TOKEN`), recording the cassettes with `make record-cassettes`, then `make golden`. The thread-reading path (`gmail_read_thread`) and the compose path have never met the real API.
- **Registered in the catalog** (`cmd/agentkit/catalog.go`): type `gmail`, token from `GOOGLE_ACCESS_TOKEN` until T096. Binary +393,216 bytes (docs/SIZES.md).
- **Scopes:** Gmail's are restricted. Using the connector with accounts outside the OAuth client's own Google Cloud organisation needs Google's app verification (and a security assessment for the restricted scopes); testing with the project's own accounts needs none.
- The Google OAuth client (ID and secret from Google Cloud Console) is the owner's to create: the `google` profile has no client built in.
