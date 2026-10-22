import { describe, expect, it } from "vitest";
import {
  AgentClient,
  ApiError,
  BadResponseError,
  NetworkError,
  ResponseTooLargeError,
  SSEReader,
  StreamTruncatedError,
  csrfHeader,
  decodeMessage,
  type RunEvent,
} from "./index";

// The cases of pkg/agentclient's TestSSEFraming: the same bodies must give
// the same events, then the same end.
const start = `{"v":1,"type":"run_start","seq":0,"run_id":"r","conversation_id":"c"}`;
const done = `{"v":1,"type":"run_done","seq":1,"run_id":"r","answer":"a"}`;

type End = "done" | typeof StreamTruncatedError | typeof BadResponseError;

const framing: { name: string; body: string; want: string[]; end: End }[] = [
  {
    name: "LF",
    body: `id: 0\nevent: run_start\ndata: ${start}\n\nid: 1\nevent: run_done\ndata: ${done}\n\n`,
    want: [start, done],
    end: "done",
  },
  {
    name: "CRLF",
    body: `id: 0\r\nevent: run_start\r\ndata: ${start}\r\n\r\ndata: ${done}\r\n\r\n`,
    want: [start, done],
    end: "done",
  },
  { name: "CR", body: `data: ${start}\r\rdata: ${done}\r\r`, want: [start, done], end: "done" },
  { name: "mixed line ends", body: `data: ${start}\r\n\ndata: ${done}\r\r\n`, want: [start, done], end: "done" },
  {
    name: "data over several lines",
    body: `data: {"v":1,\ndata:  "type":"run_start","seq":0,\ndata: "run_id":"r"}\n\ndata: ${done}\n\n`,
    want: [`{"v":1,\n "type":"run_start","seq":0,\n"run_id":"r"}`, done],
    end: "done",
  },
  {
    name: "data over several lines, CRLF",
    body: `data: {"v":1,\r\ndata: "type":"run_start"}\r\n\r\ndata: ${done}\r\n\r\n`,
    want: [`{"v":1,\n"type":"run_start"}`, done],
    end: "done",
  },
  {
    name: "comments",
    body: `: hello\n\ndata: ${start}\n: keep-alive\n\n: keep-alive\n\ndata: ${done}\n:\n\n`,
    want: [start, done],
    end: "done",
  },
  { name: "no space after the colon", body: `data:${start}\n\ndata:${done}\n\n`, want: [start, done], end: "done" },
  { name: "no id or event", body: `data: ${start}\n\ndata: ${done}\n\n`, want: [start, done], end: "done" },
  { name: "byte order mark", body: `\uFEFFdata: ${start}\n\ndata: ${done}\n\n`, want: [start, done], end: "done" },
  { name: "blank lines", body: `\n\n\ndata: ${start}\n\n\n\ndata: ${done}\n\n`, want: [start, done], end: "done" },
  {
    name: "unknown fields",
    body: `retry: 10\nfoo: bar\nevent: something_else\ndata: ${start}\nid\n\ndata: ${done}\n\n`,
    want: [start, done],
    end: "done",
  },
  {
    name: "event without its blank line",
    body: `data: ${start}\n\ndata: ${done}\n`,
    want: [start],
    end: StreamTruncatedError,
  },
  { name: "line cut off", body: `data: ${start}\n\ndata: ${done}`, want: [start], end: StreamTruncatedError },
  { name: "nothing", body: "", want: [], end: StreamTruncatedError },
  { name: "only comments", body: ": keep-alive\n\n", want: [], end: StreamTruncatedError },
  { name: "data that is not JSON", body: `data: ${start}\n\ndata: hello\n\n`, want: [start], end: BadResponseError },
  { name: "an event without a type", body: `data: {"v":1,"seq":0}\n\n`, want: [], end: BadResponseError },
  { name: "null", body: "data: null\n\n", want: [], end: BadResponseError },
  { name: "events after the end are not read", body: `data: ${done}\n\ndata: ${start}\n\n`, want: [done], end: "done" },
];

/** A fetch that answers every request with body, sent in the given chunks. */
function fakeFetch(chunks: string[], init: ResponseInit = {}, seen?: Request[]): typeof fetch {
  return (async (input: RequestInfo | URL, reqInit?: RequestInit) => {
    seen?.push(new Request(new URL(String(input), "http://test.invalid"), reqInit));
    const enc = new TextEncoder();
    const body = new ReadableStream<Uint8Array>({
      start(c) {
        for (const ch of chunks) c.enqueue(enc.encode(ch));
        c.close();
      },
    });
    return new Response(body, { status: 200, headers: { "Content-Type": "text/event-stream" }, ...init });
  }) as typeof fetch;
}

