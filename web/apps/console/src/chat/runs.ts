// The app's runs, keyed by conversation ID (docs/UI.md "Runs and
// conversations"): a run belongs to its conversation, not to the screen
// showing it, so it goes on while you look at another conversation or an
// admin page, and its page picks it up again when you come back.
//
// Stop asks the server to stop the run (POST /v1/runs/{id}/stop, T037) and
// closes its stream. Resume is a run with an empty message. Pages read
// a conversation's state with useSyncExternalStore; each state is a new
// object when it changes, and text deltas are applied once per frame.

import {
  applyEvent,
  endAnswer,
  startAnswer,
  SurfaceEdits,
  type AssistantTurn,
  type Turn,
  type UIAction,
  type UserTurn,
} from "@agentkit/agent-ui";
import { ApiError, NetworkError, type AgentClient, type RunEvent, type RunRequest } from "@agentkit/agent-ui/api";
import type { Drafts } from "./drafts";
import { ToolChoices } from "./tool-choices";

/** What is kept of an answer that ended in this tab: its history doesn't carry usage or errors. */
export type AnswerMeta = Pick<AssistantTurn, "state" | "usage" | "turns" | "durationMs" | "error" | "runId">;

/** The run on screen: the thread when it was sent, the message (none when resuming) and the answer. */
export type Live = {
  base: Turn[];
  user?: UserTurn;
  answer: AssistantTurn;
  /** The exchange the answer belongs to: the number of user messages up to it. */
  exchange: number;
};

/**
 * Why the last send didn't start. The message is back in the draft, except
 * while busy (503): then it stays pending and is retried once by itself.
 */
export type Problem = { error: Error; text: string; busy?: "retrying" | "still" };

export type ConversationState = {
  live?: Live;
  meta: ReadonlyMap<number, AnswerMeta>;
  problem?: Problem;
  /** Bumped when a run ends, so the conversation's history is loaded again. */
  version: number;
  /** A run ended while the conversation wasn't on screen. */
  unread: boolean;
};

const empty: ConversationState = { meta: new Map(), version: 0, unread: false };

type Hooks = { unauthorized: () => void; announce: (text: string) => void };

/** How long to wait before retrying a 503 once (docs/UI.md: Retry-After is a fixed 2 s). */
export const busyRetryMs = 2000;

export class RunStore {
  #states = new Map<string, ConversationState>();
  #controllers = new Map<string, AbortController>();
  /** Stops a conversation's run on the server, once its ID is known. */
  #stoppers = new Map<string, () => void>();
  #timers = new Map<string, ReturnType<typeof setTimeout>>();
  #listeners = new Set<() => void>();
  #pending = new Map<string, AssistantTurn>();
  #frame: number | undefined;
  #list = 0;
  #viewing: string | undefined;
  readonly drafts: Drafts;
  /** What the person typed or picked on the agents' surfaces, by conversation. */
  readonly edits = new SurfaceEdits();
  /** Each chat's choice of tools (T115), sent with every message. */
  readonly choices = new ToolChoices();
  #hooks: Hooks;

  constructor(drafts: Drafts, hooks: Hooks) {
    this.drafts = drafts;
    this.#hooks = hooks;
  }

  /** The hooks change with the provider's callbacks. */
  setHooks(hooks: Hooks) {
    this.#hooks = hooks;
  }

  subscribe = (fn: () => void) => {
    this.#listeners.add(fn);
    return () => this.#listeners.delete(fn);
  };

  get = (id: string): ConversationState => this.#states.get(id) ?? empty;

  /** Bumped when the conversation list changes: a run started or ended. */
  listVersion = (): number => this.#list;

  /** Whether the conversation's answer is streaming (or its message is pending). */
  running(id: string): boolean {
    return this.get(id).live?.answer.state === "streaming";
  }

  /** The conversation on screen, which gets no unread mark. */
  view(id: string | undefined) {
    this.#viewing = id;
    if (id !== undefined && this.get(id).unread) this.#update(id, (s) => ({ ...s, unread: false }));
  }

  /** On opening a conversation: an answer that ended while away now comes from its history. */
  settle(id: string) {
    const s = this.#states.get(id);
    if (s?.live && s.live.answer.state !== "streaming") this.#update(id, (st) => ({ ...st, live: undefined }));
  }

