---
id: T087
title: "Console UX design, part 2: skills, agent builder, tool policies, interrupts and approvals, tasks with steering"
epic: E11
status: review
depends_on: [T070]
---

# T087: console UX design, part 2

## Context

T070 designed the console's base: the IA (conversation list column, 56 px icon rail with "Activity (later)"), layouts, states, components and tokens (docs/UI.md, mockup v3). The owner added (2026-10-12):
- **skills**, where each skill can pick which tools it auto-loads;
- an **agent builder**: pick skills, pick tools, a defer-loading flag per tool, the interaction mode, the tool policy;
- **tasks**: agent runs that aren't chats (background and ephemeral), with a transcript you can watch and **steer**;
- **interrupts and approvals**: `ask_user` questions, approval holds, and tool policy decisions shown in transcripts.

Same deliverable as T070: a spec in docs/UI.md plus mockup v4. A design task, no app code.

## Inputs (decided)

- [E17](E17-tool-safety-control.md):
  - tool kinds (read, write, delete, send, execute) and risk (low, medium, high);
  - policy actions (allow, flag, approve, deny), presets, and narrowing by environment;
  - interaction modes (interactive, supervised, autonomous; tasks default to autonomous);
  - interrupts, deferred loading, skills that load tools, steering.
- [E15](E15-agent-definitions.md): agents and skills are versioned (draft, publish, rollback, versions list).
- [E14](E14-background-agents-messaging.md) and [E16](E16-runtimes-jobs.md): **Tasks** is the owner's name for every run that isn't a chat (background or ephemeral). They live under the rail's Activity slot, renamed **Tasks**. Their notifications, triggers and agent-to-agent threads are covered too.
- The tool-step style, tokens and components from T070.

## Scope

1. **Skills:**
   - a list (name, description, tools it loads, used by N agents, version);
   - a detail and editor (`SKILL.md` with preview, files, the tools picker with globs, version history, publish);
   - upload a skill folder.
2. **Agent builder** (desktop-first, a builder beside a test chat):
   - identity (name, description), model and effort, system prompt editor;
   - **skills picker**;
   - **tools picker** by toolset with per-tool **defer loading** toggles, kind and risk badges, and search;
   - **interaction mode**;
   - **tool policy** (preset plus rules, with a preview: "in this run these 12 tools are allowed, 3 need approval, 2 are denied");
   - memory, limits;
   - draft, publish and version history with diff and rollback;
   - validation errors per field.
3. **Tasks:**
   - a list (running, waiting for you, done, failed; agent, started by or trigger, elapsed, cost, environment);
   - a **task view**: the transcript, live, with the same steps as chat; a **steer** composer ("Message the task", delivered at the next step); pause, resume and cancel; outputs and artifacts; the version of the agent used.
3a. **Starting a task** from the console: agent, input, environment and policy narrowing.
4. **Interrupts and approvals:**
   - in chat (inline question or approval card in the transcript);
   - in tasks ("waiting for you" state, notification, answer from the task view or a notification);
   - approval cards show the tool, its kind and risk, the exact input, approve or deny with a reason.
5. **Policy in transcripts:** how flagged, held, denied and approved calls look in the step list (quiet, but unmistakable), and how deferred-tool loading shows ("Loaded 3 tools").
5a. **Adding an MCP server** (E19, T096: the "Add connector" screen T070 asked for):
   - an admin pastes a URL; the console probes it, then shows auth (none, header from a credential, or "users sign in" with OAuth);
   - **review tools**: each tool's raw description, its kind and risk, enable or disable;
   - save, then assign to agents;
   - the "review needed" state when a server changes a tool (a diff of the description and schema, approve or keep disabled);
   - each user's **Connect** card on Connections for servers that need their own sign-in;
   - later, a registry search box (T097).
6. **Updates to T070's IA:** the rail gains Skills (admin) and Tasks (everyone, with a "waiting for you" badge); say what moves.

## References