async function readRun(client: AgentClient): Promise<{ raw: RunEvent[]; end: unknown }> {
  const raw: RunEvent[] = [];
  try {
    for await (const ev of client.run({ message: "hi" })) raw.push(ev);
    return { raw, end: "done" };
  } catch (err) {
    return { raw, end: err };
  }
}

function sameEnd(got: unknown, want: End) {
  if (want === "done") expect(got).toBe("done");
  else expect(got).toBeInstanceOf(want);
}

describe("run stream framing (pkg/agentclient TestSSEFraming)", () => {
  for (const tt of framing) {
    it(tt.name, async () => {
      const got = await readRun(new AgentClient({ fetch: fakeFetch([tt.body]) }));
      expect(got.raw).toEqual(tt.want.map((d) => JSON.parse(d)));
      sameEnd(got.end, tt.end);
    });

    // The same body cut at every place: a CRLF split across reads must read
    // the same (UTF-8 split inside a character: "bytes" below).
    it(`${tt.name}, split anywhere`, async () => {
      for (let i = 1; i < tt.body.length; i++) {
        const chunks = [tt.body.slice(0, i), tt.body.slice(i)];
        const got = await readRun(new AgentClient({ fetch: fakeFetch(chunks) }));
        expect(got.raw, `split at ${i}`).toEqual(tt.want.map((d) => JSON.parse(d)));
        sameEnd(got.end, tt.end);
      }
    });
  }

  it("reads one byte at a time", async () => {
    const body = `data: ${start}\r\n\r\ndata: ${done}\r\n\r\n`;
    const got = await readRun(new AgentClient({ fetch: fakeFetch([...body]) }));
    expect(got.raw.map((e) => e.type)).toEqual(["run_start", "run_done"]);
  });
});

describe("run stream limits (pkg/agentclient TestLineLimit)", () => {
  const head = `data: ${start}\n\n`;
  for (const [name, tail] of [
    ["a line with no end", "x"],
    ["data over many lines", "data: xxxxxxxx\n"],
    ["a comment with no end", ":"],
  ] as const) {
    it(name, async () => {
      const chunks = [head, ...Array.from({ length: 300 }, () => tail.repeat(10))];
      const got = await readRun(new AgentClient({ fetch: fakeFetch(chunks), maxEventBytes: 1000 }));
      expect(got.raw.map((e) => e.type)).toEqual(["run_start"]);
      expect(got.end).toBeInstanceOf(ResponseTooLargeError);
    });
  }
});

describe("run events", () => {
  it("passes on unknown event types and doesn't stop", async () => {
    const future = `{"v":1,"type":"thinking_delta","seq":1,"run_id":"r","text":"hm"}`;
    const end = `{"v":1,"type":"run_done","seq":2,"run_id":"r","answer":"a"}`;
    const got = await readRun(
      new AgentClient({ fetch: fakeFetch([`data: ${start}\n\ndata: ${future}\n\ndata: ${end}\n\n`]) }),
    );
    expect(got.raw.map((e) => e.type)).toEqual(["run_start", "thinking_delta", "run_done"]);
    expect(got.end).toBe("done");
  });

  it("decodes the final message leniently, keeping unknown blocks", async () => {
    const end = JSON.stringify({
      v: 1,
      type: "run_done",
      seq: 1,
      run_id: "r",
      future_field: true,
      final: {
        role: "assistant",
        content: [
          { type: "text", text: "hi" },
          { type: "hologram", data: 1 },
        ],
      },
    });
    const got = await readRun(new AgentClient({ fetch: fakeFetch([`data: ${end}\n\n`]) }));
    expect(got.raw[0]?.final?.content).toEqual([
      { type: "text", text: "hi" },
      { type: "hologram", unknown: true, raw: { type: "hologram", data: 1 } },
    ]);
  });

  it("ends on run_error", async () => {
    const err = `{"v":1,"type":"run_error","seq":1,"run_id":"r","error":{"code":"timeout","message":"slow"}}`;
    const got = await readRun(new AgentClient({ fetch: fakeFetch([`data: ${start}\n\ndata: ${err}\n\n`]) }));
    expect(got.raw.at(-1)?.error?.code).toBe("timeout");
    expect(got.end).toBe("done");
  });

  it("refuses a response that isn't an event stream", async () => {
    const got = await readRun(
      new AgentClient({ fetch: fakeFetch(["{}"], { headers: { "Content-Type": "application/json" } }) }),
    );
    expect(got.end).toBeInstanceOf(BadResponseError);
  });

  it("stops the request when the signal aborts", async () => {
    let aborted = false;
    const fetchHanging = (async (_: RequestInfo | URL, init?: RequestInit) => {
      const body = new ReadableStream<Uint8Array>({
        start(c) {
          c.enqueue(new TextEncoder().encode(`data: ${start}\n\n`));
          init?.signal?.addEventListener("abort", () => {
            aborted = true;
            c.error(new DOMException("aborted", "AbortError"));
          });
        },
      });
      return new Response(body, { headers: { "Content-Type": "text/event-stream" } });
    }) as typeof fetch;
    const ctl = new AbortController();
    const seen: string[] = [];
    await expect(
      (async () => {
        for await (const ev of new AgentClient({ fetch: fetchHanging }).run(
          { message: "hi" },
          { signal: ctl.signal },
        )) {
          seen.push(ev.type);
          ctl.abort();
        }
      })(),
    ).rejects.toThrow(/abort/i);
    expect(seen).toEqual(["run_start"]);
    expect(aborted).toBe(true);
  });
});