  /**
   * Sends a message to a conversation. base is the thread on screen, so the
   * page shows it with the new message and answer until the run ends.
   */
  send(client: AgentClient, id: string, text: string, base: Turn[], agent: string) {
    if (this.running(id)) return;
    const exchange = base.filter((t) => t.role === "user").length + 1;
    const user: UserTurn = { role: "user", key: `u${Date.now()}`, text, pending: true };
    this.drafts.set(id, "");
    void this.#start(
      client,
      id,
      this.#withTools(id, { message: text, conversation_id: id }),
      { base, user, exchange },
      agent,
    );
  }

  /** req with the chat's tool choice, if it made one. */
  #withTools(id: string, req: RunRequest): RunRequest {
    const tools = this.choices.request(id);
    return tools ? { ...req, tools } : req;
  }

  /**
   * Sends the person's action on a surface (a button): a run whose message
   * is the action (ui_action), shown as their message. A send that doesn't
   * start says why, and the surface can be used again.
   */
  act(client: AgentClient, id: string, action: UIAction, base: Turn[], agent: string) {
    if (this.running(id)) return;
    const exchange = base.filter((t) => t.role === "user").length + 1;
    const user: UserTurn = { role: "user", key: `u${Date.now()}`, text: action.name, pending: true, action };
    const req: RunRequest = {
      conversation_id: id,
      ui_action: {
        surface_id: action.surfaceId,
        component_id: action.componentId,
        action: action.name,
        context: action.context,
      },
    };
    void this.#start(client, id, this.#withTools(id, req), { base, user, exchange }, agent);
  }

  /** Resumes an interrupted conversation: a run with no message, which adds no bubble. */
  resume(client: AgentClient, id: string, base: Turn[], agent: string) {
    if (this.running(id)) return;
    const exchange = Math.max(1, base.filter((t) => t.role === "user").length);
    void this.#start(client, id, { message: "", conversation_id: id }, { base, exchange }, agent);
  }

  /** Stops the conversation's run (closes its stream), or cancels a message waiting to be retried. */
  stop(id: string) {
    const timer = this.#timers.get(id);
    if (timer !== undefined) {
      clearTimeout(timer);
      this.#timers.delete(id);
      this.#giveBack(id);
      return;
    }
    this.#stoppers.get(id)?.();
    this.#controllers.get(id)?.abort();
  }

  /** "Retry now" on a message the server was too busy to take. */
  retryNow(client: AgentClient, id: string, agent: string) {
    const s = this.get(id);
    const timer = this.#timers.get(id);
    if (timer !== undefined) clearTimeout(timer);
    this.#timers.delete(id);
    if (!s.live?.user || !s.problem?.busy) return;
    const { base, user, exchange } = s.live;
    const req = this.#withTools(id, { message: user.text, conversation_id: id });
    void this.#start(client, id, req, { base, user, exchange }, agent, true);
  }

  /** Cancel on a busy message: it goes back to the composer. */
  cancel(id: string) {
    this.stop(id);
    if (this.get(id).problem?.busy) this.#giveBack(id);
  }

  /** A deleted conversation: its state goes. */
  forget(id: string) {
    this.#states.delete(id);
    this.drafts.set(id, "");
    this.choices.set(id, {});
    this.edits.forget(id);
    this.#list++;
    this.#emit();
  }

  dismiss(id: string) {
    if (this.get(id).problem) this.#update(id, (s) => ({ ...s, problem: undefined }));
  }

  /** Forget everything (the token was forgotten): stops every run. */
  clear() {
    for (const c of this.#controllers.values()) c.abort();
    for (const t of this.#timers.values()) clearTimeout(t);
    this.#timers.clear();
    this.#states.clear();
    this.drafts.clear();
    this.choices.clear();
    this.edits.clear();
    this.#list++;
    this.#emit();
  }

  /** Whether any run is streaming (the page asks before closing). */
  anyRunning(): boolean {
    return [...this.#states.keys()].some((id) => this.running(id));
  }

  async #start(
    client: AgentClient,
    id: string,
    req: RunRequest,
    run: { base: Turn[]; user?: UserTurn; exchange: number },
    agent: string,
    retried = false,
  ) {
    const ctl = new AbortController();
    this.#controllers.set(id, ctl);
    let answer = startAnswer(`a${Date.now()}`, Date.now());
    this.#stoppers.set(id, () => {
      // Closing the stream alone leaves the run going for a grace period.
      if (answer.runId) client.stopRun(answer.runId).catch(() => {});
    });
    this.#update(id, (s) => ({
      ...s,
      problem: undefined,
      live: { ...run, answer },
    }));
    let started = false;
    try {
      for await (const ev of client.run(req, { signal: ctl.signal })) {
        if (!started) {
          started = true;
          this.#began(id);
          this.#hooks.announce(`${agent} is answering`);
        }
        answer = applyEvent(answer, ev, Date.now());
        this.#announceStep(ev);
        if (ev.type === "text_delta") this.#later(id, answer);
        else this.#now(id, answer);
      }
    } catch (e) {
      if (ctl.signal.aborted) {
        answer = endAnswer(answer, "stopped", Date.now());
      } else if (!started && (e instanceof ApiError || e instanceof NetworkError)) {
        this.#controllers.delete(id);
        this.#stoppers.delete(id);
        this.#notSent(client, id, e, req.message ?? "", agent, retried);
        return;
      } else {
        answer = endAnswer(answer, "lost", Date.now());
      }
    } finally {
      if (this.#controllers.get(id) === ctl) {
        this.#controllers.delete(id);
        this.#stoppers.delete(id);
      }
    }
    this.#ended(id, answer, run.exchange);
  }

  /** The first event: the message is the server's now. */
  #began(id: string) {
    this.#update(id, (s) =>
      s.live?.user ? { ...s, live: { ...s.live, user: { ...s.live.user, pending: false } } } : s,
    );
    this.#list++;
    this.#emit();
  }

  #ended(id: string, answer: AssistantTurn, exchange: number) {
    this.#pending.delete(id);
    const { state, usage, turns, durationMs, error, runId } = answer;
    this.#update(id, (s) => {
      const meta = new Map(s.meta);
      meta.set(exchange, { state, usage, turns, durationMs, error, runId });
      return {
        ...s,
        live: s.live ? { ...s.live, user: s.live.user && { ...s.live.user, pending: false }, answer } : undefined,
        meta,
        version: s.version + 1,
        unread: this.#viewing !== id,
      };
    });
    this.#list++;
    this.#hooks.announce(
      state === "done"
        ? "Answer complete"
        : state === "stopped"
          ? "Stopped"
          : (error?.message ?? "The connection was lost before the answer finished."),
    );
    this.#emit();
  }

  /** The request failed before the run started: nothing was saved. */
  #notSent(client: AgentClient, id: string, e: ApiError | NetworkError, text: string, agent: string, retried: boolean) {
    if (e instanceof ApiError && e.status === 401) {
      this.#update(id, (s) => ({ ...s, live: undefined }));
      if (text) this.drafts.restore(id, text);
      this.#hooks.unauthorized();
      return;
    }
    if (e instanceof ApiError && e.status === 503 && text !== "") {
      // The message stays pending; one retry by itself, then "Still busy."
      this.#update(id, (s) => ({
        ...s,
        problem: { error: e, text, busy: retried ? "still" : "retrying" },
        live: s.live && { ...s.live, answer: { ...s.live.answer, state: "stopped" } },
      }));
      if (!retried) {
        this.#timers.set(
          id,
          setTimeout(() => {
            this.#timers.delete(id);
            this.retryNow(client, id, agent);
          }, busyRetryMs),
        );
      }
      return;
    }
    this.#update(id, (s) => ({ ...s, live: undefined, problem: { error: e, text } }));
    if (text) this.drafts.restore(id, text);
  }

  /** A busy message back into the composer. */
  #giveBack(id: string) {
    const s = this.get(id);
    const text = s.live?.user?.text ?? s.problem?.text ?? "";
    this.#update(id, (st) => ({ ...st, live: undefined, problem: undefined }));
    if (text) this.drafts.restore(id, text);
  }

  #announceStep(ev: RunEvent) {
    if (ev.type === "tool_result" && ev.is_error) this.#hooks.announce(`${ev.name ?? "A tool"} failed`);
  }

  /** Text deltas: shown once per frame, not per delta (the Markdown is parsed each time). */
  #later(id: string, answer: AssistantTurn) {
    this.#pending.set(id, answer);
    if (this.#frame !== undefined) return;
    this.#frame = requestAnimationFrame(() => {
      this.#frame = undefined;
      const pending = [...this.#pending];
      this.#pending.clear();
      for (const [cid, a] of pending) this.#setAnswer(cid, a);
      this.#emit();
    });
  }

  #now(id: string, answer: AssistantTurn) {
    this.#pending.delete(id);
    this.#setAnswer(id, answer);
    this.#emit();
  }

  #setAnswer(id: string, answer: AssistantTurn) {
    const s = this.#states.get(id);
    if (!s?.live) return;
    this.#states.set(id, { ...s, live: { ...s.live, answer } });
  }

  #update(id: string, fn: (s: ConversationState) => ConversationState) {
    this.#states.set(id, fn(this.get(id)));
    this.#emit();
  }

  #emit() {
    for (const fn of this.#listeners) fn();
  }
}