- T070's docs/UI.md, mockup v3 and Notes (base; keep its decisions unless a new need breaks one, and say which)
- Agent builders: OpenAI GPT builder and Agent Builder, Claude Projects settings, Dify and Flowise agent configuration, LangSmith/LangGraph Studio (versions, test beside edit)
- Task and run views: Claude Code background tasks, Devin/Jules session views (watch and steer), GitHub Actions run logs, Temporal's workflow view
- Approvals and permissions: Claude Code permission prompts (allow once / always / deny), GitHub environment protection approvals, AWS IAM policy simulator (the "what would be allowed" preview)
- Adding MCP servers: [Claude custom connectors](https://claude.com/docs/connectors/custom/remote-mcp) (URL → Add → Connect; org owner adds, members connect), ChatGPT developer-mode connectors, Cursor's MCP settings
- Version history: Vercel deployments (promote, rollback), Notion and Google Docs version history (diff and restore)

## Acceptance criteria

- [x] **Coverage:** every scope item has layouts at T070's widths (the builder may be desktop-only, with a stated phone fallback), states, and components (mapped to shadcn/Radix or marked custom, with `ui` / `agent-ui` / `console` placement).
- [x] **Mockup v4:** clickable; it extends v3 and doesn't fork it; both themes; sample data covering every policy action, every interaction mode and a waiting task.
- [x] **docs/UI.md** updated, with decisions and reasons in this file's Notes.
- [ ] **Owner review:** the owner approves mockup v4.

## Notes

**Status: review.** The first three criteria are met. The fourth, the owner's approval of mockup v4, is the owner's to give, so the task waits in review for it.

### Files

- **`docs/UI.md`**:
  - the header (part 2 and mockup v4);
  - the IA: the rail gains Tasks and Skills; new decisions on one home for tasks, on what nags, and on full-width pages;
  - Routes: the part-2 routes, and the unsaved-changes rule;
  - Screens: the Agents row is updated; Tasks, Thread, Triggers, Agent builder, Versions, Skills and Add an MCP server replace the Activity row;
  - new sections: "Interrupts and approvals", "Tool policy in transcripts", "Tasks", "Agent builder", "Versions", "Skills", "Adding an MCP server";
  - 17 new States rows, and `suspended` in the run errors;
  - Components: the tool step's new states, and 20 new rows (the "T087 adds…" line is gone);
  - Keyboard: `Mod+S`; Esc closes the test panel; Enter sends in a task's box;
  - Accessibility: interrupts, policy words, forms, diffs and trees, tasks.
- **`docs/ui/console-mockup.html`**: mockup v4, the same file extended, not forked.
  - The State menu has 65 options: v3's 36 plus 29 in five new groups (Interrupts and policy, Tasks, Agent builder, Skills, MCP servers).
  - v3's Activity code is replaced by Tasks. Everything else is v3's, except what "What changed from v3" lists.
  - The sample data covers:
    - every interaction mode: researcher is interactive, ops-helper supervised, digest-bot autonomous;
    - every policy action: allowed, flagged (ops-helper's web tools, digest-bot's `mail_send`), held for approval (`github_create_issue`), denied (`ops_restart_toolset`), approved and denied by a person;
    - loaded deferred tools;
    - a waiting task with two open calls, a running task you can steer, and a paused task with a queued message;
    - done, failed and canceled tasks, three threads (one stopped at its hop limit) and three triggers;
    - four versions of researcher, four skills (one from the config, one with a warning), and a runtime MCP server that needs a review.
  - No new external scripts: Google Fonts only, as v3.
- **Not changed:** code, tests, `tasks/STATUS.md`, the README, other task files.

### Decisions

1. **Tasks replace Activity, for everyone, and are their only home.** Any run that isn't a chat (`POST /v1/tasks`, a trigger, another agent, an API key) is listed in Tasks and not in Chats. This changes the last line of T070's decision 8, which showed a background run's conversation in Chats too. The reasons:
   - a task has no composer that starts a new turn; its box steers a running task;
   - mixed into Chats, tasks from triggers would push people's own conversations down every morning;
   - "Tasks" is the owner's word.
2. **Only "waiting for you" nags.** The Tasks badge counts tasks waiting for an answer, like an inbox's count of what needs action (GitHub, Linear). A finished task you haven't opened gets a dot on its row, as a finished chat does in v3. In Chats, a waiting conversation gets an amber mark and "Waiting for you" on its row.
3. **Interrupt cards sit inline, one per waiting call, and the composer is blocked while they wait.**
   - A suspended run keeps its conversation busy (`409 conversation_busy`, T084), so a typed message would fail; the composer says so ("Answer above first, or cancel the run.") and offers Go to it and Cancel run.
   - Answering where the call happened keeps the context: the step above, and the input on the card.
   - Several waiting calls get several cards and "1 of 2 answered", because the API takes the answers one at a time.
4. **No "Always allow" on an approval card.**
   - In Claude Code, "always" writes the user's own local permission. Here a policy is server config for an agent that others use too, so a one-click widening from a chat would be a privilege change made by whoever is in the chat.
   - The card says how instead: "To allow it every time, change ops-helper's policy." In the builder, the tool's policy select writes that rule (decision 8). Open question 1 asks the owner.
5. **A notification never takes the answer.** The toast's Review opens the task at its first open card. An approval needs the exact input in view, and a toast is small and lasts 8 s. This is GitHub's environment approvals pattern: the notification links to the run, where you approve.
6. **Policy shows as a word in the step row**, never as a colour or icon alone (UI.md "Tool policy in transcripts").
   - "Not allowed" is grey, not red: the policy worked as configured, so it isn't a failure (the same reasoning as v3's amber "needs your connection").
   - "Waiting for approval" is amber, because it needs you.
   - "Flagged" is a tag on a call that ran.
   - Waiting and not-allowed calls are always shown in a collapsed range, as failures are.
   - The policy block in the details says what the model was told.