describe("requests", () => {
  it("sends the bearer token and the run as JSON", async () => {
    const seen: Request[] = [];
    const client = new AgentClient({ fetch: fakeFetch([`data: ${done}\n\n`], {}, seen), token: () => "tok-1" });
    await readRun(client);
    const req = seen[0]!;
    expect(req.method).toBe("POST");
    expect(new URL(req.url).pathname).toBe("/v1/runs");
    expect(req.headers.get("Authorization")).toBe("Bearer tok-1");
    expect(req.headers.get("Accept")).toBe("text/event-stream");
    expect(await req.json()).toEqual({ message: "hi" });
  });

  it("stops a run with POST /v1/runs/{id}/stop", async () => {
    const seen: Request[] = [];
    const client = new AgentClient({
      fetch: fakeFetch(
        ['{"v":1,"id":"run/1","status":"interrupted"}'],
        { headers: { "Content-Type": "application/json" } },
        seen,
      ),
      token: () => "tok-1",
    });
    await client.stopRun("run/1");
    expect(seen[0]!.method).toBe("POST");
    expect(new URL(seen[0]!.url).pathname).toBe("/v1/runs/run%2F1/stop");
    expect(seen[0]!.headers.get("Authorization")).toBe("Bearer tok-1");
  });

  it("sends no Authorization without a token", async () => {
    const seen: Request[] = [];
    const client = new AgentClient({
      fetch: fakeFetch(['{"v":1,"agents":[]}'], { headers: { "Content-Type": "application/json" } }, seen),
    });
    expect(await client.listAgents()).toEqual([]);
    expect(seen[0]!.headers.has("Authorization")).toBe(false);
  });

  it("turns an error body into ApiError, with Retry-After", async () => {
    const client = new AgentClient({
      fetch: fakeFetch(['{"v":1,"error":{"code":"over_capacity","message":"busy","retryable":true}}'], {
        status: 503,
        headers: { "Content-Type": "application/json", "Retry-After": "2" },
      }),
    });
    const err = await client.listAgents().catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect(err).toMatchObject({ status: 503, code: "over_capacity", message: "busy", retryable: true, retryAfter: 2 });
  });

  it("keeps the status of an error that isn't JSON", async () => {
    const client = new AgentClient({ fetch: fakeFetch(["<html>bad gateway</html>"], { status: 502 }) });
    const err = await client.listAgents().catch((e: unknown) => e);
    expect(err).toMatchObject({ status: 502, code: "http_502" });
  });
});

/**
 * A server with a session: GET /v1/auth/me hands out the CSRF token
 * server.csrf, and every other request needs it in X-Agentkit-CSRF unless it
 * is a GET.
 */
function sessionServer(server: { csrf: string }, seen: Request[]): typeof fetch {
  return (async (input: RequestInfo | URL, init?: RequestInit) => {
    const req = new Request(new URL(String(input), "http://test.invalid"), init);
    seen.push(req);
    const path = new URL(req.url).pathname;
    if (path === "/v1/auth/me") {
      return Response.json({ v: 1, username: "ann", tenant: "acme", roles: ["user"], scopes: [], csrf: server.csrf });
    }
    // A sign-in needs no token, as on the server (only a same-site Origin).
    if (path !== "/v1/auth/login" && req.method !== "GET" && req.headers.get(csrfHeader) !== server.csrf) {
      return Response.json({ v: 1, error: { code: "csrf_failed", message: "no" } }, { status: 403 });
    }
    if (path === "/v1/auth/login") return new Response(null, { status: 204 });
    if (path === "/v1/conversations") return Response.json({ v: 1, id: "conv_1", agent: "a" }, { status: 201 });
    return Response.json({ v: 1, agents: [] });
  }) as typeof fetch;
}

