// A small client for the public agent API (/v1), shared by every web app.
// It is the TypeScript counterpart of pkg/agentclient: the same endpoints,
// error body and stream rules (tasks/T053-agentclient.md, "For T055").

import { defaultMaxEventBytes, readSSE } from "./sse";
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
  constructor() {
    super("the run's stream ended before the run did");
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
    const body = await this.getJSON<{ agents?: Agent[] }>("/v1/agents", opts);
    return Array.isArray(body.agents) ? body.agents : [];
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
    const body = await this.getJSON<{ conversations?: ConversationSummary[]; next_cursor?: string }>(
      `/v1/conversations${qs}`,
      opts,
    );
    return {
      conversations: Array.isArray(body.conversations) ? body.conversations : [],
      nextCursor: body.next_cursor || undefined,
    };
  }

  /** GET a JSON resource under /v1. */
  async getJSON<T>(path: string, opts: RequestOptions = {}): Promise<T> {
    const resp = await this.#send("GET", path, undefined, "application/json", opts.signal);
    try {
      return (await resp.json()) as T;
    } catch {
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
   * before the end event throws StreamTruncatedError.
   */
  async *run(req: RunRequest, opts: RequestOptions = {}): AsyncGenerator<RunEvent, void, undefined> {
    const resp = await this.#send("POST", "/v1/runs", JSON.stringify(req), "text/event-stream", opts.signal);
    const type = resp.headers.get("Content-Type") ?? "";
    if (!type.toLowerCase().startsWith("text/event-stream")) {
      throw new BadResponseError(`/v1/runs: the response is ${type || "untyped"}, not an event stream`);
    }
    if (!resp.body) throw new StreamTruncatedError();
    for await (const data of readSSE(resp.body, this.#maxEvent)) {
      const ev = decodeEvent(data);
      if (!ev) throw new BadResponseError("/v1/runs: an event is not JSON with a type");
      yield ev;
      if (isTerminal(ev)) return;
    }
    throw new StreamTruncatedError();
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
  } catch {
    // Not JSON (a proxy's page): keep the status.
  }
  const ra = Number(resp.headers.get("Retry-After"));
  return new ApiError(resp.status, error, Number.isFinite(ra) && ra > 0 ? ra : undefined);
}
