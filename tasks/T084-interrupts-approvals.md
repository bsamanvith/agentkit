---
id: T084
title: "Interrupts and approvals: interaction modes, ask_user, approval holds, suspend and answer"
epic: E17
status: done
depends_on: [T083, T030]
---

# T084: interrupts and approvals

## Context

Some tools need a person: `ask_user` asks a question, and tools needing approval wait for a yes. Chats can do that inline. Tasks may wait (supervised) or must never wait (autonomous). Decisions: [E17](E17-tool-safety-control.md) ("Interaction mode", "Interrupt mechanism").

## Scope

- **Harness:**
  - a tool may return an `Interrupt{Kind: question|approval, Prompt, Choices?, Pending call}`;
  - the turn ends with the call pending, and an `interrupt` event is emitted;
  - the transcript stays valid until the answer arrives.
- **Runner:**
  - suspends the run (E05 suspended status) with the pending interrupt;
  - `POST /v1/runs/{id}/input` (and later `/v1/tasks/{id}/input`) takes the answer: text or a choice for questions, `approve`/`deny` plus a reason for approvals. The answer becomes the tool result, and the run resumes;
  - answers are idempotent (a second answer to the same interrupt is `409`).
- **`ask_user` built-in tool** (`interrupts: true`): question, optional choices.
- **Policy `approve`** (T083) creates an approval interrupt for the pending call. Approved runs the call with the original input; denied returns "denied by <who>: <reason>".
- **Interaction mode** per agent (`interactive`, `supervised`, `autonomous`), narrowed per run:
  - `autonomous` doesn't offer interrupting tools, and turns `approve` into `deny`;
  - `supervised` suspends and notifies (E14 notifications).
- `agentclient` and the OpenAPI spec cover `interrupt` events and `input`.

## References

