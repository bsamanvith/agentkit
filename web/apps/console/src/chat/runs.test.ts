import { ApiError, type AgentClient, type RunEvent } from "@agentkit/agent-ui/api";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { Drafts } from "./drafts";
import { busyRetryMs, RunStore } from "./runs";

const ev = (type: string, extra: Record<string, unknown> = {}): RunEvent => ({
  v: 1,
  type,
  id: "1",
  seq: 1,
  run_id: "r1",
  ...extra,
});

/** A client whose runs follow scripts: events, a thrown error, or a stall until aborted. */
function fakeClient(scripts: (RunEvent[] | Error | "stall")[]) {
  const calls: { req: unknown; signal: AbortSignal | undefined }[] = [];
  const stopped: string[] = [];
  const client = {
    async stopRun(id: string) {
      stopped.push(id);
    },
    async *run(req: unknown, opts: { signal?: AbortSignal } = {}) {
      calls.push({ req, signal: opts.signal });
      const s = scripts.shift() ?? [ev("run_start"), ev("run_done")];
      if (s instanceof Error) throw s;
      if (s === "stall") {
        yield ev("run_start");
        yield ev("text_delta", { text: "Part" });
        await new Promise((_, reject) => {
          const abort = () => reject(new DOMException("aborted", "AbortError"));
          if (opts.signal?.aborted) abort();
          opts.signal?.addEventListener("abort", abort);
        });
        return;
      }
      for (const e of s) yield e;
    },
  } as unknown as AgentClient;
  return { client, calls, stopped };
}

let hooks: {
  unauthorized: ReturnType<typeof vi.fn<() => void>>;
  announce: ReturnType<typeof vi.fn<(text: string) => void>>;
};
let store: RunStore;

beforeEach(() => {
  sessionStorage.clear();
  hooks = { unauthorized: vi.fn<() => void>(), announce: vi.fn<(text: string) => void>() };
  store = new RunStore(new Drafts(), hooks);
});
afterEach(() => vi.useRealTimers());

const settled = () => vi.waitFor(() => expect(store.running("c1")).toBe(false));

