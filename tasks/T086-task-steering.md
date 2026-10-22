---
id: T086
title: "Steering a running task"
epic: E17
status: done
depends_on: [T071]
---

# T086: steering

## Context

A person watching a task's transcript can nudge it ("focus on 2026 data", "skip the PDF"), without stopping it. Decisions: [E17](E17-tool-safety-control.md) ("Steering").

## Scope

- **Queueing:** `POST /v1/tasks/{id}/messages {text}` queues a steering message. The runner delivers it at the next turn boundary as a user message marked `steer`, and emits a `steered` event.
- **Turn boundaries:**
  - several queued messages are delivered together, in order;
  - a message sent after the final turn is refused with a clear status (the task finished).
- **Pause and resume:** `POST /v1/tasks/{id}/pause` stops at the next boundary (status `paused`); `resume` continues; pause never interrupts a tool call in flight.
- **Clients:** `agentclient` and the OpenAPI spec.

## References

- Claude Code's message queueing while a turn runs (what users expect of steering)
- E05's turn atomicity and one-unfinished-run rule (steering must not break them)

## Acceptance criteria

- [x] **Delivery:** a steering message sent during a tool call is delivered after that call's results and before the next model call (event order checked); the transcript stays valid.
- [x] **Ordering:** two messages are delivered together, in order; a message after completion is refused.
- [x] **Pause and resume:** pause stops at the boundary, resume continues; a restart while paused stays paused.
- [x] **Mutation list** recorded.

## Notes

### What was built