7. **The builder is a full-width page** (T070 had moved it here).
   - Desktop: section nav · form · the test chat beside it, as in the GPT builder and LangGraph Studio.
   - Laptop (856 px of content at most): the test chat is an overlay you open with Test.
   - Phone: read-only, because a 40-field form with a policy editor isn't a phone task. This is the "stated phone fallback" the coverage criterion allows.
8. **The tools picker picks toolsets.** Each tool has a defer switch, and a policy select that writes an exact-name rule at the top of the rules.
   - An agent's definition lists toolsets, not single tools, so excluding one tool would need an API change (asked). Denying it with an exact rule does the same today.
   - An exact-name rule is also the only rule that waives `requires_approval` (T083), so the select is where that waiver lives.
   - A search box filters the tools across all toolsets, used or not.
9. **Policy: start from a preset, with the preset's rules written out under the custom rules.**
   - First match wins, so the order on screen is the order of evaluation, ending in "Anything else: Denied".
   - **Try it** is AWS IAM's policy simulator: pick where it runs and an environment, and see every tool's decision and the rule that made it. The console uses the server's rules, a copy of pkg/policy's logic, which can drift; an evaluation endpoint is asked.
   - The policy can't extend a preset yet (T083's open point), so for now saving writes the preset's rules into the agent's own list. The screen looks the same either way.
10. **The test chat runs the published version**, and says so while there are unsaved or unpublished changes. The API can't run a draft; it's asked. The alternative, a separate test agent, would leave stray agents behind.
11. **The model is a text field with suggestions.** There is no models API, and a select would go stale.
12. **Versions diff a readable rendering of the definition, not JSON.** The instructions are split sentence by sentence, so a one-word change doesn't mark the whole prompt. As in Vercel:
    - **Roll back** publishes an older version again.
    - **Delete** is offered only for an unpublished draft (`409 version_published` otherwise), because runs refer to published versions.
    - **Edit from v2** starts a new version from an old one.
13. **A save conflict never merges by itself.** `409 conflict` (the `if_latest` check) shows who saved what (the diff), then lets you save yours on top or load theirs. This is the Google Docs and Notion rule: nothing is overwritten silently.
14. **Agents from the config file are read-only, with Duplicate.** They change only with a restart, and `409 read_only` says the API agrees. Duplicating gives a stored, versioned copy.
15. **Skills show what each tool glob loads, per agent.**
    - A skill can load only deferred tools the agent already has, so the same glob loads different tools for different agents. The detail and the editor show the matches live, and a glob that matches nothing is a warning, not an error.
    - Scripts in a skill are files to read: "agentkit never runs them."
16. **Adding an MCP server takes four steps, and every tool needs a decision.**
    - The URL is checked in the console the way the server's SSRF guard checks it (https only, no private addresses), so the reason shows before a request. The server still decides.
    - Header values are references like `${LINEAR_API_KEY}`, never secrets, because the value must never pass through the console (`runtime_toolsets.header_env`).
    - Descriptions are shown exactly as the model sees them, invisible characters made visible. Instructions to call other tools are flagged; these are the two prompt-injection tells in tool descriptions.
    - A server's `readOnlyHint` is shown as "a claim, not a check". The kind and risk are the admin's call, and unclassified means a medium-risk write (T083).
    - "Approve the 3 without flags" keeps the common case quick without approving anything flagged.