describe("RunStore", () => {
  it("streams an answer, then keeps its usage by exchange and bumps the version", async () => {
    const { client } = fakeClient([
      [ev("run_start"), ev("text_delta", { text: "Hi" }), ev("run_done", { usage: { output_tokens: 3 }, turns: 1 })],
    ]);
    const listed = store.listVersion();
    store.send(client, "c1", "Hello", [], "researcher");
    expect(store.get("c1").live?.user).toMatchObject({ text: "Hello", pending: true });
    await settled();
    const s = store.get("c1");
    expect(s.live?.answer.state).toBe("done");
    expect(s.live?.user?.pending).toBe(false);
    expect(s.meta.get(1)).toMatchObject({ state: "done", turns: 1, usage: { output_tokens: 3 } });
    expect(s.version).toBe(1);
    expect(store.listVersion()).toBeGreaterThan(listed);
    expect(hooks.announce).toHaveBeenLastCalledWith("Answer complete");
  });

  it("stops a run on the server and aborts its request", async () => {
    const { client, calls, stopped } = fakeClient(["stall"]);
    store.send(client, "c1", "Long", [], "researcher");
    await vi.waitFor(() => expect(store.get("c1").live?.answer.runId).toBe("r1"));
    expect(store.running("c1")).toBe(true);
    store.stop("c1");
    expect(stopped).toEqual(["r1"]);
    expect(calls[0]?.signal?.aborted).toBe(true);
    await settled();
    expect(store.get("c1").live?.answer.state).toBe("stopped");
  });

  it("puts a message refused before the run back in the draft", async () => {
    const { client } = fakeClient([new ApiError(413, { code: "too_large", message: "big" })]);
    store.send(client, "c1", "Big", [], "researcher");
    store.drafts.set("c1", "typed meanwhile");
    await vi.waitFor(() => expect(store.get("c1").problem).toBeDefined());
    expect(store.get("c1").live).toBeUndefined();
    expect(store.drafts.get("c1")).toBe("Big\n\ntyped meanwhile");
  });

  it("asks for the token on a 401", async () => {
    const { client } = fakeClient([new ApiError(401, { code: "unauthorized", message: "no" })]);
    store.send(client, "c1", "Hi", [], "researcher");
    await vi.waitFor(() => expect(hooks.unauthorized).toHaveBeenCalledOnce());
    expect(store.drafts.get("c1")).toBe("Hi");
    expect(store.get("c1").problem).toBeUndefined();
  });

  it("retries a busy server once by itself, then waits for Retry now or Cancel", async () => {
    vi.useFakeTimers();
    const busy = () => new ApiError(503, { code: "over_capacity", message: "busy" }, 2);
    const { client, calls } = fakeClient([busy(), busy()]);
    store.send(client, "c1", "Hi", [], "researcher");
    await vi.waitFor(() => expect(store.get("c1").problem?.busy).toBe("retrying"));
    expect(store.get("c1").live?.user?.pending).toBe(true);
    await vi.advanceTimersByTimeAsync(busyRetryMs);
    await vi.waitFor(() => expect(store.get("c1").problem?.busy).toBe("still"));
    expect(calls).toHaveLength(2);
    await vi.advanceTimersByTimeAsync(busyRetryMs * 3);
    expect(calls).toHaveLength(2); // only once by itself

    store.cancel("c1");
    expect(store.get("c1").live).toBeUndefined();
    expect(store.get("c1").problem).toBeUndefined();
    expect(store.drafts.get("c1")).toBe("Hi");
  });

  it("resumes with an empty message and no user bubble", async () => {
    const { client, calls } = fakeClient([[ev("run_start"), ev("run_done")]]);
    store.resume(client, "c1", [{ role: "user", key: "u", text: "Q" }], "researcher");
    await settled();
    expect(calls[0]?.req).toEqual({ message: "", conversation_id: "c1" });
    expect(store.get("c1").live?.user).toBeUndefined();
    expect(store.get("c1").meta.has(1)).toBe(true);
  });

  it("marks a run that ended while elsewhere as unread, until viewed", async () => {
    const { client } = fakeClient([[ev("run_start"), ev("run_done")]]);
    store.view("other");
    store.send(client, "c1", "Hi", [], "researcher");
    await settled();
    expect(store.get("c1").unread).toBe(true);
    store.view("c1");
    expect(store.get("c1").unread).toBe(false);
  });

  it("is a lost answer when the stream breaks", async () => {
    const { client } = fakeClient([[ev("run_start"), ev("text_delta", { text: "x" })]]);
    // The fake ends without run_done: the real client throws StreamTruncatedError there.
    const run = client.run.bind(client);
    client.run = async function* (req, opts) {
      yield* run(req, opts);
      throw new Error("truncated");
    };
    store.send(client, "c1", "Hi", [], "researcher");
    await settled();
    expect(store.get("c1").live?.answer.state).toBe("lost");
  });

  it("clears everything when the token is forgotten", async () => {
    const { client, calls } = fakeClient(["stall"]);
    store.drafts.set("c2", "draft");
    store.send(client, "c1", "Long", [], "researcher");
    await vi.waitFor(() => expect(calls[0]?.signal).toBeDefined());
    store.clear();
    expect(calls[0]?.signal?.aborted).toBe(true);
    expect(store.drafts.get("c2")).toBe("");
    expect(sessionStorage.length).toBe(0);
  });

  it("sends an action on a surface as ui_action, shown as the person's message", async () => {
    const { client, calls } = fakeClient([[ev("run_start"), ev("run_done")]]);
    store.drafts.set("c1", "half a thought");
    const action = { surfaceId: "news", componentId: "search", name: "search", context: { topic: "go" } };
    store.act(client, "c1", action, [{ role: "user", key: "h0", text: "Hi" }], "researcher");
    expect(store.get("c1").live?.user).toMatchObject({ text: "search", pending: true, action });
    expect(store.running("c1")).toBe(true);
    await settled();
    expect(calls[0]?.req).toEqual({
      conversation_id: "c1",
      ui_action: { surface_id: "news", component_id: "search", action: "search", context: { topic: "go" } },
    });
    // The draft is the person's own: an action leaves it.
    expect(store.drafts.get("c1")).toBe("half a thought");
    expect(store.get("c1").meta.get(2)).toMatchObject({ state: "done" });
  });

  it("an action that doesn't start says why and puts nothing in the composer", async () => {
    const { client } = fakeClient([new ApiError(400, { code: "invalid_request", message: "no UI" })]);
    store.act(client, "c1", { surfaceId: "s", componentId: "b", name: "go", context: {} }, [], "researcher");
    await vi.waitFor(() => expect(store.get("c1").problem).toBeDefined());
    expect(store.get("c1").live).toBeUndefined();
    expect(store.drafts.get("c1")).toBe("");
  });

  it("a deleted conversation's surface edits go with it", () => {
    const surface = { id: "s", revision: 1 };
    store.edits.set("c1", surface, "/topic", "go");
    store.edits.set("c2", surface, "/topic", "stay");
    store.forget("c1");
    expect(store.edits.get("c1", surface).size).toBe(0);
    expect(store.edits.get("c2", surface).get("/topic")).toBe("stay");
  });

  it("forgets the surfaces' edits with everything else", () => {
    const surface = { id: "s", revision: 1 };
    store.edits.set("c1", surface, "/topic", "go");
    expect(store.edits.get("c1", surface).get("/topic")).toBe("go");
    store.clear();
    expect(store.edits.get("c1", surface).size).toBe(0);
  });
});
