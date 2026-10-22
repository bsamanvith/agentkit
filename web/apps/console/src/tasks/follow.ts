// Following one run's event stream (GET /v1/runs/{id}/events) for the run
// view: the stored events from the start, then the live ones, with the same
// answer model as chat (applyEvent), so steps render the same way. The
// server ends the stream early when another process runs the run, or when a
// task waits to be picked up after a restart: then it follows again from the
// last event it has (Last-Event-ID), backing off, until the run's end event
// or the run is over.

import { applyEvent, endAnswer, promptOf, startAnswer, type AssistantTurn, type UserTurn } from "@agentkit/agent-ui";
import { ApiError, type AgentClient, type RunEvent } from "@agentkit/agent-ui/api";
import { runStatus } from "./store";

export type RunView = {
  /** The message the run started from (run_start's prompt). */
  prompt?: UserTurn;
  answer: AssistantTurn;
  /** The id of the last event taken, for Last-Event-ID. */
  last?: string;
  /** The stream ended for good: the end event came, or the run is over without one. */
  ended: boolean;
  /** Why the last attempt failed (it is tried again, unless gone is set). */
  problem?: Error;
  /** The run can't be followed: 404 (no such run) or 401. */
  gone?: boolean;
};

export function startView(): RunView {
  return { answer: startAnswer("answer", Date.now()), ended: false };
}

const at = (ev: RunEvent) => {
  const t = typeof ev.at === "string" ? Date.parse(ev.at) : NaN;
  return Number.isNaN(t) ? Date.now() : t;
};

/** The view after one event. Pure. */
export function take(v: RunView, ev: RunEvent): RunView {
  let { answer, prompt } = v;
  if (ev.type === "run_start") {
    prompt = promptOf(ev, "prompt") ?? prompt;
    // Times come from the events, so a replayed run's duration is its own.
    answer = { ...answer, startedAt: at(ev) };
  }
  answer = applyEvent(answer, ev, at(ev));
  const ended = answer.state !== "streaming";
  const next: RunView = { ...v, answer, last: ev.id, ended };
  if (prompt) next.prompt = prompt;
  delete next.problem;
  return next;
}

export type FollowOptions = {
  signal: AbortSignal;
  /** Waits ms (a test passes a fake). */
  wait?: (ms: number, signal: AbortSignal) => Promise<void>;
  unauthorized?: () => void;
};

const sleep = (ms: number, signal: AbortSignal) =>
  new Promise<void>((resolve) => {
    const t = setTimeout(resolve, ms);
    signal.addEventListener("abort", () => {
      clearTimeout(t);
      resolve();
    });
  });

/**
 * Follows run id until it ends or the signal aborts, calling emit with each
 * new view. Resolves when it stops.
 */
export async function follow(
  client: AgentClient,
  id: string,
  emit: (v: RunView) => void,
  { signal, wait = sleep, unauthorized }: FollowOptions,
): Promise<void> {
  let view = startView();
  let backoff = 500;
  while (!signal.aborted) {
    try {
      const events = client.events(id, { after: view.last, signal });
      let next = await events.next();
      while (!next.done) {
        view = take(view, next.value);
        emit(view);
        backoff = 500;
        next = await events.next();
      }
      if (next.value || view.ended) return;
      // The stream ended before the run did: is it over?
      const run = await client.getRun(id, { signal });
      if (!runStatus(run.status).active) {
        view = { ...view, answer: endAnswer(view.answer, "stopped", Date.now()), ended: true };
        emit(view);
        return;
      }
    } catch (e) {
      if (signal.aborted) return;
      const error = e instanceof Error ? e : new Error(String(e));
      if (e instanceof ApiError && (e.status === 404 || e.status === 401)) {
        if (e.status === 401) unauthorized?.();
        emit({ ...view, problem: error, gone: true });
        return;
      }
      view = { ...view, problem: error };
      emit(view);
    }
    await wait(backoff, signal);
    backoff = Math.min(backoff * 2, 5000);
  }
}
