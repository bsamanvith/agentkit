---
id: T096
title: "Add MCP servers at runtime: API and console (add by URL, probe, connect, review and pin tools, assign to agents), SSRF guard"
epic: E19
status: done
depends_on: [T094, T062, T040, T082]
---

# T096: MCP servers API and console

## Context

The easy path the owner asked for: an admin pastes a URL, agentkit probes it, the admin reviews its tools, and users connect. No config edit, no restart. Decisions: [E19](E19-external-mcp-servers.md). The console screens are designed in [T087](T087-console-ux-design-2.md) (the "Add connector" screen T070 asked for). This task builds them on that design.

## Scope

- **API** (admin scope; in the OpenAPI spec):
  - `POST /v1/toolsets` with `{type: "mcp", name, url, auth: none|headers|oauth, scope: tenant|user, oauth_client?}`. It probes the server and returns:
    - the server's name, version and tool list with fingerprints;
    - whether auth is needed and of which kind;
    - the connect URL when OAuth is needed.
  - `GET /v1/toolsets/{name}/review` lists new, changed and removed tools, each with its raw description, its sanitized description and a diff.
  - `POST /v1/toolsets/{name}/review` approves or rejects tools and sets each tool's kind and risk (E17).
  - `PATCH` enables, disables or edits a toolset; `DELETE` removes it.
- Stored as data in the store (tenant-scoped), next to config-file toolsets, which stay read-only. Agents pick runtime toolsets the same way as config ones (T075 when agents are data).
- **Pinning enforced:**
  - approved fingerprints are stored per tool;
  - on connect or `list_changed`, a changed or new tool is disabled and the toolset shows "review needed";
  - a run sees only approved, unchanged tools.
- **SSRF guard:**
  - `https` only;
  - a dialer that checks the resolved IP at connect time and refuses loopback, private, link-local, CGNAT and cloud metadata addresses (IPv4 and IPv6, IPv4-mapped included);
  - redirects re-checked;
  - an operator allowlist for internal hosts.
