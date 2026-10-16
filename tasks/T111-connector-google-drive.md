---
id: T111
title: "connectors/google/drive: search, read, create, share and trash files"
epic: E21
status: review
depends_on: [T108]
---

# T111: `connectors/google/drive`

## Context

Files are where the work lives: docs, sheets, slides, PDFs. This connector lets an agent find and read a user's Drive files, and with settings, create and share them. Google Drive API v3 (`https://www.googleapis.com/drive/v3`, uploads at `/upload/drive/v3`). Decisions: [E21](E21-google-connectors.md). Shared code: [T108](T108-google-common.md); layout and settings follow [T109](T109-connector-gmail.md).

## Scope

- **Factory:** `drive.Factory(opts...)`, type `drive`, tools `drive_*`.
- **Settings:** `common.Settings`, `read_only` (default true), `allow_share` (default false: the share tool), `max_content_chars` (default 20,000, at most 200,000), `max_download_bytes` (default 5 MiB, at most 8 MiB).
- **Tools:**
  - `drive_search_files` (read/low): `query` as plain text matched on name and full text (the connector builds the `q`, escaping quotes), or `drive_query` for Drive's own query syntax; `mime_type`, `folder_id`, `max_results` (default 20, at most 100), `page_token`. Returns `{files: [{id, name, mime_type, modified_time, size?, owners, web_view_link, parents}], next_page_token?}`. Shared drives included.
  - `drive_get_file` (read/low): metadata of one file.
  - `drive_read_file` (read/low): `file_id`, `max_chars`. Google Docs export as text (markdown if the API offers it), Sheets as CSV (first sheet; `sheet_gid` for another), Slides as text, plain-text and JSON/CSV/markdown files downloaded; anything else (images, PDFs, binaries) is refused with its type and size rather than returned as bytes. Cut with the E21 marker.
  - `drive_list_folder` (read/low): `folder_id` (default root), paging as search.
  - `drive_create_file` (write/medium): `name`, `content` (text), `mime_type` (`text/plain`, `text/markdown`, `text/csv`, or a Google type to convert on import: Doc, Sheet), `folder_id`.
  - `drive_update_file` (write/medium): `file_id`, `content` and/or `name`. A Google-native file's content is replaced only by an import of the same family; refused otherwise.
  - `drive_share_file` (send/high, approval; needs `allow_share`): `file_id`, `email`, `role` (`reader` or `commenter` or `writer`), `notify` (default false). No "anyone with the link".
  - `drive_trash_file` (delete/high, approval): moves to trash; there is no permanent delete.
- **Scopes:** `drive.readonly` for the read tools; `drive.file` (files the app created or the user opened with it) for create/update when the settings ask for the narrow mode, else `drive`. Drive's broad scopes are restricted: outside users need verification (record in Notes).

## Out of scope

- Binary upload and download, resumable uploads, shared-drive administration, comments, revisions, push notifications, "anyone with the link" sharing.

## Acceptance criteria

- [x] **Kit:** passes `toolsettest.RunConnector` and a fuzz target for each read tool; the cassettes are synthetic (written from the v3 reference). `cassettes.openapi`, `settings.honored` and `limits.rune_boundary` are skipped with reasons (Notes) — tests: `TestConformance`, `FuzzResponses*`. Discovery-document validation is not done yet
- [x] **Query building:** user text with quotes, backslashes and `'` cannot break out of the `q` (property test over the escaper); `drive_query` is passed through unchanged — tests: `TestQuoteCannotBreakOut`, `TestSearchQueryBuildsOneExpression`.
- [x] **Read** exports a Doc, a Sheet and a Slides deck, downloads a text file, refuses a PDF and a file over `max_download_bytes` with a message naming type or size, and cuts long text with the marker — tests: `TestReadFile`, `TestReadCutsAtARuneBoundary`, the `read_q3_report` call.
- [x] **Share** never grants to `anyone`/`domain`; `notify` defaults to false; the tool is absent without `allow_share` — test: `TestShare`, `TestGatingFollowsSettings`.
- [x] **Trash** sets `trashed: true`; no request ever uses `DELETE` — test: `TestTrashSetsTrashedAndNeverDeletes`.
- [x] **Gating:** default settings have only the read tools; destructive and sending tools are classified delete/send, high, with approval — tests: `TestGatingFollowsSettings`, `TestToolClasses`.
- [ ] **Mutation list** recorded.

## Notes

- **Settings beyond the scope list:** `narrow_scopes` (default false) chooses `drive.file` over `drive` when the write tools are on; `drive.Scopes(settings)` returns the scopes a configuration needs. `allow_share: true` with `read_only: true` fails `Build`, because the share tool is a write tool.
- **Differences from the scope above:** `drive_share_file` takes an optional `type` (`user`, the default, or `group`), since Drive refuses a group address as a user. `sheet_gid` is not offered: the Drive export API exports a spreadsheet's first sheet only, and other sheets need a different host and URL. Docs export as `text/markdown`. `drive_create_file`'s `mime_type` is `text/plain`, `text/markdown`, `text/csv`, `google_doc` or `google_sheet`. `drive_update_file` replaces content only for text files, Docs (from text) and Sheets (from CSV); every other Google type and every binary is refused.
- **Skipped kit checks:** `cassettes.openapi` (Google publishes Drive as a discovery document, not OpenAPI), `settings.honored` (no setting changes a request) and `limits.rune_boundary` (the text is a raw body, not a JSON field; covered by `TestReadCutsAtARuneBoundary`).
- **Uploads** (create and update with content) are one `multipart/related` request, which the kit's cassettes cannot match (they match JSON bodies), so the kit's calls cover the JSON paths (an empty Doc, a rename) and `TestCreateAndUpdateUploadContent` covers the upload. It needs `common.Request.Raw` and a `Content-Type` and `Accept` that `Header` can set.
- **Not yet done:** recording the cassettes with a test account (`DRIVE_TEST_ACCESS_TOKEN`; no live test exists yet), the discovery-document validation, the console's credential line for Google toolsets (`credentialOf` in `cmd/agentkit/agent.go` reads `api_key_env`, not `access_token_env`), and the mutation list. Leave `status: review` until the cassettes are recorded.
- **Restricted scopes:** `drive` and `drive.readonly` are restricted by Google: an app used by people outside the owner's organization needs Google's verification and a security assessment. `drive.file` is not restricted.
