// A small client for the public agent API (/v1), shared by every web app.
// It is the TypeScript counterpart of pkg/agentclient: the same endpoints,
// error body and stream rules (tasks/T053-agentclient.md, "For T055").

import { defaultMaxEventBytes, readSSE, ResponseTooLargeError } from "./sse";
import {
  decodeEvent,
  isTerminal,
  decodeConversation,
  type Agent,
  type AuthConfig,
  type Conversation,
  type ConversationSummary,
  type Me,
  type Run,
  type RunEvent,
  type RunRequest,
  type TaskRequest,
  type Thread,
  type ThreadMessage,
  type Trigger,
  type WireError,
} from "./types";

/** An HTTP error from the API: its status and the body's error. */
export class ApiError extends Error {
  readonly status: number;
  readonly code: string;
  readonly retryable: boolean;
  /** Seconds from Retry-After (503 over_capacity, 429), if the server sent it. */
  readonly retryAfter: number | undefined;

  constructor(status: number, error: WireError, retryAfter?: number) {
    super(error.message || `HTTP ${status}`);
    this.name = "ApiError";
    this.status = status;
    this.code = error.code;
    this.retryable = error.retryable === true;
    this.retryAfter = retryAfter;
  }
}

/** The response was not what the API sends (not JSON, not an event, wrong type). */
export class BadResponseError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "BadResponseError";
  }
}

/** The server couldn't be reached, or the connection broke while reading a response. */
export class NetworkError extends Error {
  constructor(options?: ErrorOptions) {
    super("couldn't reach the server", options);
    this.name = "NetworkError";
  }
}

/** The stream ended before run_done or run_error: the connection was lost. */
export class StreamTruncatedError extends Error {
  constructor(options?: ErrorOptions) {
    super("the run's stream ended before the run did", options);
    this.name = "StreamTruncatedError";
  }
}

export type ClientOptions = {
  /** Where /v1 is; "" (the default) is the page's own origin. */
  baseUrl?: string;
  /** The bearer token for each request, read at request time; undefined sends none. */
  token?: () => string | undefined;
  /** For tests. */
  fetch?: typeof fetch;
  /** The limit on one event of a stream (see readSSE). */
  maxEventBytes?: number;
};

type RequestOptions = { signal?: AbortSignal | undefined };

/** The header a session's requests other than GET carry its CSRF token in. */
export const csrfHeader = "X-Agentkit-CSRF";

const safeMethods = ["GET", "HEAD", "OPTIONS"];

export class AgentClient {
  readonly #base: string;
  readonly #token: () => string | undefined;
  readonly #fetch: typeof fetch;
  readonly #maxEvent: number;
  /** The session's CSRF token, from the last GET /v1/auth/me. */
  #csrf: string | undefined;

  constructor(opts: ClientOptions = {}) {
    this.#base = (opts.baseUrl ?? "").replace(/\/+$/, "");
    this.#token = opts.token ?? (() => undefined);
    // Bound, because calling a detached window.fetch throws "Illegal invocation".
    this.#fetch = opts.fetch ?? globalThis.fetch.bind(globalThis);
    this.#maxEvent = opts.maxEventBytes ?? defaultMaxEventBytes;
  }

  /** GET /v1/auth/config (no credentials needed): the sign-in methods, "password" and "token". */
  async authConfig(opts: RequestOptions = {}): Promise<AuthConfig> {
    const body = await this.getJSON<unknown>("/v1/auth/config", opts);
    const methods = (body as { methods?: unknown } | null)?.methods;
    if (!Array.isArray(methods) || !methods.every((m) => typeof m === "string")) {
      throw new BadResponseError("/v1/auth/config: the response has no list of methods");
    }
    return { methods };
  }