- **Commands, not a new channel.** A message, a pause and a resume are commands in the run's command log (`store.CommandLog`, the one stop and cancel use), sent through any server: `runner.SteerCommand(key, msg)`, `runner.PauseCommand(key)`, `runner.ResumeCommand(key)` (types `steer`, `pause`, `resume`). A request answers 202 once the command is stored; it never waits for the task.
- **The runner reads them at turn boundaries** when built with `runner.WithCommands(log)` (the agent API passes its store). A boundary is a harness `EventTurnDone` whose stop is `tool_use` or `end_turn` (the turn's tool results are in it), and the start of a resumed run. No model or tool call is in flight there. The commands are read in order up to the first one that is not the runner's (a stop or cancel, which agentapi's `obey` acts on); `obey` likewise consumes only the prefix before the first runner command. So each side consumes what it owns, in order.
- **Delivery.** At a boundary with steering (or a pause), the runner stops iterating the agent, stores its turns, appends one `KindSteer` entry holding every message read (its record lists the command seqs, `Record.Steer`), emits `EventSteered`, consumes the commands, and runs the agent again from the transcript (turns numbered from 1 again; not a retry attempt). The runner's view puts steering on the last user message (tool results, then the text), so roles still alternate and every adapter's request stays valid; `harness.Transcript.Validate` now allows text blocks after a message's tool results (and only text). After the model's answer (`end_turn`), the steering is the next user message, and the model answers it.
- **`store.StatusPaused`**: running → paused, paused → running or canceled. Not final; it counts as the conversation's unfinished run; `ListResumable`, `ListResumableAll` and `ClaimNext` skip it (the SQL already filters statuses explicitly, so no migration). `Runner.Resume` continues it. The execution of a paused run ends with `EventDone` (status paused); the agent API streams `run_error` with code `paused` (not stored as a failure).
- **Agent API:** `POST /v1/tasks/{id}/messages` (202 `TaskMessage`, 400 empty text or a reused `Idempotency-Key`, 404 no task or not a task, 409 `run_final`, 413), `/pause` (202 the task still running, 200 paused already, 409 final), `/resume` (202 relaunched, 409 final or `run_leased`, 429 `too_many_background_runs`, 503). A durable `steered` event carries `steering: [texts]`, between the turn before it and the next one; conversation messages mark steering with `steer: true`. Status `paused` in `Run.status`. `Idempotency-Key` makes a retried command the same command.
- **agentclient:** `SteerTask`, `PauseTask`, `ResumeTask`.
- Docs: runner package doc ("Steering"), the OpenAPI spec (three paths, `SteerRequest`, `TaskMessage`, `Event.steering`, `TimedMessage.steer`, status and error code `paused`).

### Decisions (defaults for open questions)

- **A pause read with the model's answer is ignored**; the run completes (there is nothing left to pause). Steering read there is delivered and answered.
- **A resume before the pause took withdraws it** (both are consumed at the next boundary, nothing happens).
- **A stop or cancel behind steering waits for the next boundary** in order; a cancel is acted on anywhere in the log, as before.
- **Budgets** (max turns) count per agent segment after steering, as after a retry. The **wall-clock limit keeps counting while a task is paused** (see owner questions).
- **Resume counts against the tenant's background-run limit** (429 when full); paused tasks do not count toward it.
- **Interactive and flow runs are not tasks**: the endpoints answer 404 for them.
- **Dedupe across a crash:** a crash after the steer entry is stored and before the commands are consumed delivers nothing twice (the view tracks the delivered seqs). That memory is lost only if the conversation was compacted and the cache is cold, a negligible window.
- **Remote agents** (`AgentFactory` pools): the runner stops iterating at the boundary; a model call the remote side already started is canceled. Orchestrators behind `WithRuns` support steering only if they implement it.
- The runner reads the command log once per turn boundary (one indexed read).

### Tests, by criterion

- **Delivery:** `pkg/runner` `TestSteeringIsDeliveredAtTheNextTurnBoundary` (sent while a tool runs: events tool done → turn done → steered → next model call; the request carries results then text; the stored transcript validates), `TestSteeringAfterTheLastTurnIsAnswered`, `TestSteeringReachesARunAcrossACrash` (sent while down; crash before consuming), `TestSteeringWaitsBehindAnotherCommand`, `TestSteerCommandRefusesWhatIsNotText`; `pkg/agentapi` `TestTaskSteering` (HTTP event order, the conversation marks steering in place, the stream check validates every transcript); `pkg/harness` `TestInvalidInputIsRejectedBeforeAnyModelCall` (non-text after results refused).
- **Ordering:** `TestSteeringMessagesAreDeliveredTogetherInOrder` (two messages, one steered event, in order, then `ErrFinal` after completion); `TestTaskSteering` (both texts in one `steered`, 409 `run_final` after the end, 404 for a chat); `pkg/agentclient` `TestSteerTask`.
- **Pause and resume:** `TestPauseStopsAtTheBoundaryAndSurvivesARestart` (the tool call finishes, paused, a restart's recovery leaves it, resume answers, no model call done twice), `TestResumeWithdrawsAPauseNotActedOn`, `TestAPausedRunCanBeCanceled`; `TestTaskPauseAndResume` (three replicas: steer and pause through one, the task pauses on another, a restart's dispatcher passes it, a resume through a third continues it with the steering read first); storetest (paused is never listed resumable or claimed; allowed moves); `TestOpenAPIResponses` probes every documented status of the three paths.
- **Invariants:** the runner's invariant checks (`helpers_test.go`) now match each `EventSteered` with its steer entry; the agent API stream check accepts `steered` only at a turn boundary.

### Mutation list

Each planted alone (`scratchpad/t086/mutate.py`), then `go test -count=1` on the package; all compiled and every one was caught.

| Planted bug | Caught by |
|---|---|
| no dedupe of delivered steering | `TestSteeringReachesARunAcrossACrash/crash_before_consuming` |
| steering as a user message of its own (two user messages in a row) | `TestSteeringIsDeliveredAtTheNextTurnBoundary`, `TestSteeringMessagesAreDeliveredTogetherInOrder`, `TestSteeringReachesARunAcrossACrash` |
| only the first queued message delivered | `TestSteeringMessagesAreDeliveredTogetherInOrder` |
| queued messages delivered in reverse | `TestSteeringMessagesAreDeliveredTogetherInOrder` |
| pause acted on at the model's answer | `TestResumeWithdrawsAPauseNotActedOn/pause_at_the_end` |
| no boundary when a resumed run starts | `TestSteeringReachesARunAcrossACrash/sent_while_down` |
| paused runs resumable by recovery | storetest `ListResumableFindsInterruptedAndDueRuns`, `Simulation` |
| agentapi's `obey` consumes the runner's commands | `TestTaskSteering`, `TestTaskPauseAndResume` |
| harness accepts any block after the tool results (survived; the case was added to `TestInvalidInputIsRejectedBeforeAnyModelCall`) | `TestInvalidInputIsRejectedBeforeAnyModelCall` |
| replay: `steered` not a turn bound (turn text placed before it) | `TestTaskPauseAndResume` |
| boundary on a tool result, before the turn is done | `TestSteeringIsDeliveredAtTheNextTurnBoundary`, `TestPauseStopsAtTheBoundaryAndSurvivesARestart` |
| delivered commands never consumed | `TestPauseStopsAtTheBoundaryAndSurvivesARestart`, `TestSteeringReachesARunAcrossACrash` |

Also caught while writing the tests: `steered` events marshaled without their `steering` (the wire form is per type), and replayed turn text placed before `steered`.

### Owner questions

- Should the wall-clock limit stop while a task is paused? Now it keeps counting, so a long pause can fail the task when it is resumed.
- Should a pause expire, or be listed in the console (the paused tasks)? Nothing resumes one on its own.

### For T087 and the console (E14)

- A `steered` event (`steering: [texts]`) comes between a turn's `turn_done` and the next turn's events; show the texts as the person's messages there. Conversation messages carry `steer: true` on them.
- A paused task's stream ends with `run_error` code `paused` and the run's status is `paused`; it is not a failure. Offer resume (`POST /v1/tasks/{id}/resume`); 429 means the tenant is at its background limit.
- `POST /v1/tasks/{id}/pause` answers 202 with the task still `running` until its boundary; poll or follow the stream.

### Files outside the task's packages

`pkg/harness/transcript.go` (text after results), `pkg/store` (status, kind, storetest), `docs/api/openapi.yaml`.