- **Console** (on T087's design):
  - Toolsets: "Add MCP server" (URL → probe → auth → review tools → save), the review screen with diffs, and the "review needed" state.
  - Connections: each user's **Connect** for user-scoped servers, using T095's flow.
- **Agent API for harnesses:** everything above is in `/v1` and `agentkit` CLI subcommands (`agentkit toolsets add-mcp <url>`), so Claude Code can add and review servers too.

## Out of scope

- Registry search (T097).
- User-added private servers. The tenant setting exists and defaults to off; turning it on is a later task.

## Acceptance criteria

- [x] **Add by URL:** against a fake MCP server, `POST /v1/toolsets` probes it and returns its tools; after review, an agent using the toolset calls a tool.
- [x] **Rug pull:**
  - the fake server changes a tool's description;
  - the next connect disables that tool and the run no longer offers it;
  - the review shows the diff, and approval restores the tool.
- [x] **New tool:** a tool the server adds later starts disabled.
- [x] **SSRF:** URLs resolving to `127.0.0.1`, `10.x`, `169.254.169.254`, `::1`, `::ffff:127.0.0.1`, and a DNS name that rebinds to a private address between check and connect are all refused; an allowlisted internal host is accepted.
- [x] **Scopes** (needs T095 and T044; part 2 refuses `scope: user`):
  - a `user`-scoped server uses each user's own connection;
  - a user who hasn't connected gets "not connected" as a tool error, never another user's token.
- [x] **Authorization:**
  - non-admins get 403 on add, review and delete;
  - a toolset outside the principal's `Toolsets` is invisible (E07).
- [x] **Console:** the add → review → connect path passes the browser smoke test (`make web-smoke`).
- [x] **Mutation list** recorded (parts 1, 2 and 3 below).

## References

- [Claude custom connectors](https://claude.com/docs/connectors/custom/remote-mcp) and [adding unlisted connectors](https://claude.com/docs/connectors/custom/add-unlisted) (the flow to match: URL → Add → Connect; org owner adds, members connect)
- [MCP security best practices](https://modelcontextprotocol.io/specification/2026-07-28/basic/security_best_practices) (SSRF, token passthrough, confused deputy)
- [OWASP SSRF prevention](https://cheatsheetseries.owasp.org/cheatsheets/Server_Side_Request_Forgery_Prevention_Cheat_Sheet.html)
- docs/UI.md "Connections" and "Toolset skipped"; T087

## Notes

- **Split into three parts** (2026-10-17), so each lands green on its own:
  1. **Guards** (done): `pkg/netguard` and pinning in `pkg/mcp/client`. Usable now for config toolsets.
  2. **Runtime API**: runtime toolsets as tenant data in the store, `POST/GET/PATCH/DELETE /v1/toolsets`, the review endpoints, `agentkit toolsets add-mcp`, every URL through `netguard`, approved fingerprints as the toolset's `pins`.
  3. **Console** on T087's design, once T087 is done.
- Part 1 decisions:
  - **The SSRF check is at connect time**, in the dialer's `Control` hook, on the address actually being connected to. A URL is never resolved to check it, so there is no window for DNS rebinding. `CheckURL` checks only what the URL itself says (scheme, user info, an IP literal). Allowed hosts (`New(allow)`) skip the address check and may use http.
  - **Refused:** loopback, private (RFC 1918, ULA), link-local (metadata), CGNAT, unspecified, multicast, broadcast, and IANA special-purpose ranges (documentation, benchmarking, reserved, Teredo, discard, site-local). IPv4-mapped, 6to4 and NAT64 addresses are judged by the IPv4 address inside; zoned addresses are refused.
  - **No proxy:** the guarded client ignores `HTTPS_PROXY`, since a proxy would make the connection and the checked address would be the proxy's. A deployment that must go through a proxy needs the proxy itself to enforce this.
  - **Redirects:** at most 5, each one through `CheckURL`, and every connection through the dialer.
  - **Pinning without the store:** each wrapped tool remembers the fingerprint the model was given. On `list_changed` the toolset rechecks, and a changed or removed tool fails its calls (`ErrToolFailed` with the reason) until its definition is back. New tools are never added at runtime. The `pins` setting (written by `agentkit mcp-tools <url>`) allows only pinned definitions at startup. Part 2 stores the approved fingerprints and passes them as `pins`.
- Mutations (each failed the package's tests): netguard: no dial-time check, no unmapping, no redirect check, no redirect cap, proxy kept, no 6to4/NAT64, no special ranges, http to any host, user info allowed, IP literal not checked, wildcard matching the apex, zone ignored. Pinning: `Invoke` ignoring a disabled tool, `Recheck` ignoring changes, a removed tool not disabled, never re-enabled, `pins` ignored, unpinned tools allowed, no recheck on `list_changed`, pin format unchecked.
- Part 2 decisions (2026-10-17):
  - **Stored as tenant data:** `store.ToolsetStore` keeps one opaque JSON record per toolset with a version for compare-and-swap, in all three backends (Postgres under RLS). `pkg/mcp/servers` owns the record's shape: the server's URL, transport, auth and headers, and per tool the approved fingerprint and definition, or a rejection, with the class an admin set and who decided when.
  - **Pinning:** a tool is offered only while the server lists it with the approved fingerprint. A new or changed tool is off and the toolset says `review_needed`; the review shows the approved definition next to the current one and the fields that differ. Approving sends the fingerprint the review showed, so a server that changes the tool in between gets `409 conflict`, never a silent approval of something unseen. A removed tool shows as `removed` without flagging a review, since nothing new would reach the model; rejecting it forgets it.
  - **Live, per turn:** the harness's registry reads the toolsets at every turn in the run's tenant, so a review, a disable or a server change takes effect at the next turn, and each call checks again that its tool is still offered. One connection per tenant and toolset, made on first use, re-made when its settings change and retried 30s after a failure; `list_changed` relists.
  - **Auth:** `none` or `headers` only. Header values are `${VAR}` references to variables the operator lists in `runtime_toolsets.header_env`; a header whose name looks like a secret must use one, so the store never holds a secret. `oauth` and `scope: user` are refused until T095 and T044 wire the flow and the vault; a server answering 401 is `422 auth_required`, naming OAuth when it advertises it.
  - **Names:** tools are always `<toolset>__<tool>`; a runtime toolset may not take a config toolset's name. Agents opt in with `runtime_toolsets` in the config (T075 makes agents data).
  - **Scope:** `agent:admin`, which `*` and `AGENTKIT_API_TOKEN` grant. Reading the review is admin too: it shows URLs and raw definitions.
  - **CLI:** `agentkit toolsets add|review|approve|reject|enable|disable|delete|list` over `pkg/agentclient`, instead of the single `add-mcp` the scope named.
  - **Not yet:** agents on a separate runner component (startup refuses `runtime_toolsets` there; the runner would need the store), OAuth and per-user connections (T095, T044), the console (part 3, after T087).
- Part 2 mutations (each failed the tests): offering a tool whose fingerprint changed, offering a disabled toolset's tools, approving without matching the live fingerprint, no per-tenant cap, connections shared across tenants, a secret header without `${VAR}`, no URL check on add (the dial-time guard alone), DELETE without the admin scope, an agent's runtime toolsets left out of the key's toolset check, invoking a tool that is no longer offered, a disabled toolset shown as ready, and serve accepting a config toolset's name as a runtime one. Store: every `RunToolsetStore` mutation was killed except tenant scoping on Postgres, which RLS covers on its own.

- **Status `review`** (part 3, 2026-10-22): the console is built and its criterion ran (`make web-smoke`, below). **Scopes** stays unticked: the API refuses `scope: user` until T095 and T044 wire per-user OAuth, so there is no user-scoped server to connect. The console shows that state as it is (below) rather than a Connect button that cannot work.
- Part 3 (console), on T087's design (docs/UI.md "Adding an MCP server"), all in the lazy admin chunk:
  - **Add an MCP server** (`/toolsets/new`, `pages/mcp-add.tsx`): the four steps, "1 Server · 2 Sign-in · 3 Review tools · 4 Use it", with the current one `aria-current="step"`.
    1. **Server:** "Find a server" searches the registry (T097, `GET /v1/mcp-registry/search`, debounced; hidden on 404; "The registry can't be reached; paste the URL instead." on 502). Picking a remote fills the URL, transport, the name (`suggested_name`) and the headers it takes (a secret one as `${NAME}`); a remote with `allowed: false` is disabled with "not allowed in this workspace". Then the URL and the name (from the host by default: `mcp.linear.app` is `linear`). **Connect** sends `POST /v1/toolsets`.
    2. **Sign-in**, when the server answered 401 (`422 auth_required`), or the registry says it takes a required header: a header whose value is a `${VAR}` of the server's environment. "No sign-in" is off after a 401; **"Each person signs in" is shown disabled, "Not available yet: per-user OAuth … needs T095"**; when the API says the server wants MCP OAuth, a notice says nobody can connect it on Connections yet.
    3. **Review tools:** one card per tool (`pages/mcp-review.tsx`, `ToolCard`): its name as agents call it (`name__tool`), the description exactly as the model gets it (the API's sanitized `description`), what the server sent with invisible characters shown as ⟦U+200B⟧, flags (invisible characters, telling the model to call another tool or about its instructions, a cut description), the kind and risk selects (defaulting to the API's class), the server's hints as "a claim, not a check", and the input schema on demand (the field list and the raw definition). Every tool needs Approve or Reject; "Approve the N without flags" never takes a flagged one. **Save and continue** sends `POST …/review` with the fingerprints shown.
    4. **Use it:** stored agents (with a `version`, not `read_only`) get a new version with the toolset added to `runtime_toolsets` (`GET`, then `POST /v1/agents/{name}/versions` with `if_latest`); config agents are greyed. A draft by default; "Publish the new versions now" publishes.
  - **The review page** (`/toolsets/{name}/review`): the notice ("linear changed 1 tool and added 1 since they were approved."), each changed tool's diffs (description by words, schema by lines, approved → now, with − and +, "Removed:"/"Added:" for screen readers, never colour alone), new tools as cards, rejected ones (approve to turn on), removed ones (Forget it), and the approved ones collapsed. **Save review** needs a decision on every new and changed tool; a `409 conflict` saves nothing, says why and reads the review again.
  - **Review needed** on the toolset's page (a notice with **Review**) and in the list (the row's status). A runtime toolset's page also has Review tools, Turn off/on (`PATCH`) and Remove (asks first; `DELETE`). The Toolsets list head has + ("Add an MCP server"), the phone list page an Add button, and the empty state a button.
  - API client (`agent-ui`): `addToolset`, `getToolsetReview`, `reviewToolset`, `setToolsetEnabled`, `deleteToolset`, `searchMCPRegistry`, `listAgentVersions`, `saveAgentVersion`, with their types. No API change was needed.
- Part 3 decisions:
  - **The URL is refused before a request only for what the server always refuses:** not a URL, a scheme other than http(s), user info, an unfilled `{variable}`. Plain http and private addresses get a warning under the field and go to the server, because `runtime_toolsets.allow_hosts` lets the operator allow them and the console can't know the list (the design refused them outright). The server's reason then shows under the field.
  - **A header that carries a secret** (its name has authorization, key, token, secret, cookie, auth, session, signature, password, or its value looks like a key: see "Part 3 review") must have a `${VAR}` value, checked in the browser, so a key pasted by mistake is never sent. The server checks the variables against `header_env`, which the API doesn't list, so the console can't offer them; its refusal is shown.
  - **The probe stores the toolset** (`POST /v1/toolsets` does both, with every tool off), so step 3 saves its review at once rather than at the end, and "Cancel and remove it" deletes it. Leaving keeps it, off, with Review on its page. A server that answers without sign-in skips step 2 (marked done, and step 3 says so).
  - **"Publish the new versions now"** in step 4, off by default: the console has no builder yet (T077), so a draft would otherwise need the CLI to publish.
  - **OAuth detection** reads the `auth_required` message for "OAuth". A field in the error (or `auth: oauth` in a 422's body) would be sturdier; for T095.
  - **The tests' server is its own** (`e2e/mcp.json` on 18485, the fake MCP server and registry on 18484), not `e2e/admin.json` as T064 asked: the stored agent the tests create can't be deleted once published, and would change the admin tests' agent count.
  - A toolset named `new` can't be opened at `/toolsets/new`; the add form refuses that name.
- Part 3 tests:
  - Unit (`src/pages/mcp.test.tsx`, 54 with the tables): the add form (401 → sign-in, a pasted key never sent, `${VAR}` header), every tool decided with fingerprints and class then stored agents (`if_latest`, `runtime_toolsets` added, draft), hostile text as text, checks before any request, the server's refusals by field, per-user sign-in not available and the OAuth notice, the registry (prefill, not allowed, required headers first, 404 hides, 502 says so), the review page (diffs, the shown fingerprint, a conflict), review needed with Review, Turn off and Remove asks first, the 403 pages, and the tables of `urlProblem`, `isPrivateHost`, `nameFromURL`, `nameProblem`, `headerProblem`, `addError`, `visible`, `toolFlags`, `diffTokens` and `decisions`. `agent-ui` `client.test.ts` adds 3.
  - Browser (`e2e/mcp.spec.ts`, 6, against `agentkit serve` and `e2e/fake-mcp.ts`, a Streamable HTTP server that wants a bearer token, sends `list_changed`, and has hostile text): find it in the registry → Connect → 401 → a pasted key refused in the page → `Bearer ${FAKE_MCP_TOKEN}` → review (flags, ⟦U+200B⟧, markup as text) → approve one, reject the flagged one → a stored agent's published version → a chat calls `fake-mcp__echo` and the server counts the call; the rug pull (review needed in the list and the page, the agent no longer calls it, the diff, approve, the chat calls it again); a private address refused by the server with the reason; a user's 403 pages and 403s from the API; the pages on a phone; turn off, on, remove. The token (a canary) is checked against the HTML, fields, storage, URL, every request and console line.
  - `make web-smoke`: 28 passed (with T064's, T074's and the chat tests).
- Part 3 mutations (each made a unit test fail, then was reverted): a secret header's literal value allowed; `visible` showing nothing; approving without the fingerprint; approving without the class; nothing left to decide; a removed tool approved; IPv4-mapped addresses not unmapped; user info allowed; a tool name matched inside other words; no invisible-character flag; the diff adding before removing; the description rendered with `dangerouslySetInnerHTML`; the bulk approve taking flagged tools; no new read after a conflict; `del` and `ins` swapped; no 403 page on the add page; per-user sign-in offered; no OAuth notice; config agents selectable; a version saved without `if_latest`; a registry URL not allowed made usable; a registry's required headers not asked first; a registry that is down hiding the box; Remove without asking; review needed without its Review link. None survived.
- Part 3 size: the console's first load went from 225,617 to 226,372 bytes (+755: the list head's + and the toolsets reload), under its 240,000 budget. The admin chunk (lazy) grew from 10,492 to 20,846 bytes gzipped; `pkg/web/dist` from 299,432 to 310,518 bytes (+11 KB in every image that embeds the console), under 100 KB, so no docs/SIZES.md row.
- **Part 3 review** (2026-10-22), fixed on the branch:
  - **A big or many-tooled server froze the page.** `toolFlags` built a regular expression per other tool for every tool, twice per render: 1,000 tools took 1.8 s at each click. `diffTokens` allocated one array per token of one side with no cap when the other side was empty (a 1M-token description: 0.75 s per render), and showed a big schema with one changed line whole. Now the flags are worked out once per review (`reviewFlags`, a word set, no regular expression from a server's name), the diffs once per card (`useMemo`), and `diffTokens` keeps the common start and end and tables only the changed middle, in one flat array, up to `maxDiffCells` (1M cells, 4 MB); past it the middle is shown whole.
  - **Step 4 used the highest listed version as `if_latest`.** A deleted draft keeps its number, so after one every save was a `409` reported as "someone saved a version meanwhile". It now sends the list's `latest`. And "Publish the new versions now" published on top of the newest version even when that was someone else's unpublished draft, publishing their changes unseen: on top of a draft it now saves a draft and says so.
  - **A key under an innocent header name** (`X-Api: sk-…`, or `Bearer ${A}sk-…`) was sent: only names were checked. `literalSecret` refuses a value that, outside its `${VAR}`s, is a scheme with a literal or a long token of letters and digits (not a UUID). The server makes the same check since the hardening pass after it (`literalSecret` in `pkg/mcp/servers/check.go`, 400 `invalid_request` on add and update; planted: the check off, which failed `TestLiteralHeaderValues`).
  - **A conflict in step 3** left Save enabled on the old review until the new one arrived, and a failed re-read was an unhandled rejection. Save now waits for the fresh review; if it can't be read, Save stays off and the page says to review it from the toolset's page.
  - **Filling in a registry remote's `{variable}`** reset its transport and headers, as any URL edit did. An edit that still fills the remote's URL (`fillsTemplate`, matched piece by piece) keeps them.
  - Tests added: the draft and deleted-draft case, both conflict cases of step 3, the registry template, adding by URL with no registry, `literalSecret`, `fillsTemplate`, names with `-` and `.`, `reviewFlags` against `toolFlags`, and the diff's trimming and cap. Plants (each failed a test, then reverted): `literalSecret` off; `if_latest` from the newest listed version; publishing over a draft; no common-start trim; no cap; a non-word name matched inside a longer word; no choice reset after a conflict; Save on after a failed re-read; `fillsTemplate` accepting anything; URL edits dropping the registry's hints; the description through `dangerouslySetInnerHTML`.
  - Size after the review: first load 226,421 bytes; admin chunk 21,551 gzipped; `pkg/web/dist` 311,270 bytes (+752).

## For T095 (per-user OAuth)

Done in T095 (2026-10-22): **Scopes** is ticked and the status is `done`. `scope: user` with `auth: oauth` is accepted. Each user's own connection is used, and a user who hasn't connected gets "not connected" as a tool error that never reaches the server (`pkg/mcp/servers` `TestOAuthUserScope`, `TestOAuthTenantScope`; `cmd/agentkit` `TestMCPOAuthE2E`). The console's step 2 enables "Each person signs in" from the 422's `auth` field, and step 3 connects the admin's account first. The two notes below are done.

- The add form's step 2 has "Each person signs in" disabled. Once `scope: user` and `auth: oauth` are accepted, enable it, send them, and the user's Connect row comes from `GET /v1/credentials` like any slot (Connections already connects OAuth slots in a popup).
- Say "the server wants OAuth" in the `422 auth_required` body (a field), so the console needn't read the message.