describe("sign-in", () => {
  it("sends a session's CSRF token, from /me, on requests other than GET", async () => {
    const seen: Request[] = [];
    const client = new AgentClient({ fetch: sessionServer({ csrf: "c1" }, seen) });
    expect(await client.me()).toMatchObject({ username: "ann", csrf: "c1" });
    await client.createConversation("a");
    await client.listAgents();
    expect(seen.map((r) => `${r.method} ${new URL(r.url).pathname} ${r.headers.get(csrfHeader)}`)).toEqual([
      "GET /v1/auth/me null",
      "POST /v1/conversations c1",
      "GET /v1/agents null",
    ]);
  });

  it("reads /me again once when the token is refused, then retries", async () => {
    const seen: Request[] = [];
    const server = { csrf: "c1" };
    const client = new AgentClient({ fetch: sessionServer(server, seen) });
    // No token yet (a reload with the cookie), then a rotated one.
    for (const csrf of ["c1", "c2"]) {
      server.csrf = csrf;
      seen.length = 0;
      expect(await client.createConversation("a")).toEqual({ id: "conv_1", agent: "a" });
      expect(seen.map((r) => `${r.method} ${new URL(r.url).pathname}`)).toEqual([
        "POST /v1/conversations",
        "GET /v1/auth/me",
        "POST /v1/conversations",
      ]);
      expect(seen[2]!.headers.get(csrfHeader)).toBe(csrf);
    }
  });

  it("gives up after one retry", async () => {
    const seen: Request[] = [];
    const fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
      seen.push(new Request(new URL(String(input), "http://test.invalid"), init));
      if (String(input).endsWith("/v1/auth/me")) {
        return Response.json({ v: 1, tenant: "t", roles: [], scopes: [], csrf: "never-right" });
      }
      return Response.json({ v: 1, error: { code: "csrf_failed", message: "no" } }, { status: 403 });
    }) as typeof globalThis.fetch;
    const err = await new AgentClient({ fetch }).createConversation("a").catch((e: unknown) => e);
    expect(err).toMatchObject({ status: 403, code: "csrf_failed" });
    expect(seen).toHaveLength(3);
  });

  it("does not retry a sign-in refused as cross-site, which no token fixes", async () => {
    const seen: Request[] = [];
    const fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
      seen.push(new Request(new URL(String(input), "http://test.invalid"), init));
      if (String(input).endsWith("/v1/auth/me")) {
        return Response.json({ v: 1, error: { code: "unauthenticated", message: "no" } }, { status: 401 });
      }
      return Response.json({ v: 1, error: { code: "csrf_failed", message: "no" } }, { status: 403 });
    }) as typeof globalThis.fetch;
    const err = await new AgentClient({ fetch }).login("ann", "pw").catch((e: unknown) => e);
    expect(err).toMatchObject({ status: 403, code: "csrf_failed" });
    expect(seen).toHaveLength(1);
  });

  it("posts the username and password, and forgets the token at logout", async () => {
    const seen: Request[] = [];
    const client = new AgentClient({ fetch: sessionServer({ csrf: "c1" }, seen) });
    await client.login("ann", "pw");
    expect(seen[0]!.method).toBe("POST");
    expect(new URL(seen[0]!.url).pathname).toBe("/v1/auth/login");
    expect(await seen[0]!.json()).toEqual({ username: "ann", password: "pw" });
    await client.me();
    await client.logout();
    expect(seen[2]!.headers.get(csrfHeader)).toBe("c1");
    seen.length = 0;
    await client.createConversation("a");
    expect(seen[0]!.headers.has(csrfHeader)).toBe(false);
  });

  it("reads the sign-in methods, and refuses a body without them", async () => {
    const ok = new AgentClient({
      fetch: fakeFetch(['{"v":1,"methods":["password","token"]}'], { headers: { "Content-Type": "application/json" } }),
    });
    expect(await ok.authConfig()).toEqual({ methods: ["password", "token"] });
    const bad = new AgentClient({
      fetch: fakeFetch(['{"v":1,"agents":[]}'], { headers: { "Content-Type": "application/json" } }),
    });
    await expect(bad.authConfig()).rejects.toBeInstanceOf(BadResponseError);
    await expect(bad.me()).rejects.toBeInstanceOf(BadResponseError);
  });
});

describe("decodeMessage", () => {
  it("keeps malformed known blocks as placeholders", () => {
    expect(decodeMessage({ role: "user", content: [{ type: "text" }, "nonsense"] })?.content).toEqual([
      { type: "text", unknown: true, raw: { type: "text" } },
      { type: "unknown", unknown: true, raw: "nonsense" },
    ]);
  });
});

