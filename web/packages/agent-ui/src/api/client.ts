// A small client for the public agent API (/v1), shared by every web app.
// It is the TypeScript counterpart of pkg/agentclient: the same endpoints,
// error body and stream rules (tasks/T053-agentclient.md, "For T055").

import { defaultMaxEventBytes, readSSE, ResponseTooLargeError } from "./sse";
import {
  decodeEvent,
  isTerminal,
  type Agent,
  type ConversationSummary,
  type RunEvent,
  type RunRequest,
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

export class AgentClient {
  readonly #base: string;
  readonly #token: () => string | undefined;
  readonly #fetch: typeof fetch;
  readonly #maxEvent: number;

  constructor(opts: ClientOptions = {}) {
    this.#base = (opts.baseUrl ?? "").replace(/\/+$/, "");
    this.#token = opts.token ?? (() => undefined);
    // Bound, because calling a detached window.fetch throws "Illegal invocation".
    this.#fetch = opts.fetch ?? globalThis.fetch.bind(globalThis);
    this.#maxEvent = opts.maxEventBytes ?? defaultMaxEventBytes;
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
    const cursor = (body as { next_cursor?: unknown } | null)?.next_cursor;
    return {
      conversations: listField<ConversationSummary>(body, "conversations", "/v1/conversations"),
      nextCursor: typeof cursor === "string" && cursor !== "" ? cursor : undefined,
    };
  }

  /** GET a JSON resource under /v1. */
  async getJSON<T>(path: string, opts: RequestOptions = {}): Promise<T> {
    const resp = await this.#send("GET", path, undefined, "application/json", opts.signal);
    try {
      return (await resp.json()) as T;
    } catch (e) {
      if (isAbort(e)) throw e;
      throw new BadResponseError(`${path}: the response is not JSON`);
    }
  }

  /**
   * POST /v1/runs, streamed: yields each event until run_done or run_error,
   * which ends the iteration (events after it are not read). Unknown event
   * types are yielded like any other; the caller skips what it doesn't know.
   *
   * Abort the signal to stop the run: the server sees the request end and
   * cancels it (the conversation may be left interrupted). A body that ends
   * or breaks (a reset connection) before the end event throws
   * StreamTruncatedError, as pkg/agentclient does.
   */
  async *run(req: RunRequest, opts: RequestOptions = {}): AsyncGenerator<RunEvent, void, undefined> {
    const resp = await this.#send("POST", "/v1/runs", JSON.stringify(req), "text/event-stream", opts.signal);
    const type = resp.headers.get("Content-Type") ?? "";
    if (!type.toLowerCase().startsWith("text/event-stream")) {
      throw new BadResponseError(`/v1/runs: the response is ${type || "untyped"}, not an event stream`);
    }
    if (!resp.body) throw new StreamTruncatedError();
    const events = readSSE(resp.body, this.#maxEvent);
    try {
      for (;;) {
        let next: IteratorResult<string>;
        try {
          next = await events.next();
        } catch (e) {
          // Stop stays an abort; a limit stays itself; a broken body is truncation.
          if (isAbort(e) || opts.signal?.aborted || e instanceof ResponseTooLargeError) throw e;
          throw new StreamTruncatedError({ cause: e });
        }
        if (next.done) throw new StreamTruncatedError();
        const ev = decodeEvent(next.value);
        if (!ev) throw new BadResponseError("/v1/runs: an event is not JSON with a type");
        yield ev;
        if (isTerminal(ev)) return;
      }
    } finally {
      // Cancels the download when the run ends or the caller stops reading.
      await events.return(undefined);
    }
  }

  async #send(
    method: string,
    path: string,
    body: string | undefined,
    accept: string,
    signal: AbortSignal | undefined,
  ): Promise<Response> {
    const headers = new Headers({ Accept: accept });
    if (body !== undefined) headers.set("Content-Type", "application/json");
    const token = this.#token();
    if (token) headers.set("Authorization", `Bearer ${token}`);
    const resp = await this.#fetch(this.#base + path, {
      method,
      headers,
      body: body ?? null,
      signal: signal ?? null,
      credentials: "same-origin",
      cache: "no-store",
    });
    if (!resp.ok) throw await errorFrom(resp);
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

/**
 * The list under key in a JSON body. null or missing is empty (Go encodes a
 * nil slice as null); anything else that isn't a list is a bad response,
 * not an empty one, so a broken proxy isn't shown as "nothing configured".
 */
function listField<T>(body: unknown, key: string, path: string): T[] {
  if (typeof body !== "object" || body === null || Array.isArray(body)) {
    throw new BadResponseError(`${path}: the response is not an object`);
  }
  const v = (body as Record<string, unknown>)[key];
  if (v === undefined || v === null) return [];
  if (!Array.isArray(v)) throw new BadResponseError(`${path}: "${key}" is not a list`);
  return v as T[];
}
