// The tasks (background runs) the console watches (docs/UI.md "Tasks", T074):
// the shell loads GET /v1/runs?background=true now and then, so the Tasks
// list stays current wherever you are, and a run that ends (done, failed,
// canceled elsewhere) or starts waiting for input while you are not looking
// at it raises a quiet notification (a toast with Open) and an unread mark,
// and, while it waits for you, the rail's badge. The first load is the baseline: runs that had
// already ended then are not news.
//
// It lives above the router, like the chat runs, and is in the first load:
// keep it small. The pages (list, run and thread views) are lazy.

import { ApiError, type AgentClient, type Run } from "@agentkit/agent-ui/api";
import type { StatusTone } from "@agentkit/ui";

/** How often the runs are loaded again, while the page is visible. */
export const pollMs = 5000;

/** How many runs the list holds: the newest page. */
export const listLimit = 50;

/** How long a toast stays: 8 s, paused while it has the pointer or focus (docs/UI.md "Toast"). */
export const toastMs = 8000;

/** The localStorage key of the unread runs (a per-browser convenience). */
export const unreadStorageKey = "agentkit.tasks.unread";

/** A run's status as the console shows it: a tone, a word, and whether it can still be canceled. */
export function runStatus(status: string): { tone: StatusTone; word: string; active: boolean } {
  switch (status) {
    case "running":
      return { tone: "neutral", word: "Running", active: true };
    case "suspended":
      return { tone: "warn", word: "Waiting for you", active: true };
    case "interrupted":
      return { tone: "warn", word: "Interrupted", active: true };
    case "paused":
      return { tone: "warn", word: "Paused", active: true };
    case "done":
      return { tone: "ok", word: "Done", active: false };
    case "failed":
      return { tone: "bad", word: "Failed", active: false };
    case "rejected":
      return { tone: "bad", word: "Rejected", active: false };
    case "canceled":
      return { tone: "neutral", word: "Canceled", active: false };
    default:
      return { tone: "neutral", word: status || "Unknown", active: false };
  }
}

/** The list's groups (docs/UI.md "Tasks"). */
export type Group = "waiting" | "running" | "finished";

export function groupOf(status: string): Group {
  if (status === "suspended") return "waiting";
  return runStatus(status).active ? "running" : "finished";
}

/** How many tasks wait for you: the rail's badge. */
export function waitingCount(runs: readonly Run[] | null): number {
  return runs?.filter((r) => r.status === "suspended").length ?? 0;
}

/** What a change of status is worth telling: "finished", "failed", "needs you"; undefined for nothing. */
export function newsOf(before: string | undefined, now: string): string | undefined {
  if (before === now) return undefined;
  if (before !== undefined && !runStatus(before).active) return undefined; // ended already
  switch (now) {
    case "done":
      return "finished";
    case "failed":
    case "rejected":
      return "failed";
    case "canceled":
      return "was canceled";
    case "suspended":
      return "is waiting for you";
    default:
      return undefined;
  }
}

export type Toast = { key: number; text: string; runId: string };

export type TasksState = {
  /** The newest background runs; null until the first load. */
  runs: Run[] | null;
  /** Why the last load failed; the list keeps what it had. */
  error: Error | null;
  unread: ReadonlySet<string>;
  toasts: readonly Toast[];
};

type Hooks = { client: () => AgentClient; unauthorized: () => void; accepted: () => void };

function loadUnread(): Set<string> {
  try {
    const v: unknown = JSON.parse(localStorage.getItem(unreadStorageKey) ?? "[]");
    return new Set(Array.isArray(v) ? v.filter((x): x is string => typeof x === "string") : []);
  } catch {
    return new Set();
  }
}

export class TaskStore {
  #state: TasksState = { runs: null, error: null, unread: loadUnread(), toasts: [] };
  #listeners = new Set<() => void>();
  #hooks: Hooks;
  #timer: ReturnType<typeof setTimeout> | undefined;
  #ctl: AbortController | undefined;
  /** The visibilitychange listener that loads again when a hidden tab shows. */
  #onShow: (() => void) | undefined;
  #running = false;
  /** The status of each run at the last load; the first load sets it without news. */
  #seen: Map<string, string> | undefined;
  /** When the first load was: a run that started after it and is already over is news. */
  #since = 0;
  /** Runs canceled from this tab: their end is no news. */
  #canceled = new Set<string>();
  /** The run on screen: its news is no news, and it is never unread. */
  #viewing: string | undefined;
  #toastKey = 0;
  readonly #every: number;

  constructor(hooks: Hooks, every = pollMs) {
    this.#hooks = hooks;
    this.#every = every;
  }

  setHooks(hooks: Hooks) {
    this.#hooks = hooks;
  }

  subscribe = (f: () => void) => {
    this.#listeners.add(f);
    return () => this.#listeners.delete(f);
  };

  get = () => this.#state;

  #set(patch: Partial<TasksState>) {
    this.#state = { ...this.#state, ...patch };
    for (const f of this.#listeners) f();
  }