/** A fetch whose body is these byte chunks, then error (if given) or the end. */
function bytesFetch(
  chunks: Uint8Array[],
  error?: unknown,
  onCancel?: () => void,
  type = "text/event-stream",
): typeof fetch {
  return (async () => {
    let i = 0;
    const body = new ReadableStream<Uint8Array>({
      pull(c) {
        const chunk = chunks[i++];
        if (chunk) c.enqueue(chunk);
        else if (error !== undefined) c.error(error);
        else c.close();
      },
      cancel() {
        onCancel?.();
      },
    });
    return new Response(body, { headers: { "Content-Type": type } });
  }) as typeof fetch;
}

describe("bytes", () => {
  it("reads a UTF-8 character split across reads", async () => {
    const answer = "é ü 中文 😀";
    const bytes = new TextEncoder().encode(
      `data: ${start}\n\ndata: {"v":1,"type":"run_done","answer":"${answer}"}\n\n`,
    );
    for (let i = 1; i < bytes.length; i++) {
      const got = await readRun(new AgentClient({ fetch: bytesFetch([bytes.slice(0, i), bytes.slice(i)]) }));
      expect(got.end, `split at ${i}`).toBe("done");
      expect(got.raw[1], `split at ${i}`).toMatchObject({ type: "run_done", answer });
    }
  });

  it("drops a byte order mark the decoder left in", () => {
    // TextDecoder strips a leading BOM itself, so the framing test can't see
    // this; SSEReader must too (the spec says so, and not every caller decodes).
    const r = new SSEReader();
    r.push("\uFEFFdata: x\n\n");
    expect([...r.take()]).toEqual(["x"]);
  });

  it("turns a broken body into StreamTruncatedError, keeping the cause", async () => {
    const broken = new TypeError("network error");
    const got = await readRun(
      new AgentClient({ fetch: bytesFetch([new TextEncoder().encode(`data: ${start}\n\n`)], broken) }),
    );
    expect(got.raw.map((e) => e.type)).toEqual(["run_start"]);
    expect(got.end).toBeInstanceOf(StreamTruncatedError);
    expect((got.end as Error).cause).toBe(broken);
  });

  it("cancels the download when the caller stops reading", async () => {
    let cancelled = false;
    const chunks = [`data: ${start}\n\n`, `data: ${start}\n\n`].map((c) => new TextEncoder().encode(c));
    const client = new AgentClient({ fetch: bytesFetch(chunks, undefined, () => (cancelled = true)) });
    for await (const ev of client.run({ message: "hi" })) {
      expect(ev.type).toBe("run_start");
      break;
    }
    expect(cancelled).toBe(true);
  });
});

describe("list responses", () => {
  const json = (body: string) =>
    new AgentClient({
      fetch: bytesFetch([new TextEncoder().encode(body)], undefined, undefined, "application/json"),
    });

  it.each(['{"v":1,"agents":[]}', '{"v":1,"agents":null}', '{"v":1}'])("%s is no agents", async (body) => {
    expect(await json(body).listAgents()).toEqual([]);
  });

  it.each(['{"v":1,"agents":{}}', '{"v":1,"agents":"x"}', "null", "[]", '"agents"'])(
    "%s is a bad response, not an empty list",
    async (body) => {
      await expect(json(body).listAgents()).rejects.toBeInstanceOf(BadResponseError);
    },
  );

  it("keeps an abort while reading the body an abort", async () => {
    const client = new AgentClient({
      fetch: bytesFetch(
        [new TextEncoder().encode('{"v":1,')],
        new DOMException("aborted", "AbortError"),
        undefined,
        "application/json",
      ),
    });
    await expect(client.listAgents()).rejects.toMatchObject({ name: "AbortError" });
  });
});

describe("network errors", () => {
  const json = (body: string, error?: unknown) =>
    new AgentClient({
      fetch: bytesFetch([new TextEncoder().encode(body)], error, undefined, "application/json"),
    });

  it("is a NetworkError, keeping the cause, when fetch fails", async () => {
    const down = new TypeError("Failed to fetch");
    const client = new AgentClient({
      fetch: (async () => {
        throw down;
      }) as typeof fetch,
    });
    const err: unknown = await client.listAgents().catch((e: unknown) => e);
    expect(err).toBeInstanceOf(NetworkError);
    expect((err as Error).cause).toBe(down);
    await expect(readRun(client).then((r) => r.end)).resolves.toBeInstanceOf(NetworkError);
  });

  it("is a NetworkError when the body breaks, and BadResponseError when it isn't JSON", async () => {
    await expect(json('{"v":1,', new TypeError("network error")).listAgents()).rejects.toBeInstanceOf(NetworkError);
    await expect(json("<html>").listAgents()).rejects.toBeInstanceOf(BadResponseError);
  });

  it("isn't made of a token that can't go in a header", async () => {
    // The browser throws before sending: that's not the network.
    const client = new AgentClient({ token: () => "abc\u200b", fetch: bytesFetch([]) });
    const err: unknown = await client.listAgents().catch((e: unknown) => e);
    expect(err).toBeInstanceOf(TypeError);
    expect(err).not.toBeInstanceOf(NetworkError);
  });
});