- [LangGraph human-in-the-loop (`interrupt`, `Command(resume=...)`)](https://langchain-ai.github.io/langgraph/concepts/human_in_the_loop/) for the interrupt and resume model
- [A2A spec: `input-required` task state](https://a2a-protocol.org/latest/specification/) (so interrupts map onto A2A later)
- [AG-UI interrupts](https://docs.ag-ui.com/) and Claude Code's permission prompts, for how approvals are shown
- E05's suspended status and `flow.Approve` (same suspend and resume path)

## Acceptance criteria

- [x] **Question, interactive:** `ask_user` suspends; the answer resumes; the transcript is valid throughout and after a process restart while suspended (replay e2e).
  - The restart while suspended: `pkg/runner` `TestQuestionSuspendsAcrossARestart` (another process, over the same store, takes the answer and resumes the run; ticked 2026-10-24). Before it:
    - harness: `pkg/harness.TestQuestionSuspendsAndTheAnswerResumes`, and `checkSuspended` in every harness test;
    - the `ask_user` tool: `pkg/coretools.TestAskUserInterrupts`;
    - restart: `pkg/runner.TestQuestionSuspendsAcrossARestart` (the answer goes to a new process, which reads the pending turn from the log); invariants 2 and 7 check the transcript after every test;
    - over HTTP: `pkg/agentapi.TestInputAnswersInterrupts`.
  - The replay e2e needs a cassette recorded from the API, and no key was available here: **unticked**.
- [x] **Approval:** approved runs the call with exactly the original input; denied returns the reason. A second answer gives `409`.
  - Harness: `pkg/harness.TestApprovalRunsTheOriginalInputOrDenies`. The tool sees the reply's input byte for byte, whitespace included, even after the pending turn crossed JSON.
  - Runner: `pkg/runner.TestApprovalRunsTheOriginalInputOrDenies` (the write runs once, or never) and `TestAnswersComeOneByOne` (`ErrAnswered`, the same answer is a no-op).
  - API: `pkg/agentapi.TestInputAnswersInterrupts` (409 `not_pending` for a different answer, 409 `run_final` after the end) and `TestInputDenies` ("denied by a person: not now").
  - Client: `pkg/agentclient.TestAnswer`. CLI: `cmd/agentkit.TestChatAnswersApprovals`.
- [x] **Autonomous:** `ask_user` isn't offered (the fake model records the tools); an `approve` rule denies with a clear reason.
  - `pkg/harness.TestAutonomousNeverWaits`;
  - `pkg/runner.TestAutonomousRunsNeverWait` (the mode survives a crash and resume);
  - `pkg/agentapi.TestRunRequestInteraction` (`"interaction": "autonomous"` in `POST /v1/runs`).
- [x] **Supervised:** the task shows `waiting for input`, and a notification event is emitted. Done 2026-10-24: `POST /v1/tasks` takes `"interaction": "supervised"` (narrowing an agent that may wait); the task is `suspended` (the console's "Waiting for you"), its user's `/v1/events` gets `run_status` `suspended`, and `/input` lets it go on in the background. `pkg/agentapi` `TestSupervisedTaskWaitsForInput`.
  - (Before 2026-10-24: tasks (T071) and notifications (E14) did not exist yet.)
  - A supervised run suspends exactly like an interactive one (the harness's `waits()`).
  - The CLI prints "waiting for input" for a suspended run, but there is no task and no notification: **unticked**.
- [x] **Simulation:** a seeded simulation with interrupts, answers, crashes and duplicate answers keeps the runner invariants.
  - The harness:
    - `pkg/harness.TestSimulatedRuns` / `FuzzSimulatedRun` add a tool that asks and a random input mode;
    - suspended seeds are resumed with seeded answers, sometimes after a doubled answer, which must be `ErrInvalidInput`;
    - at least 10 resumed seeds are required.
  - The runner: `pkg/runner.TestInterruptSimulation` / `FuzzInterruptSimulation`, 200 seeds (60 with `-short`). Each seed has:
    - interactive and autonomous runs of ask / write / echo turns;
    - processes dying at random store calls while running, suspending, answering or recovering;
    - answers one at a time or all at once, the same answer twice, a different answer to an answered interrupt, and cancels while suspended.
  - The runner simulation checks:
    - the world's invariants, including the new invariant 7;
    - writes run only once approved, and at least once when their run completed;
    - an autonomous run never suspends, and is never offered `ask`;
    - a required mix of cases.
- [x] **Mutation list** recorded (below).

## Notes

### The API

`pkg/tool`:

```go
func Interrupt(prompt string, choices ...string) error // a tool asks a person; *InterruptError
```

`pkg/harness`:

```go
type Interaction string // Interactive, Supervised, Autonomous; Interactions(), Valid(), NarrowInteraction(a, b)
func WithInteraction(m Interaction) Option // default Autonomous
type Interrupt struct{ ID string; Call int; Name string; Kind InterruptKind; Prompt string; Choices []string; Decision *policy.Decision; Input string }
type Answer struct{ ID, Text string; Approved bool; Reason, By string }
type Pending struct{ Turn TurnRecord; Done llm.Message; Interrupts []Interrupt } // Reply(), Clone(), Interrupt(id)
// Input gains Interaction, Pending, Answers; Result gains Pending.
// EventInterrupt (a call waits: Event.Interrupt), EventSuspended (the run ends; Result.Pending).
// ErrInvalidAnswer (wraps ErrInvalidInput); ErrSuspended / *SuspendedError from Collect.
```

`pkg/runner`:

```go
func Interaction(m harness.Interaction) RunOption // stored with the prompt, kept on resume
func (r *Runner) Interrupts(ctx, runID) ([]harness.Interrupt, []harness.Answer, error)
func (r *Runner) Answer(ctx, runID, answers ...harness.Answer) (*Execution, error)
var ErrAnswered // wraps ErrNotPending
func PendingTurn(e store.Entry) (harness.Pending, []harness.Answer, error)
```

The store gains `KindInterrupt`. Its entries count no messages and are not context. On the wire:

- `POST /v1/runs/{id}/input` with `wire.InputRequest{Answers []wire.Answer}`;
- the event type `interrupt`, carrying `wire.Interrupt`;
- `Run.Interrupts`;
- `RunRequest.Interaction`.

`agentapi.Runs` gains `Answer`, and `StartOptions` gains `Interaction`. `agentclient.Client.Answer` posts the input. `runnerrpc` adds `runner/RunWithInterrupts`. The `coretools.User()` factory (type `user`) gives `ask_user`. The serve config gains `agents[].interaction`.

### Decisions

- **The pending turn lives in the conversation's log, not the transcript.**
  - When a run suspends, the runner appends an interrupt entry: the reply as its one message, and an `InterruptRecord` with the turn record, the finished calls' results and the interrupts.
  - Each new answer appends the entry again, whole, with the answers so far: the latest one counts. The log stays append-only, and the answers are durable before anything runs.
  - The turn that finishes the pending turn repeats the reply with all the results. So the context, which skips interrupt entries, is a valid transcript at every point.
  - I picked this over a separate command log (no new store interface) and over storing the reply in the transcript (which would leave it ending on unanswered tool calls).
- **Turn 0.**
  - A resume finishes the pending turn as turn 0 (events and `TurnRecord.Turn`). It runs the interrupted calls only: the finished ones keep their results.
  - Its model call was made, and is counted, by the run that suspended. The resumed run's `Result.Turns` and `Usage` leave it out.
  - The API numbers turn 0 as the run's next turn.
- **An approved call runs with `Interrupt.Input`.** These are the reply's input bytes, kept as a string, because a `ToolUse`'s input is compacted when it is stored. The pending turn's check makes sure they are the same JSON as the call's.
- **Idempotency.**
  - The same answer to the same interrupt is a no-op.
  - A different one is `ErrAnswered`, which is `ErrNotPending`.
  - An answer to a run that resumed already is `ErrNotPending`; to a final run, `store.ErrFinal`.
  - The API maps all of these to 409: `not_pending` or `run_final`.
  - A crash after the last answer was stored, but before the run moved to running, is fixed by sending the same answer again: it finds every answer stored and resumes.
- **Suspended chat runs are not recovered** (the recovery loop skips suspended runs, as for flows).
  - A crash while suspending, before the interrupt entry is stored, leaves the run running. Recovery then runs the turn again.
  - A crash after the entry is stored resuspends the run from the entry, without a model call.
  - An approved call whose finished turn is lost to a crash runs again on recovery, like any call whose turn was lost. `TestAnswerSurvivesCrashes` records this.
- **A suspended chat run keeps its conversation busy:** a new message gets 409 `conversation_busy`, unlike an interrupted run. Cancel ends it, and appends the marker reply if the conversation ends on the user's message.
- **Default mode.**
  - The harness default is `autonomous`, so existing callers behave as before. An `approve` stays a refusal, with a reason that names the mode.
  - Agents in the serve config default to `autonomous` too. The console cannot answer yet (T087/T055), and an interactive default would leave console users with runs they cannot finish.
  - `"interaction": "interactive"` opts in. A run request may only narrow the mode.
- **`supervised`** behaves like `interactive` in the harness and runner. Notifications are E14's.
- **Who answered** is the authenticated user (`auth.Principal.User`), never the request body. With a shared token there is no user, so the denial reads "denied by a person".
- **Version skew:**
  - A run that may wait (interactive or supervised), or that finishes a pending turn, goes to `runner/RunWithInterrupts`. An older runner answers `unknown_method`, and the run fails without being retried, instead of dropping the answers.
  - A run that sets an autonomous mode still goes to the older methods. A runner that predates interaction modes never waits anyway.

### Open points

- `toolrpc` does not carry `*tool.InterruptError`. A remote toolset's tool that asks is reported as an error, so `ask_user` works only in-process. A follow-up would add a wire code.
- The `ask` command (one-shot) prints a suspended run as `suspended` and exits with the failure code. Only `chat` answers.
- `GET /v1/runs?status=suspended` lists suspended chat runs alongside flow runs. An inbox would want to filter by kind.
- The approval interrupt's `Prompt` is the rule's reason. A tool-provided summary of the call (Display) would make a better card.

### For T087 / T055 (console)

- An `interrupt` event (with `interrupt.kind`, `prompt`, `choices`, `input`, `policy`, `rule`) comes where a call's `tool_result` would be.
- The stream then ends with `run_error` code `suspended`, and the run's `interrupts` list what is open, each with its `answer` once given.
- Post `{"answers": [...]}` to `/v1/runs/{id}/input`. With `Accept: text/event-stream`, the response follows the run from the last answer on.
- After the resume, the turn's events go on as the same turn: the answered calls' `tool_call` again, then their `tool_result` and `turn_done`.

### For T071 (tasks) and E14 (notifications)

- `supervised` needs a "waiting for input" task state and a notification when a run suspends. The runner's `outSuspended` outcome with interrupt IDs in `store.Run.Pending` is the hook.
- `/v1/tasks/{id}/input` can call `Runner.Answer` with the task's run.

### For T086 (steering)

- A steering message to a suspended run is neither an answer nor a new run. Decide whether it cancels the pending turn (deny everything with the message as the reason) or waits.

### Mutation list

| Planted bug | Caught by |
|---|---|
| approved call runs with the reply's input, not the approved one | TestAnswerSurvivesCrashes, TestAnswersComeOneByOne, TestApprovalRunsTheOriginalInputOrDenies and others |
| a denial is not an error result | TestApprovalRunsTheOriginalInputOrDenies, TestInterruptSimulation |
| a denial loses its reason | TestApprovalRunsTheOriginalInputOrDenies, TestInputDenies, TestInterruptSimulation |
| autonomous runs are offered interrupting tools | TestAutonomousNeverWaits, TestAutonomousRunsNeverWait, TestInterruptSimulation and others |
| supervised does not wait | TestApprovalRunsTheOriginalInputOrDenies |
| NarrowInteraction keeps the laxer mode | TestAutonomousNeverWaits, TestAutonomousRunsNeverWait, TestInterruptSimulation and others |
| a missing answer is accepted | TestBadAnswersAreInvalidInput |
| choices not enforced | TestBadAnswersAreInvalidInput, TestOpenAPIResponses |
| text accepted for an approval | TestAnswersComeOneByOne, TestBadAnswersAreInvalidInput, TestInputAnswersInterrupts |
| a tool that asks in an autonomous run suspends anyway | TestAutonomousNeverWaits, TestInterruptSimulation |
| the approval interrupt drops the call's input | TestAnswerSurvivesCrashes, TestAnswersComeOneByOne, TestApprovalRunsTheOriginalInputOrDenies and others |
| a different answer is taken as the same | TestAnswersComeOneByOne, TestInputAnswersInterrupts, TestInterruptSimulation |
| Answer resumes before every interrupt has its answer | TestInputAnswersInterrupts |
| the answers are not stored before the resume | TestAnswerSurvivesCrashes, TestAnswersComeOneByOne, TestApprovalRunsTheOriginalInputOrDenies and others |
| the resume drops the answers | TestAnswerSurvivesCrashes, TestAnswersComeOneByOne, TestApprovalRunsTheOriginalInputOrDenies and others |
| a later entry does not clear the pending turn | TestAnswerSurvivesCrashes |
| the run's interaction is not kept with its prompt | TestAutonomousRunsNeverWait, TestInterruptSimulation |
| a suspended chat run may be resumed without its answers | TestApprovedRunCompletes, TestSuspendedChatRunGuards |
| the suspended run's Pending is not its interrupts | TestAnswerSurvivesCrashes, TestAnswersComeOneByOne, TestApprovalRunsTheOriginalInputOrDenies and others |
| an old runner gets runs that may wait | TestOldRunnerRefusesARunThatMayWait |
| the suspended result's pending turn is not sent | TestRemoteRunSuspendsAndResumesLikeLocal |
| the answers are not sent to the runner | TestRemoteRunSuspendsAndResumesLikeLocal |
| the API takes who answered from the body | TestInputAnswersInterrupts |
| the pending turn's events are numbered as the turn before | TestInputAnswersInterrupts, TestInputDenies |
| the API's run turns are not counted after the pending turn | TestInputAnswersInterrupts, TestInputDenies |
| a resume rewinds the pending turn's events | TestInputAnswersInterrupts |
| a suspended run shows no interrupts | TestAnswer, TestInputAnswersInterrupts, TestOpenAPIResponses |
| the interaction of a run request is ignored | TestRunRequestInteraction |
| an interrupt entry counts as messages | TestInputAnswersInterrupts, TestInputDenies |
| an interrupt entry joins the context | TestAnswerSurvivesCrashes, TestAnswersComeOneByOne, TestApprovalRunsTheOriginalInputOrDenies and others |
| chat reads n as an approval | TestAnswerOf, TestChatAnswersApprovals |
| ask_user takes one choice | TestAskUserInterrupts |
| ask_user passes the choices on in no order | TestAskUserInterrupts |

Each bug was planted alone by a script (scratch `mutate.py`), the tests of the packages that use the code ran with `-short`, and the file was restored. All 33 were caught.

### Size

+139,264 bytes on the `agentkit` binary and 120 to 145 KB on every
variant, with no new dependency (docs/SIZES.md row). Under every budget.

### Files

- New:
  - `pkg/tool/interrupt.go`;
  - `pkg/harness/interrupt.go`, `pkg/harness/interrupt_test.go`;
  - `pkg/coretools/user.go`, `pkg/coretools/user_test.go`;
  - `pkg/runner/answer.go`, `pkg/runner/interrupt_test.go`, `pkg/runner/interrupt_sim_test.go`, `pkg/runner/interrupts_helpers_test.go`;
  - `pkg/runner/runnerrpc/interrupt_test.go`;
  - `pkg/agentapi/interrupt_test.go`;
  - `pkg/agentclient/answer_test.go`;
  - `cmd/agentkit/interrupt_test.go`.
- Changed:
  - `pkg/harness`: tools, harness, options, events, errors, records, transcript, and the invariants and simulation;
  - `pkg/runner`: exec, cache, record, compact;
  - `pkg/store`: `KindInterrupt`, message counts in sqlite and postgres, storetest;
  - `pkg/runner/runnerrpc`;
  - `pkg/agentapi`: wire, runs, server, events, replay, orchestrator, doc, `wire/wire.go`, `wire/catalog.go`;
  - `pkg/agentclient/runs.go`;
  - `cmd/agentkit`: agent, catalog, chat, client, run;
  - `pkg/policy` and `pkg/tool` docs;
  - `docs/api/openapi.yaml`, `docs/QUICKSTART.md`.
