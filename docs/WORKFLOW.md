# agentkit: How We Work

> How the project is built by one planner and several parallel coding sessions (agents or people), and what we learned doing it. [PLAN.md](PLAN.md) says what we build, [ENGINEERING.md](ENGINEERING.md) how the code is designed and tested, and this file how the work is organized. The current state of the plan is in [tasks/STATUS.md](../tasks/STATUS.md).

## 1. Roles

- **Planner.** One session owns the plan: `docs/PLAN.md`, `docs/ENGINEERING.md`, the epic files, the status board, and the decisions recorded in task files. It reads every handoff, decides open questions, updates the plan, writes the next handoff prompts, and starts sessions. It does not implement tasks.
- **Task sessions.** One session per task (or a small group of tightly related tasks). A task session implements, tests, updates its own task file, pushes to `main`, and reports back with a handoff.
- **Owner.** The person who decides what the planner cannot: priorities, where to deploy, secrets and accounts, deleting shared things (branches, data).

## 2. Rules every session follows

1. **Work goes straight to `main`.** No long-lived branches. `git pull --rebase origin main` right before every push.
2. **Touch only the files your task owns.** The handoff prompt lists them. Shared files (`go.mod`, `go.sum`, `internal/archtest/rules.go`) conflict often: on a rebase conflict keep both sides, then `go mod tidy` and `make check`. README and the status board are updated by the planner.
3. **Gate every push on `make check`'s exit status.** Never pipe it through `grep` or `tail` in a way that hides a failure.
4. **CI must be green on the pushed commit.** Check it (GitHub Actions); a red `main` is fixed before anything else, by whoever broke it or whoever is next.
5. **A criterion is ticked only if it actually ran.** Live tests without a key, manual checks without a client, Docker checks without Docker: leave the box unticked, set `status: review`, and say why. Never fake a result.
6. **One test per acceptance criterion, invariants after every test, and a mutation self-check** ([ENGINEERING.md §2](ENGINEERING.md#2-testing), the `write-tests` skill). The mutation list goes into the task's Notes: each planted bug must make a test *fail*; a compile error does not count.
7. **Record decisions where the next person looks:** the task file's Notes, with a "For T0xx" section for whoever builds on it.
8. **Out-of-scope findings are reported, not fixed.** A session that finds a problem outside its files puts it in its handoff; the planner schedules it.

## 3. Handoffs

### Planner → task session (the handoff prompt)

A prompt must work for a session that knows nothing about this conversation. Template: [tasks/_handoff_template.md](../tasks/_handoff_template.md). Every prompt has:

- **Repo and push rules** (main, rebase, owned files, `make check` exit status).
- **Why**: what the task unblocks, in two or three lines.
- **Read first, in order**: the task file, the decisions it depends on, the `go doc` of the packages it uses, ENGINEERING §1–§2, relevant skills (`write-tests`, `claude-api`).
- **Build**: the design, including the traps we already know about.
- **Tests**: what each criterion needs, invariants, replay e2e, live.
- **Definition of done**: criteria ticked, `make check`, race runs, fuzz, mutation list with specific bugs to plant, fresh clone, task file and epic updated, CI green.
- **Report when finished**: what the planner needs back.

Prompts are started as suggested-task cards or pasted into a new session. When the plan changes while a session runs, send the change to that session directly (and commit it), rather than waiting for it to rebase.

### Task session → planner (the handoff report)

Ends every task. It contains: state (commit, CI run, task status), what was decided and why, what the next tasks must know ("For T0xx"), open questions that need a planner decision, and follow-ups found outside the task's scope. The planner turns it into plan changes and acknowledges each item.

## 4. Lessons learned

Each of these happened. The rule it led to is in bold.

### Testing and tooling

- **A test that passes no matter what is worse than none.** Planting bugs in `pkg/tool` found `TestDefinitionIsACopy` comparing two values that shared memory, and `TestTimeout` hanging instead of failing. **Every task plants its own bugs and lists them.**
- **A fresh clone catches what your machine hides.** The linter, installed by the Makefile, was built with the linter's own older Go and refused to run on our code; it worked locally only because the toolchain had been forced by hand. **Run `make check` in a fresh clone before calling a task done.**
- **Global test flags break `go test ./...`.** A `-update` flag defined in one helper package made every other package fail with "flag provided but not defined". **Golden files are rewritten with `make golden` (an env var), not a flag.**
- **Fuzzing finds real stream bugs.** `FuzzStream` in the Anthropic adapter turned `main` red with a block start that had no block. **Fuzz anything that parses untrusted input, and add each reproducer to the corpus.**
- **Concurrency tests must not depend on scheduling.** The `agentkit serve` shutdown test failed 1 run in 10 because an HTTP/1.1 request could still be queued when the listener closed. It was fixed by holding the request inside the handler (`Expect: 100-continue`) before sending SIGTERM. **Drive concurrency from the test's side; a wait may only make a broken implementation pass, never fail a correct one.**
- **One test per criterion misses the paths between criteria.** Review of the harness found bugs in combinations no criterion named. **Code with many paths gets invariants checked after every test and a seeded simulation (§2.6).**

### Design

- **Encoders change bytes.** `encoding/json` compacts and HTML-escapes `json.RawMessage`, so Claude's thinking blocks would not round-trip byte for byte. `llm.Opaque` stores its data as a JSON string. **Anything that must come back unchanged is tested byte for byte.**
- **Stack traces leak.** A panic's stack ended up in an error message that reaches models and MCP clients. **Error messages carry the cause only; stacks go to logs (`*tool.PanicError`).**
- **Mounting under a prefix breaks routing.** The MCP server must be mounted without `http.StripPrefix`. **The wiring template (`examples/mcp-gateway`) is copied, not reinvented.**
- **Security features have deployment consequences.** The MCP SDK's DNS-rebinding protection answers 403 behind a same-host reverse proxy. **Recorded, and decided per deployment, not hidden.**
- **Checkpoints outlive code.** Once checkpoints are stored, a deploy can resume a half-finished run with a different step tree. **`flow` fingerprints the tree and refuses (`ErrShapeChanged`); completed runs are never resumed.**
- **Saving state is not the same as resuming.** We had checkpoints and saved transcripts, but nothing found interrupted runs, retried transient failures, or let a person approve. **Every way a run can stop has a named way to continue (E05 decisions).**
- **Conversations have shape rules.** The harness requires user and assistant messages to alternate; a cancelled run can leave a dangling user message. **One unfinished run per conversation; cancel appends a marker, never edits.**
- **Frameworks were evaluated, not adopted.** ADK Go has no Claude support and Gemini-typed models; Genkit pulls 133 dependencies; Eino is pre-1.0. **Our own harness and flow; frameworks integrate through MCP.**

### Process

- **Parallel sessions on one `main` work** if each owns its files and rebases right before pushing. Conflicts were limited to `go.mod` and the archtest allowlist.
- **Plans go stale fast.** Handoffs arrived describing a state already overtaken (a task finished in between). **The planner checks `main` before acting on any handoff.**
- **Scope rules can block obvious fixes.** A session could not correct a stale line outside its files. **That is intended; it reports the line and the planner fixes it.**
- **Review branches linger.** A throwaway base branch for a review PR stayed after the work was on `main`. **Delete review branches when the PR is merged (with the owner's yes).**
- **A finished task is not a finished session.** The planner archived the T038 session after its commit was on `main` with CI green, while the session was still running a self-review. **Re-read a session's latest status right before archiving it, and archive only an idle one.**
- **Never discard a gate's output.** One local `make check` failed with its output sent to `/dev/null`; eight later runs passed, so the failing test is unknown. **Keep the log of every gate run (`make check > log 2>&1`) until it passes.**
- **Credentials differ by environment.** The key is in `ANTHROPIC_KEY` in Claude sessions, not `ANTHROPIC_API_KEY`, and the owner keeps it out of GitHub. **`make test-live` maps it; code never reads `ANTHROPIC_KEY`. Live tests run in sessions, never in CI.**

## 5. Where things are

| What | Where |
|---|---|
| Scope, decisions, build order | [docs/PLAN.md](PLAN.md) |
| Module design, tests, environments, skills | [docs/ENGINEERING.md](ENGINEERING.md) |
| How we work (this file) | docs/WORKFLOW.md |
| Plan status: done, running, next, waiting | [tasks/STATUS.md](../tasks/STATUS.md) |
| One file per task, grouped by epics | [tasks/](../tasks/) |
| Handoff prompt template | [tasks/_handoff_template.md](../tasks/_handoff_template.md) |
| Agent skills | `.agents/skills/` (Claude Code reads them through `.claude/skills`) |