describe("conversations", () => {
  const json = (body: string) =>
    new AgentClient({
      fetch: bytesFetch([new TextEncoder().encode(body)], undefined, undefined, "application/json"),
    });

  it.each([
    ['{"v":1,"conversations":[],"next_cursor":"c2"}', "c2"],
    ['{"v":1,"conversations":[],"next_cursor":""}', undefined],
    ['{"v":1,"conversations":[],"next_cursor":7}', undefined],
    ['{"v":1,"conversations":null}', undefined],
  ])("%s has cursor %s", async (body, cursor) => {
    expect(await json(body).listConversations()).toEqual({ conversations: [], nextCursor: cursor });
  });

  it("with a non-list is a bad response", async () => {
    await expect(json('{"v":1,"conversations":{}}').listConversations()).rejects.toBeInstanceOf(BadResponseError);
  });
});

describe("background runs and threads (T074)", () => {
  const jsonFetch = (body: string, seen: Request[]) =>
    fakeFetch([body], { headers: { "Content-Type": "application/json" } }, seen);

  it("follows a run's events from Last-Event-ID, and says whether the end event came", async () => {
    const seen: Request[] = [];
    const ev = (id: string, type: string) => `data: {"v":1,"type":"${type}","id":"${id}","seq":1,"run_id":"r"}\n\n`;
    // The server replayed what it has and ended the stream: no end event.
    let client = new AgentClient({ fetch: fakeFetch([ev("3", "tool_call"), ev("4", "tool_result")], {}, seen) });
    const got: string[] = [];
    const gen = client.events("run/1", { after: "2.1" });
    let next = await gen.next();
    while (!next.done) {
      got.push(next.value.id);
      next = await gen.next();
    }
    expect(got).toEqual(["3", "4"]);
    expect(next.value).toBe(false);
    expect(new URL(seen[0]!.url).pathname).toBe("/v1/runs/run%2F1/events");
    expect(seen[0]!.method).toBe("GET");
    expect(seen[0]!.headers.get("Last-Event-ID")).toBe("2.1");
    expect(seen[0]!.headers.get("Accept")).toBe("text/event-stream");

    // From the start (no Last-Event-ID), to run_done: true, and nothing after it is read.
    client = new AgentClient({ fetch: fakeFetch([ev("1", "run_start"), ev("2", "run_done"), ev("3", "x")], {}, seen) });
    const all = client.events("r");
    const ids: string[] = [];
    let n = await all.next();
    while (!n.done) {
      ids.push(n.value.id);
      n = await all.next();
    }
    expect(ids).toEqual(["1", "2"]);
    expect(n.value).toBe(true);
    expect(seen[1]!.headers.has("Last-Event-ID")).toBe(false);
  });

  it("lists background runs with the filters as query parameters", async () => {
    const seen: Request[] = [];
    const client = new AgentClient({
      fetch: jsonFetch('{"v":1,"runs":[{"id":"r1","status":"running"}],"next_cursor":"c2"}', seen),
    });
    const page = await client.listRuns({ background: true, status: "running", limit: 50 });
    expect(page.runs.map((r) => r.id)).toEqual(["r1"]);
    expect(page.nextCursor).toBe("c2");
    const url = new URL(seen[0]!.url);
    expect(url.pathname).toBe("/v1/runs");
    expect(Object.fromEntries(url.searchParams)).toEqual({ background: "true", status: "running", limit: "50" });
  });

  it("cancels with POST /v1/runs/{id}/cancel and answers with the run", async () => {
    const seen: Request[] = [];
    const client = new AgentClient({ fetch: jsonFetch('{"v":1,"id":"r/1","status":"canceled"}', seen) });
    const run = await client.cancelRun("r/1");
    expect(run.status).toBe("canceled");
    expect(seen[0]!.method).toBe("POST");
    expect(new URL(seen[0]!.url).pathname).toBe("/v1/runs/r%2F1/cancel");
  });

  it("starts a task with POST /v1/tasks, and refuses an answer without a run", async () => {
    const seen: Request[] = [];
    const client = new AgentClient({
      fetch: jsonFetch('{"v":1,"id":"r1","status":"running","background":true}', seen),
    });
    expect((await client.startTask({ input: "go", agent: "a" })).background).toBe(true);
    expect(await seen[0]!.json()).toEqual({ input: "go", agent: "a" });
    const bad = new AgentClient({ fetch: jsonFetch('{"v":1}', []) });
    await expect(bad.startTask({ input: "go" })).rejects.toBeInstanceOf(BadResponseError);
  });

  it("lists threads and reads one with its messages", async () => {
    const seen: Request[] = [];
    let client = new AgentClient({ fetch: jsonFetch('{"v":1,"threads":[{"id":"t1","hops":2}]}', seen) });
    const page = await client.listThreads({ limit: 20 });
    expect(page).toEqual({ threads: [{ id: "t1", hops: 2 }], nextCursor: undefined });
    expect(new URL(seen[0]!.url).search).toBe("?limit=20");
    client = new AgentClient({ fetch: jsonFetch('{"v":1,"id":"t1","messages":null}', seen) });
    expect((await client.getThread("t1")).messages).toEqual([]);
    expect(new URL(seen[1]!.url).pathname).toBe("/v1/threads/t1");
    client = new AgentClient({ fetch: jsonFetch('{"v":1,"id":"t1","messages":{}}', seen) });
    await expect(client.getThread("t1")).rejects.toBeInstanceOf(BadResponseError);
  });
});