17. **Steering:** the box under a live task sends at once. Its words say when the agent reads the message: at the next step, or queued while paused or waiting. A finished task has no box. Enter sends there, unlike a running chat's composer, because a steering message doesn't stop anything.
18. **Starting a task** is autonomous only (T071 serves no other mode yet). The task can narrow its policy but never widen it, and the dialog shows a live count of what its tools will do. The count reminds you that an autonomous task turns "needs approval" into "denied".
19. **Spelling:** "Canceled", as the API spells the status. v3 had "Cancelled".
20. **No new tokens.** The risk dot and the diff use the status colours, always with a word or sign. `Mod+S` saves a draft in the builder and the skill editor, where the browser's Save page is never what you want.

### For T077 (agent builder, skill editor)

- The builder layout, sections, tools picker, policy editor with Try it, errors summary, conflict, read-only and Duplicate, test panel and phone fallback are in UI.md "Agent builder". Versions and the diff are in "Versions". Skills, the editor and upload are in "Skills".
- Compute **Try it** from the API once it has an evaluation endpoint. Until then, port pkg/policy's rules and test them against `TestPresets`.
- Leaving with unsaved changes asks first, in the app and through `beforeunload`.

### For T074 (Tasks in the console)

- The list, task view, steering box, pause, resume, cancel, Run again, Start a task, notifications, threads and triggers are in UI.md "Tasks".
- The task view reuses the chat's step rows and interrupt cards (`agent-ui`). Keep one implementation.
- Steering, pause and resume wait for T086. Hide them until the server says it has them (the run's actions or a capability), rather than showing buttons that 404.

### For T055 (chat)

- The interrupt cards, the waiting composer, Cancel run and the new step states are `agent-ui` pieces shared with Tasks (UI.md "Interrupts and approvals", "Tool policy in transcripts").
- A reload must show the same rows, which needs the history to carry the policy decision and the loaded tools (see "Found out of scope").

### For T096 part 3 (MCP servers in the console)

- The four steps, the review page and Review needed are in UI.md "Adding an MCP server".
- Per-user sign-in is a Connections row ("Account, MCP server"), drawn in the mockup's Connections state as later (T095).

### Found out of scope (asks, not changed)

- **T071, supervised tasks:** `POST /v1/tasks` serves only `interaction: autonomous`. The design shows Supervised as later. It also needs a rule for who may answer a task's interrupt (open question 2).
- **T086:** the design follows T086's spec: `POST /v1/tasks/{id}/messages`, `/pause` and `/resume`, the `steered` event, the `paused` status, and a refusal after the end.
- **T075:**
  - run an unpublished version, for the test chat;
  - `GET /v1/agents` and `/v1/agents/{name}` to return the interaction mode, the policy, `defer` and the version, which the agent detail shows;
  - include or exclude single tools of a toolset.
- **T083:**
  - a policy that **extends** a preset (its open point);
  - an evaluation endpoint, for example `POST /v1/policy/evaluate {policy, mode, env, tools}`, so the builder's preview can't drift from the server.
- **T076:** the skills API. Stored skills with versions, upload with checks, front-matter validation, and `GET /v1/skills` listing each skill's tool globs. Stored agents can't hold skills or memory settings until then.
- **T062, T083, T085, the conversation history:** `GET /v1/conversations/{id}` should keep each step's policy decision and the turn's `loaded` tools, so a reload draws the same rows as the stream.
- **T071 and E16:**
  - choose a pool on `POST /v1/tasks`; the dialog's Environment has only the default;
  - list a run's output files on `GET /v1/runs/{id}`; the Result block has a place for them.
- **T073:** `GET /v1/threads` should return the hop limit and the token budget, which the mockup shows as "of 8" and "of 500,000".
- **T062:** a models list, so the model field can suggest what the server accepts.
- **E17:** a missing connection as an interrupt kind, so a run could wait for you to connect (open question 6).
- **T095 and T044:** per-user OAuth for MCP servers, the third sign-in choice.

### References: what we took and what we left

| Reference | Took | Left |
|---|---|---|
| OpenAI GPT builder, Agent Builder | A form with a live preview beside it; sections for instructions, knowledge and tools | "Create" (configuring by chatting); the node canvas |
| Claude Projects settings | Instructions as one large text field | Project knowledge (not an agent setting here) |
| Dify, Flowise | Tools grouped by provider, with a toggle each | Flow canvases |
| LangSmith, LangGraph Studio | Versions with a diff; testing beside editing | The graph view |
| Claude Code background tasks | A list of tasks with status; the transcript one click away | Terminal rendering |
| Devin, Jules | Watch a run live and message it; messages arrive between steps; pause | IDE and browser panes |
| GitHub Actions run logs | The run header (trigger, who, time); collapsed steps; Re-run | The job graph |
| Temporal's workflow view | Status words taken from the API; the history is the record | The raw event table |
| Claude Code permission prompts | One decision per call, with the exact command shown | "Always allow" (decision 4) |
| GitHub environment approvals | Approve or reject with a comment; who approved is recorded; the notification links to the run | Required-reviewer settings |
| AWS IAM policy simulator | Pick a context and see every action's decision and the statement that decided it (Try it) | The JSON editor |
| Claude custom connectors | URL, then add, then connect; admins add, members connect | — |
| ChatGPT developer-mode connectors | Reviewing each tool's description before use; warnings for unverified servers | Write actions on by default |
| Cursor's MCP settings | A toggle per server tool | Editing a JSON file |
| Vercel deployments | Promote and roll back by publishing an earlier version | Preview URLs |
| Notion, Google Docs version history | Who and when per version; diff; restore | Per-keystroke history |

### Contrast

Part 2 adds no colour tokens, so T070's tables still hold. The new pairs come from them:

- Amber step words and the Flagged tag: `--warn` on `--surface` is 5.57 (light) and 12.02 (dark).
- Diff signs on `--surface-2`: `--ok` is 5.70 and 9.97; `--bad` is 6.20 and 6.28.
- Risk dots are non-text (3:1 needed) and always sit next to the word: `--muted`, `--warn` and `--bad` on `--surface` are all above 4.5.
- The interrupt card's amber left edge is decorative: the card's title says what it is.

### What changed from v3

- **Rail:** Activity became **Tasks**, for everyone, with a badge for waiting tasks. **Skills** joined the admin group.
- **Chats:** background runs' conversations are no longer listed (decision 1). A waiting conversation has a mark and "Waiting for you"; the composer has the waiting notice.
- **Tool steps:** the policy and loaded-tools states, and the group summary counts them.
- **Agents detail:** version, interaction, tool policy with counts, deferred tools; Edit and Versions, or Duplicate.
- **Toolsets:** an add button, runtime servers, and the Review needed state.
- **Connections:** the account popup's text follows the provider. An MCP server row appears in the "sign in to an MCP server" state, marked later.
- **Shortcuts:** `Mod+S`.
- **Words:** "Cancelled" is now "Canceled".
- **Show later features** now covers org connections, Memories and per-user MCP sign-in. Part 2 is always shown, because it is what is being reviewed.

### Open questions for the owner

1. **"Always allow" from an approval card** is left out (decision 4). Should admins get "Allow every time for ops-helper", which writes an exact-name rule as a new draft of the agent?
2. **Who may answer a task's interrupt:** the person who started it, the owner of the trigger that started it, or any admin? The mockup lets whoever opens the task answer.
3. **Browser notifications** (the Notification API) for waiting tasks, or only the in-app toast, mark and badge?
4. **The builder on phones** is read-only. Is that enough?
5. **Tasks for users:** users see the tasks they started and those of triggers they own; admins see the tenant's. Is that right? (T070's question 6, carried over.)
6. **Waiting for a connection:** should a missing connection become an interrupt, so the run waits until you connect, instead of going on without the tool?
7. **Skills on stored agents** need T076. Until then, should the builder show the Skills section disabled, or hide it?

### Checks run

- **Script:** `node --check` on the mockup's script.
- **Every state, every button:** in a scratch folder outside the repo, jsdom 24 loaded the mockup at 1300, 1000 and 400 px. Each of the 65 State options was opened, then each button and link in the view was clicked, and so was each button of any dialog or menu that opened. There were no script errors at any width. A probe confirmed the harness catches a thrown handler error.
- **Flows driven with events:**
  - approve in a chat, then the answer;
  - a waiting task: approve, pick a choice, running, done;
  - steer a running task, pause, resume;
  - the builder: add a rule, save a draft (v5), publish;
  - the server's field errors (summary and 2 field errors);
  - the conflict: the diff dialog, then "Save mine as v6";
  - a config agent: read-only, then Duplicate;
  - versions and the diff;
  - the skill editor: add a glob;
  - an upload with a failed check: Upload disabled;
  - MCP: http refused, then the four steps through to the new toolset;
  - Review needed;
  - the Connections MCP row.
- **Repository:** `make lint` exits 0 (golangci-lint 0 issues; lint-skills passes on the task file and links). `make check` exits 0; one earlier run failed `TestCredentialsFile` in `cmd/agentkit` on a held lock file (a package this change does not touch), which passed when rerun alone and in the next full run.
