// What the Tasks pages share that isn't a component.

import { usageText } from "@agentkit/agent-ui";
import { ApiError, type AgentClient, type Run, type Thread, type ThreadMessage } from "@agentkit/agent-ui/api";
import { useEffect, useEffectEvent, useState } from "react";
import { useApi } from "../api";

/** "48 s", "2 min 14 s", "1 h 3 min". */
export function elapsedText(ms: number): string {
  const s = Math.max(0, Math.floor(ms / 1000));
  if (s < 60) return `${s} s`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m} min ${s % 60} s`;
  return `${Math.floor(m / 60)} h ${m % 60} min`;
}

/** How long a run has taken: to its end, or to now while it goes on. */
export function runElapsed(run: Pick<Run, "created_at" | "finished_at">, now: number): string {
  const start = Date.parse(run.created_at);
  if (Number.isNaN(start)) return "";
  const end = run.finished_at ? Date.parse(run.finished_at) : now;
  return elapsedText((Number.isNaN(end) ? now : end) - start);
}

/** When a run started: the time today, else the date and time. */
export function startedText(iso: string, now: Date): string {
  const t = new Date(iso);
  if (Number.isNaN(t.getTime())) return "";
  const today = t.toDateString() === now.toDateString();
  return today
    ? t.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" })
    : t.toLocaleString(undefined, { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" });
}

/** "1,234 tokens (900 cached)", or "" when none yet. */
export function tokensText(run: Pick<Run, "usage">): string {
  return run.usage ? usageText({ usage: run.usage }) : "";
}

/** "researcher" for "agent:researcher"; other addresses (agentkit's notices) as they are. */
export function agentName(address: string): string {
  return address.startsWith("agent:") ? address.slice("agent:".length) : address;
}

/** The agents of a thread, in the order they first speak or are spoken to; the server's notices aren't one. */
export function participants(messages: readonly ThreadMessage[]): string[] {
  const out: string[] = [];
  for (const m of messages) {
    for (const a of [m.from, m.to]) {
      if (a.startsWith("agent:") && !out.includes(agentName(a))) out.push(agentName(a));
    }
  }
  return out;
}

/**
 * The run that wrote a message, where the thread says: a reply was written
 * by the run that processed its request; any other message by the run that
 * processed the latest earlier message to the conversation it was sent from.
 * undefined when the thread doesn't say (it began in a chat or a task).
 */
export function writtenBy(m: ThreadMessage, messages: readonly ThreadMessage[]): string | undefined {
  if (m.in_reply_to) {
    const req = messages.find((x) => x.id === m.in_reply_to);
    if (req?.run_id) return req.run_id;
  }
  if (!m.from_conversation) return undefined;
  for (let i = messages.length - 1; i >= 0; i--) {
    const x = messages[i];
    if (x && x.hop < m.hop && x.to_conversation === m.from_conversation && x.run_id) return x.run_id;
  }
  return undefined;
}

/** A thread's state as a tone and a word. */
export function threadStatus(t: Pick<Thread, "status" | "messages">): {
  tone: "ok" | "warn" | "bad" | "neutral";
  word: string;
} {
  if (t.status === "stopped") return { tone: "bad", word: "Stopped" };
  const ms = t.messages ?? [];
  if (ms.some((m) => m.state !== "done")) return { tone: "neutral", word: "Working" };
  if (ms.some((m) => m.kind === "request" && !ms.some((r) => r.in_reply_to === m.id)))
    return { tone: "neutral", word: "Waiting for a reply" };
  return { tone: "ok", word: "Done" };
}

export type Polled<T> =
  | { state: "loading" }
  | { state: "ready"; data: T; error?: Error }
  | { state: "failed"; error: Error; retry: () => void };

/**
 * Loads with the client now and every ms after (while the tab is visible),
 * keeping what it had while it loads again: a list that refreshes doesn't
 * flash its skeleton. A 401 opens the sign-in card and stops. key changes
 * start over. ms 0 loads once.
 */
export function usePolled<T>(
  load: (client: AgentClient, signal: AbortSignal) => Promise<T>,
  key: string,
  ms: number,
): Polled<T> {
  const { client, epoch, prompt, unauthorized, accepted } = useApi();
  const [attempt, setAttempt] = useState(0);
  const id = `${key}\n${epoch}\n${attempt}`;
  const [got, setGot] = useState<{ id: string; res: Polled<T> } | null>(null);
  const run = useEffectEvent((signal: AbortSignal) => load(client, signal));
  const settle = useEffectEvent((res: Polled<T>) => setGot({ id, res }));
  const previous = useEffectEvent(() => (got?.id === id ? got.res : undefined));

  useEffect(() => {
    if (prompt !== null) return;
    const ctl = new AbortController();
    let timer: ReturnType<typeof setTimeout> | undefined;
    let shown: (() => void) | undefined;
    const once = () => {
      run(ctl.signal).then(
        (data) => {
          if (ctl.signal.aborted) return;
          accepted();
          settle({ state: "ready", data });
          if (ms > 0) timer = setTimeout(again, ms);
        },
        (error: unknown) => {
          if (ctl.signal.aborted) return;
          if (error instanceof ApiError && error.status === 401) {
            unauthorized();
            return;
          }
          const err = error instanceof Error ? error : new Error(String(error));
          const had = previous();
          if (had?.state === "ready") {
            settle({ ...had, error: err });
            if (ms > 0) timer = setTimeout(again, ms);
          } else {
            settle({ state: "failed", error: err, retry: () => setAttempt((a) => a + 1) });
          }
        },
      );
    };
    const again = () => {
      if (document.visibilityState === "hidden") {
        const show = () => {
          if (document.visibilityState === "hidden") return;
          document.removeEventListener("visibilitychange", show);
          shown = undefined;
          if (!ctl.signal.aborted) once();
        };
        shown = show;
        document.addEventListener("visibilitychange", show);
        return;
      }
      once();
    };
    once();
    return () => {
      ctl.abort();
      clearTimeout(timer);
      if (shown) document.removeEventListener("visibilitychange", shown);
    };
  }, [id, ms, prompt, accepted, unauthorized]);

  return got?.id === id ? got.res : { state: "loading" };
}

/** Ticks every second while on, for elapsed times. */
export function useNow(on: boolean): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (!on) return;
    const t = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(t);
  }, [on]);
  return now;
}

/** The threads of a page with their messages, so the list can show who and what was said last. */
export async function threadsWithMessages(client: AgentClient, signal: AbortSignal): Promise<Thread[]> {
  const { threads } = await client.listThreads({ limit: 20 }, { signal });
  // GET /v1/threads has no participants or last message: read each thread (T074 Notes, API gaps).
  return Promise.all(threads.map((t) => client.getThread(t.id, { signal }).catch(() => t)));
}

export type Segment = "tasks" | "threads" | "triggers";

/** The tab a path shows: /tasks/threads… is Threads, /tasks/triggers Triggers, the rest Tasks. */
export function segmentOf(pathname: string): Segment {
  if (pathname === "/tasks/threads" || pathname.startsWith("/tasks/threads/")) return "threads";
  if (pathname === "/tasks/triggers") return "triggers";
  return "tasks";
}

/** The task (/tasks/{run_id}) or thread (/tasks/threads/{id}) in the URL; "" when none. */
export function selectedOf(pathname: string): string {
  const m = /^\/tasks\/(?:threads\/)?([^/]+)$/.exec(pathname);
  if (m?.[1] === "threads" || m?.[1] === "triggers") return "";
  try {
    return m?.[1] ? decodeURIComponent(m[1]) : "";
  } catch {
    return "";
  }
}

/** Asks before canceling (docs/UI.md "Cancel… asks first"). */
export function confirmCancel(agent: string): boolean {
  return window.confirm(
    `Cancel this task of ${agent}? It ends for good: waiting calls and queued messages are dropped.`,
  );
}