describe("the catalog and credentials (T064)", () => {
  const jsonFetch = (body: unknown, seen: Request[], status = 200) =>
    (async (input: RequestInfo | URL, init?: RequestInit) => {
      seen.push(new Request(new URL(String(input), "http://test.invalid"), init));
      return Response.json(body, { status });
    }) as typeof fetch;

  it("gets an agent, and a toolset's tools, by escaped name", async () => {
    const seen: Request[] = [];
    const agent = { v: 1, name: "a/b", toolsets: [], system: "s" };
    expect(await new AgentClient({ fetch: jsonFetch(agent, seen) }).getAgent("a/b")).toMatchObject({ system: "s" });
    const tools = { v: 1, toolset: "web", status: "ready", tools: [{ name: "t", description: "", input_schema: {} }] };
    expect(await new AgentClient({ fetch: jsonFetch(tools, seen) }).listTools("we b")).toMatchObject({
      status: "ready",
      tools: [{ name: "t" }],
    });
    expect(seen.map((r) => new URL(r.url).pathname)).toEqual(["/v1/agents/a%2Fb", "/v1/toolsets/we%20b/tools"]);
    await expect(new AgentClient({ fetch: jsonFetch({ v: 1 }, seen) }).getAgent("x")).rejects.toBeInstanceOf(
      BadResponseError,
    );
  });

  it("puts a key, deletes it with its scope, and starts a connect flow", async () => {
    const seen: Request[] = [];
    const info = { v: 1, slot: "mail", scope: "user", status: "connected", oauth: false, toolsets: ["mail"] };
    const c = new AgentClient({ fetch: jsonFetch(info, seen), token: () => "tok" });
    expect(await c.putCredential("mail", { scope: "user", kind: "api_key", api_key: "k" })).toMatchObject({
      status: "connected",
    });
    await c.deleteCredential("mail", "user");
    expect(seen.map((r) => `${r.method} ${new URL(r.url).pathname}${new URL(r.url).search}`)).toEqual([
      "PUT /v1/credentials/mail",
      "DELETE /v1/credentials/mail?scope=user",
    ]);
    expect(await seen[0]?.json()).toEqual({ scope: "user", kind: "api_key", api_key: "k" });

    const flow = { v: 1, authorize_url: "https://idp.example/authorize?state=s", expires_at: "2026-10-22T10:00:00Z" };
    const started = await new AgentClient({ fetch: jsonFetch(flow, seen) }).connectCredential("mail", {
      return_url: "https://console.example/connections/done",
    });
    expect(started.authorize_url).toBe(flow.authorize_url);
    await expect(
      new AgentClient({ fetch: jsonFetch({ v: 1 }, seen) }).connectCredential("mail", {}),
    ).rejects.toBeInstanceOf(BadResponseError);
    // The page opens it in a window of its own origin: only http(s), never script.
    for (const authorize_url of ["javascript:alert(1)", "data:text/html,x", "/relative", "JavaScript:alert(1)"]) {
      await expect(
        new AgentClient({ fetch: jsonFetch({ v: 1, authorize_url }, seen) }).connectCredential("mail", {}),
        authorize_url,
      ).rejects.toBeInstanceOf(BadResponseError);
    }
  });

  it("says the server keeps no credentials with a 404", async () => {
    const body = { v: 1, error: { code: "not_found", message: "this server keeps no credentials" } };
    const err = await new AgentClient({ fetch: jsonFetch(body, [], 404) }).listCredentials().catch((e: unknown) => e);
    expect(err).toMatchObject({ status: 404, code: "not_found" });
  });
});

