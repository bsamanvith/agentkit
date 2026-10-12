import { describe, expect, it } from "vitest";
import {
  AgentClient,
  ApiError,
  BadResponseError,
  NetworkError,
  ResponseTooLargeError,
  SSEReader,
  StreamTruncatedError,
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