  /** Starts loading (now, then every pollMs); stop() ends it. */
  start() {
    if (this.#running) return;
    this.#running = true;
    void this.refresh();
  }

  stop() {
    this.#running = false;
    clearTimeout(this.#timer);
    this.#unwatch();
    this.#ctl?.abort();
  }

  #unwatch() {
    if (this.#onShow) document.removeEventListener("visibilitychange", this.#onShow);
    this.#onShow = undefined;
  }

  /** Forgets everything (sign out, another user). */
  clear() {
    this.stop();
    this.#seen = undefined;
    this.#canceled.clear();
    try {
      localStorage.removeItem(unreadStorageKey);
    } catch {
      // Storage blocked: nothing kept.
    }
    this.#set({ runs: null, error: null, unread: new Set(), toasts: [] });
  }

  /** Loads the runs now (and schedules the next load). */
  async refresh(): Promise<void> {
    clearTimeout(this.#timer);
    this.#unwatch();
    this.#ctl?.abort();
    const ctl = new AbortController();
    this.#ctl = ctl;
    try {
      const { runs } = await this.#hooks
        .client()
        .listRuns({ background: true, limit: listLimit }, { signal: ctl.signal });
      if (ctl.signal.aborted) return;
      this.#hooks.accepted();
      this.#took(runs);
    } catch (e) {
      if (ctl.signal.aborted) return;
      if (e instanceof ApiError && e.status === 401) {
        this.#hooks.unauthorized();
        return; // the provider starts again once signed in
      }
      this.#set({ error: e instanceof Error ? e : new Error(String(e)) });
    }
    if (this.#running && this.#ctl === ctl) {
      this.#timer = setTimeout(() => {
        if (typeof document !== "undefined" && document.visibilityState === "hidden") {
          // Not while the tab is hidden: look again when it shows. One
          // listener at a time; stop() and the next load remove it.
          const again = () => {
            if (document.visibilityState === "hidden") return;
            this.#unwatch();
            if (this.#running) void this.refresh();
          };
          this.#onShow = again;
          document.addEventListener("visibilitychange", again);
          return;
        }
        void this.refresh();
      }, this.#every);
    }
  }

  #took(runs: Run[]) {
    const first = this.#seen === undefined;
    const seen = this.#seen ?? new Map<string, string>();
    const unread = new Set(this.#state.unread);
    const toasts = [...this.#state.toasts];
    if (first) this.#since = Date.now();
    for (const r of runs) {
      const before = seen.get(r.id);
      seen.set(r.id, r.status);
      if (first) continue;
      // A run that this tab hasn't seen yet is news only if it started after
      // the baseline (one that started and ended between two loads).
      if (before === undefined && Date.parse(r.created_at) < this.#since) continue;
      const news = newsOf(before, r.status);
      if (!news || this.#canceled.has(r.id) || r.id === this.#viewing) continue;
      unread.add(r.id);
      toasts.push({ key: ++this.#toastKey, text: `Task of ${r.agent} ${news}.`, runId: r.id });
    }
    this.#seen = seen;
    this.#saveUnread(unread);
    // At most three toasts at once: the oldest go first.
    this.#set({ runs, error: null, unread, toasts: toasts.slice(-3) });
  }

  #saveUnread(unread: ReadonlySet<string>) {
    try {
      localStorage.setItem(unreadStorageKey, JSON.stringify([...unread]));
    } catch {
      // Storage blocked: kept for this page only.
    }
  }

  /** The run on screen (undefined when none): it is read, and its end makes no toast. */
  view(id: string | undefined) {
    this.#viewing = id;
    if (id) this.markRead(id);
  }

  markRead(id: string) {
    if (!this.#state.unread.has(id)) return;
    const unread = new Set(this.#state.unread);
    unread.delete(id);
    this.#saveUnread(unread);
    this.#set({ unread });
  }

  dismiss(key: number) {
    this.#set({ toasts: this.#state.toasts.filter((t) => t.key !== key) });
  }

  /**
   * Cancels the run (POST /v1/runs/{id}/cancel), puts what the server says
   * in the list, and loads the list again. A run that ended before the
   * cancel reached it (409 run_final) resolves with the run as it ended.
   * Rejects with the server's other errors.
   */
  async cancel(id: string): Promise<Run> {
    this.#canceled.add(id);
    let run: Run;
    try {
      run = await this.#hooks.client().cancelRun(id);
    } catch (e) {
      if (e instanceof ApiError && e.status === 401) this.#hooks.unauthorized();
      if (!(e instanceof ApiError && e.status === 409 && e.code === "run_final")) throw e;
      // It had ended already: show how.
      run = await this.#hooks.client().getRun(id);
    }
    const runs = this.#state.runs?.map((r) => (r.id === id ? { ...r, ...run } : r)) ?? null;
    this.#seen?.set(id, run.status);
    this.#set({ runs });
    if (this.#running) void this.refresh();
    return run;
  }
}