describe("toolsets added at run time and the MCP Registry (T096, T097)", () => {
  const jsonFetch = (body: unknown, seen: Request[], status = 200) =>
    (async (input: RequestInfo | URL, init?: RequestInit) => {
      seen.push(new Request(new URL(String(input), "http://test.invalid"), init));
      return Response.json(body, { status });
    }) as typeof fetch;
  const ts = { v: 1, name: "linear", status: "review_needed", tools: null };

  it("adds, reviews, changes and removes by escaped name, and checks the answer is a toolset", async () => {
    const seen: Request[] = [];
    const c = new AgentClient({ fetch: jsonFetch(ts, seen) });
    const added = await c.addToolset({ type: "mcp", name: "linear", url: "https://mcp.linear.app/mcp" });
    expect(added.tools).toEqual([]); // null is an empty list
    await c.getToolsetReview("a/b");
    await c.reviewToolset("a/b", [{ name: "t", fingerprint: "f", decision: "approve", kind: "read", risk: "low" }]);
    await c.setToolsetEnabled("a/b", false);
    await new AgentClient({ fetch: async () => new Response(null, { status: 204 }) }).deleteToolset("a/b");
    expect(seen.map((r) => `${r.method} ${new URL(r.url).pathname}`)).toEqual([
      "POST /v1/toolsets",
      "GET /v1/toolsets/a%2Fb/review",
      "POST /v1/toolsets/a%2Fb/review",
      "PATCH /v1/toolsets/a%2Fb",
    ]);
    expect(await seen[2]!.text()).toBe(
      '{"tools":[{"name":"t","fingerprint":"f","decision":"approve","kind":"read","risk":"low"}]}',
    );
    expect(await seen[3]!.text()).toBe('{"enabled":false}');
    await expect(new AgentClient({ fetch: jsonFetch({ v: 1 }, seen) }).getToolsetReview("x")).rejects.toBeInstanceOf(
      BadResponseError,
    );
    const refused = { v: 1, error: { code: "auth_required", message: "the server answered 401" } };
    await expect(
      new AgentClient({ fetch: jsonFetch(refused, seen, 422) }).addToolset({ type: "mcp", name: "x", url: "u" }),
    ).rejects.toMatchObject({ status: 422, code: "auth_required" });
  });

  it("searches the registry with the query escaped, and reads a missing list as none", async () => {
    const seen: Request[] = [];
    const body = { v: 1, query: "a&b", registry: "r", restricted: true, servers: [{ name: "s", remotes: null }] };
    const found = await new AgentClient({ fetch: jsonFetch(body, seen) }).searchMCPRegistry("a&b", { limit: 5 });
    expect(found).toMatchObject({ restricted: true, servers: [{ name: "s", remotes: [] }] });
    expect(new URL(seen[0]!.url).searchParams.get("q")).toBe("a&b");
    expect(new URL(seen[0]!.url).searchParams.get("limit")).toBe("5");
  });

  it("lists a stored agent's versions and saves a new one", async () => {
    const seen: Request[] = [];
    const list = { v: 1, versions: [{ name: "h", version: 1, published: true, definition: {}, created_at: "" }] };
    expect(await new AgentClient({ fetch: jsonFetch(list, seen) }).listAgentVersions("h")).toMatchObject({
      latest: 1,
      published: 0,
    });
    // A deleted draft keeps its number: latest is the server's, not the highest listed.
    const after = { ...list, latest: 4, published: 1 };
    expect(await new AgentClient({ fetch: jsonFetch(after, seen) }).listAgentVersions("h")).toMatchObject({
      latest: 4,
      published: 1,
    });
    const saved = { v: 1, name: "h", version: 2, published: false, definition: {}, created_at: "" };
    const c = new AgentClient({ fetch: jsonFetch(saved, seen) });
    expect((await c.saveAgentVersion("h", { definition: {}, if_latest: 1 })).version).toBe(2);
    expect(`${seen[2]!.method} ${new URL(seen[2]!.url).pathname}`).toBe("POST /v1/agents/h/versions");
    await expect(
      new AgentClient({ fetch: jsonFetch({ v: 1 }, seen) }).saveAgentVersion("h", { definition: {} }),
    ).rejects.toBeInstanceOf(BadResponseError);
  });
});