  /**
   * GET /v1/auth/me: who the caller is. A session's answer carries its CSRF
   * token, which the client keeps and sends on later requests other than
   * GET (and fetches again once if the server refuses it).
   */
  async me(opts: RequestOptions = {}): Promise<Me> {
    const m = await this.getJSON<Partial<Me> | null>("/v1/auth/me", opts);
    if (typeof m?.tenant !== "string" || !Array.isArray(m.roles) || !Array.isArray(m.scopes)) {
      throw new BadResponseError("/v1/auth/me: the response is not who the caller is");
    }
    this.#csrf = typeof m.csrf === "string" && m.csrf !== "" ? m.csrf : undefined;
    return m as Me;
  }

  /**
   * POST /v1/auth/login: signs in; the server sets the session cookie. 401 is
   * a wrong username or password, 429 too many attempts (retryAfter).
   */
  async login(username: string, password: string, opts: RequestOptions = {}): Promise<void> {
    const body = JSON.stringify({ username, password });
    const resp = await this.#send("POST", "/v1/auth/login", body, "application/json", opts.signal);
    await resp.body?.cancel();
  }

  /** POST /v1/auth/logout: ends the session on the server. The CSRF token is forgotten either way. */
  async logout(opts: RequestOptions = {}): Promise<void> {
    try {
      const resp = await this.#send("POST", "/v1/auth/logout", undefined, "application/json", opts.signal);
      await resp.body?.cancel();
    } finally {
      this.#csrf = undefined;
    }
  }

  /** GET /v1/agents: the agents the caller can use, the default first. */
  async listAgents(opts: RequestOptions = {}): Promise<Agent[]> {
    return listField<Agent>(await this.getJSON<unknown>("/v1/agents", opts), "agents", "/v1/agents");
  }

  /** GET /v1/conversations: one page of the caller's conversations, newest first. */
  async listConversations(
    query: { agent?: string; limit?: number; cursor?: string } = {},
    opts: RequestOptions = {},
  ): Promise<{ conversations: ConversationSummary[]; nextCursor: string | undefined }> {
    const params = new URLSearchParams();
    if (query.agent) params.set("agent", query.agent);
    if (query.limit) params.set("limit", String(query.limit));
    if (query.cursor) params.set("cursor", query.cursor);
    const qs = params.size > 0 ? `?${params}` : "";
    const body = await this.getJSON<unknown>(`/v1/conversations${qs}`, opts);
    return {
      conversations: listField<ConversationSummary>(body, "conversations", "/v1/conversations"),
      nextCursor: cursorOf(body),
    };
  }

  /** GET /v1/conversations/{id}: the conversation with its messages. */
  async getConversation(id: string, opts: RequestOptions = {}): Promise<Conversation> {
    const path = `/v1/conversations/${encodeURIComponent(id)}`;
    const c = decodeConversation(await this.getJSON<unknown>(path, opts));
    if (!c) throw new BadResponseError(`${path}: the response is not a conversation`);
    return c;
  }

  /**
   * POST /v1/conversations: an empty conversation with the agent (the
   * default when empty). The first message is then a run in it (T092).
   */
  async createConversation(
    agent: string | undefined,
    opts: RequestOptions = {},
  ): Promise<{ id: string; agent: string }> {
    const body = agent ? JSON.stringify({ agent }) : "{}";
    const resp = await this.#send("POST", "/v1/conversations", body, "application/json", opts.signal);
    const c = (await this.#json(resp, "/v1/conversations")) as { id?: unknown; agent?: unknown } | null;
    if (typeof c?.id !== "string" || c.id === "") {
      throw new BadResponseError("/v1/conversations: the response has no conversation ID");
    }
    return { id: c.id, agent: typeof c.agent === "string" ? c.agent : (agent ?? "") };
  }

  /** DELETE /v1/conversations/{id}: 409 conversation_busy while a run is going on. */
  async deleteConversation(id: string, opts: RequestOptions = {}): Promise<void> {
    const resp = await this.#send(
      "DELETE",
      `/v1/conversations/${encodeURIComponent(id)}`,
      undefined,
      "application/json",
      opts.signal,
    );
    await resp.body?.cancel();
  }

  /** GET /v1/runs/{id}. */
  async getRun(id: string, opts: RequestOptions = {}): Promise<Run> {
    return this.getJSON<Run>(`/v1/runs/${encodeURIComponent(id)}`, opts);
  }

  /**
   * POST /v1/runs/{id}/stop (T037): interrupts the run at once, keeping its
   * completed turns; POST /v1/runs/{id}/resume continues it. A run that has
   * already ended is left as it is.
   */
  async stopRun(id: string, opts: RequestOptions = {}): Promise<void> {
    const resp = await this.#send(
      "POST",
      `/v1/runs/${encodeURIComponent(id)}/stop`,
      undefined,
      "application/json",
      opts.signal,
    );
    await resp.body?.cancel();
  }

  /** GET a JSON resource under /v1. */
  async getJSON<T>(path: string, opts: RequestOptions = {}): Promise<T> {
    const resp = await this.#send("GET", path, undefined, "application/json", opts.signal);
    return (await this.#json(resp, path)) as T;
  }

  async #json(resp: Response, path: string): Promise<unknown> {
    try {
      return await resp.json();
    } catch (e) {
      if (isAbort(e)) throw e;
      // A SyntaxError is the body; anything else is reading it (a reset).
      if (e instanceof SyntaxError) throw new BadResponseError(`${path}: the response is not JSON`);
      throw new NetworkError({ cause: e });
    }
  }

  /**
   * POST /v1/runs, streamed: yields each event until run_done or run_error,
   * which ends the iteration (events after it are not read). Unknown event
   * types are yielded like any other; the caller skips what it doesn't know.
   *
   * Aborting the signal only closes the stream: since T037 the server lets
   * a run nobody follows go on for a grace period, then interrupts it. To
   * stop the run, call stopRun as well. A body that ends
   * or breaks (a reset connection) before the end event throws
   * StreamTruncatedError, as pkg/agentclient does.
   */
  async *run(req: RunRequest, opts: RequestOptions = {}): AsyncGenerator<RunEvent, void, undefined> {
    const resp = await this.#send("POST", "/v1/runs", JSON.stringify(req), "text/event-stream", opts.signal);
    const ended = yield* this.#events(resp, "/v1/runs", opts.signal);
    if (!ended) throw new StreamTruncatedError();
  }

  /**
   * GET /v1/runs/{id}/events: the run's events from the one after `after`
   * (an event's id, sent as Last-Event-ID; from the first without it), then
   * the new ones while the server runs it, until run_done or run_error. The
   * server ends the stream early when it doesn't run the run (it replays
   * what is stored): then the iteration returns false, and the caller
   * follows again later from the last id it has. It returns true after the
   * end event. A body that breaks throws StreamTruncatedError.
   */
  async *events(
    id: string,
    opts: RequestOptions & { after?: string | undefined } = {},
  ): AsyncGenerator<RunEvent, boolean, undefined> {
    const path = `/v1/runs/${encodeURIComponent(id)}/events`;
    const headers = opts.after ? { "Last-Event-ID": opts.after } : undefined;
    const resp = await this.#send("GET", path, undefined, "text/event-stream", opts.signal, false, headers);
    return yield* this.#events(resp, path, opts.signal);
  }

  /** Reads an event stream to its end event (true) or the end of the body (false). */
  async *#events(
    resp: Response,
    path: string,
    signal: AbortSignal | undefined,
  ): AsyncGenerator<RunEvent, boolean, undefined> {
    const type = resp.headers.get("Content-Type") ?? "";
    if (!type.toLowerCase().startsWith("text/event-stream")) {
      throw new BadResponseError(`${path}: the response is ${type || "untyped"}, not an event stream`);
    }
    if (!resp.body) return false;
    const events = readSSE(resp.body, this.#maxEvent);
    try {
      for (;;) {
        let next: IteratorResult<string>;
        try {
          next = await events.next();
        } catch (e) {
          // Stop stays an abort; a limit stays itself; a broken body is truncation.
          if (isAbort(e) || signal?.aborted || e instanceof ResponseTooLargeError) throw e;
          throw new StreamTruncatedError({ cause: e });
        }
        if (next.done) return false;
        const ev = decodeEvent(next.value);
        if (!ev) throw new BadResponseError(`${path}: an event is not JSON with a type`);
        yield ev;
        if (isTerminal(ev)) return true;
      }
    } finally {
      // Cancels the download when the run ends or the caller stops reading.
      await events.return(undefined);
    }
  }

  /**
   * GET /v1/runs: one page of the tenant's runs, newest first; with
   * background true only tasks (background runs), with false only chats.
   */
  async listRuns(
    query: { status?: string; background?: boolean; agent?: string; limit?: number; cursor?: string } = {},
    opts: RequestOptions = {},
  ): Promise<{ runs: Run[]; nextCursor: string | undefined }> {
    const params = new URLSearchParams();
    if (query.status) params.set("status", query.status);
    if (query.background !== undefined) params.set("background", String(query.background));
    if (query.agent) params.set("agent", query.agent);
    if (query.limit) params.set("limit", String(query.limit));
    if (query.cursor) params.set("cursor", query.cursor);
    const qs = params.size > 0 ? `?${params}` : "";
    const body = await this.getJSON<unknown>(`/v1/runs${qs}`, opts);
    return { runs: listField<Run>(body, "runs", "/v1/runs"), nextCursor: cursorOf(body) };
  }

  /** POST /v1/runs/{id}/cancel: ends the run for good; answers with the run once it has ended here. */
  async cancelRun(id: string, opts: RequestOptions = {}): Promise<Run> {
    const path = `/v1/runs/${encodeURIComponent(id)}/cancel`;
    const resp = await this.#send("POST", path, undefined, "application/json", opts.signal);
    return (await this.#json(resp, path)) as Run;
  }

  /** POST /v1/tasks: starts a background run, answered at once (202) with the run. */
  async startTask(req: TaskRequest, opts: RequestOptions = {}): Promise<Run> {
    const resp = await this.#send("POST", "/v1/tasks", JSON.stringify(req), "application/json", opts.signal);
    const run = (await this.#json(resp, "/v1/tasks")) as Partial<Run> | null;
    if (typeof run?.id !== "string" || run.id === "") throw new BadResponseError("/v1/tasks: the response has no run");
    return run as Run;
  }

  /** GET /v1/triggers (agent:admin): the tenant's triggers, sorted by ID, without their webhooks' secrets. */
  async listTriggers(opts: RequestOptions = {}): Promise<Trigger[]> {
    return listField<Trigger>(await this.getJSON<unknown>("/v1/triggers", opts), "triggers", "/v1/triggers");
  }

  /** POST /v1/triggers/{id}/pause or /resume (agent:admin): the trigger as it is now. */
  async setTriggerPaused(id: string, paused: boolean, opts: RequestOptions = {}): Promise<Trigger> {
    const path = `/v1/triggers/${encodeURIComponent(id)}/${paused ? "pause" : "resume"}`;
    const resp = await this.#send("POST", path, undefined, "application/json", opts.signal);
    return (await this.#json(resp, path)) as Trigger;
  }

  /** GET /v1/threads: one page of the tenant's threads between agents, newest first, without messages. */
  async listThreads(
    query: { limit?: number; cursor?: string } = {},
    opts: RequestOptions = {},
  ): Promise<{ threads: Thread[]; nextCursor: string | undefined }> {
    const params = new URLSearchParams();
    if (query.limit) params.set("limit", String(query.limit));
    if (query.cursor) params.set("cursor", query.cursor);
    const qs = params.size > 0 ? `?${params}` : "";
    const body = await this.getJSON<unknown>(`/v1/threads${qs}`, opts);
    return { threads: listField<Thread>(body, "threads", "/v1/threads"), nextCursor: cursorOf(body) };
  }

  /** GET /v1/threads/{id}: the thread with its messages in order. */
  async getThread(id: string, opts: RequestOptions = {}): Promise<Thread> {
    const path = `/v1/threads/${encodeURIComponent(id)}`;
    const th = await this.getJSON<Partial<Thread> | null>(path, opts);
    if (typeof th?.id !== "string") throw new BadResponseError(`${path}: the response is not a thread`);
    return { ...th, messages: listField<ThreadMessage>(th, "messages", path) } as Thread;
  }

  async #send(
    method: string,
    path: string,
    body: string | undefined,
    accept: string,
    signal: AbortSignal | undefined,
    retried = false,
    extra?: Record<string, string>,
  ): Promise<Response> {
    const headers = new Headers({ ...extra, Accept: accept });
    if (body !== undefined) headers.set("Content-Type", "application/json");
    const token = this.#token();
    if (token) headers.set("Authorization", `Bearer ${token}`);
    const unsafe = !safeMethods.includes(method);
    if (unsafe && this.#csrf) headers.set(csrfHeader, this.#csrf);
    let resp: Response;
    try {
      resp = await this.#fetch(this.#base + path, {
        method,
        headers,
        body: body ?? null,
        signal: signal ?? null,
        credentials: "same-origin",
        cache: "no-store",
      });
    } catch (e) {
      if (isAbort(e)) throw e;
      throw new NetworkError({ cause: e });
    }
    if (!resp.ok) {
      const err = await errorFrom(resp);
      // A stale or missing CSRF token: read /me for the session's, and try once more.
      // A sign-in sends no token, so its refusal (another Origin) is final.
      if (err.status === 403 && err.code === "csrf_failed" && unsafe && !retried && path !== "/v1/auth/login") {
        await this.me({ signal });
        if (this.#csrf) return this.#send(method, path, body, accept, signal, true, extra);
      }
      throw err;
    }
    return resp;
  }
}

/** The ApiError of a failed response: {"v":1,"error":{code,message,retryable}}. */
async function errorFrom(resp: Response): Promise<ApiError> {
  let error: WireError = { code: `http_${resp.status}`, message: resp.statusText || `HTTP ${resp.status}` };
  try {
    const body: unknown = await resp.json();
    const e = (body as { error?: unknown } | null)?.error as Partial<WireError> | undefined;
    if (e && typeof e.code === "string") {
      error = {
        code: e.code,
        message: typeof e.message === "string" ? e.message : error.message,
        retryable: e.retryable === true,
      };
    }
  } catch (e) {
    if (isAbort(e)) throw e;
    // Not JSON (a proxy's page): keep the status.
  }
  const ra = Number(resp.headers.get("Retry-After"));
  return new ApiError(resp.status, error, Number.isFinite(ra) && ra > 0 ? ra : undefined);
}

// By name: an abort is a DOMException, which isn't an Error in every realm.
function isAbort(e: unknown): boolean {
  return typeof e === "object" && e !== null && (e as { name?: unknown }).name === "AbortError";
}

/** A page's next_cursor; undefined on the last page. */
function cursorOf(body: unknown): string | undefined {
  const cursor = (body as { next_cursor?: unknown } | null)?.next_cursor;
  return typeof cursor === "string" && cursor !== "" ? cursor : undefined;
}

/**
 * The list under key in a JSON body. null or missing is empty (Go encodes a
 * nil slice as null); anything else that isn't a list is a bad response,
 * not an empty one, so a broken proxy isn't shown as "nothing configured".
 */
export function listField<T>(body: unknown, key: string, path: string): T[] {
  if (typeof body !== "object" || body === null || Array.isArray(body)) {
    throw new BadResponseError(`${path}: the response is not an object`);
  }
  const v = (body as Record<string, unknown>)[key];
  if (v === undefined || v === null) return [];
  if (!Array.isArray(v)) throw new BadResponseError(`${path}: "${key}" is not a list`);
  return v as T[];
}
