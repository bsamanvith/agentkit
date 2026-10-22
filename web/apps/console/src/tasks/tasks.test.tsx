import type { AgentClient, Run, RunEvent, Thread, ThreadMessage, Trigger } from "@agentkit/agent-ui/api";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createMemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { App } from "../app";
import { routes } from "../routes";
import { follow, type RunView } from "./follow";
import { participants, segmentOf, selectedOf, threadStatus, writtenBy } from "./shared";
import { TaskStore, newsOf, runStatus, unreadStorageKey } from "./store";

const iso = (ms: number) => new Date(ms).toISOString();
const run = (id: string, status: string, over: Partial<Run> = {}): Run => ({
  id,
  conversation_id: `conv_${id}`,
  agent: "researcher",
  status,
  created_at: iso(Date.now() - 60_000),
  background: true,
  usage: { input_tokens: 100, output_tokens: 20 },
  ...over,
});

beforeEach(() => {
  sessionStorage.clear();
  localStorage.clear();
});
afterEach(() => vi.unstubAllGlobals());

describe("newsOf", () => {
  it.each([
    ["running", "done", "finished"],
    ["running", "failed", "failed"],
    ["running", "rejected", "failed"],
    ["running", "suspended", "is waiting for you"],
    ["running", "canceled", "was canceled"],
    ["interrupted", "done", "finished"],
    [undefined, "done", "finished"],
    ["running", "running", undefined],
    ["done", "done", undefined],
    ["done", "failed", undefined],
    ["suspended", "running", undefined],
  ])("%s to %s is %s", (before, now, want) => {
    expect(newsOf(before, now)).toBe(want);
  });

  it("knows which statuses can be canceled", () => {
    expect(["running", "suspended", "interrupted"].every((s) => runStatus(s).active)).toBe(true);
    expect(["done", "failed", "canceled", "rejected", "x"].some((s) => runStatus(s).active)).toBe(false);
  });
});

/** A store over a client whose listRuns answers the pages in turn. */
function storeOver(pages: Run[][], cancel?: (id: string) => Run) {
  const client = {
    listRuns: vi.fn(async () => ({ runs: pages.shift() ?? [], nextCursor: undefined })),
    cancelRun: vi.fn(async (id: string) => cancel?.(id) ?? run(id, "canceled")),
  } as unknown as AgentClient;
  const store = new TaskStore({ client: () => client, unauthorized: vi.fn(), accepted: vi.fn() }, 1e9);
  return { store, client };
}

describe("the store", () => {
  it("tells of runs that end after the first load, once, and keeps them unread", async () => {
    const old = iso(Date.now() - 3_600_000);
    // r5 ended before the first load, though its clock says it started after.
    const r5 = run("r5", "done", { created_at: iso(Date.now() + 5000) });
    const { store } = storeOver([
      [run("r1", "running"), run("r2", "done", { created_at: old }), run("r3", "running"), r5],
      [
        r5,
        run("r1", "done"),
        run("r2", "done", { created_at: old }),
        run("r3", "suspended"),
        run("r4", "failed", { created_at: iso(Date.now() + 1000) }),
      ],
      [run("r1", "done"), run("r3", "suspended"), run("r4", "failed", { created_at: iso(Date.now() + 1000) })],
    ]);
    await store.refresh();
    // The first load is the baseline: nothing is news.
    expect(store.get().toasts).toEqual([]);
    expect(store.get().runs?.map((r) => r.id)).toEqual(["r1", "r2", "r3", "r5"]);
    await store.refresh();
    expect(store.get().toasts.map((t) => [t.runId, t.text])).toEqual([
      ["r1", "Task of researcher finished."],
      ["r3", "Task of researcher is waiting for you."],
      ["r4", "Task of researcher failed."],
    ]);
    expect([...store.get().unread].sort()).toEqual(["r1", "r3", "r4"]);
    expect(JSON.parse(localStorage.getItem(unreadStorageKey) ?? "[]")).toHaveLength(3);
    // Unchanged: no news again.
    await store.refresh();
    expect(store.get().toasts).toHaveLength(3);
    store.markRead("r1");
    store.dismiss(store.get().toasts[0]!.key);
    expect([...store.get().unread].sort()).toEqual(["r3", "r4"]);
    expect(store.get().toasts).toHaveLength(2);
  });

  it("doesn't tell of a run canceled here, or of the run on screen", async () => {
    const { store, client } = storeOver(
      [
        [run("r1", "running"), run("r2", "running"), run("r3", "running")],
        [run("r1", "canceled"), run("r2", "done"), run("r3", "canceled")],
      ],
      // r3's process is another replica's: the cancel is taken, the run ends there.
      (id) => run(id, id === "r3" ? "running" : "canceled"),
    );
    await store.refresh();
    store.view("r2");
    await store.cancel("r3");
    const got = await store.cancel("r1");
    expect(got.status).toBe("canceled");
    expect(client.cancelRun).toHaveBeenCalledWith("r1");
    // The list has the server's answer at once.
    expect(store.get().runs?.find((r) => r.id === "r1")?.status).toBe("canceled");
    await store.refresh();
    expect(store.get().toasts).toEqual([]);
    expect(store.get().unread.size).toBe(0);
  });

  it("keeps the list when a load fails, and clears everything on sign out", async () => {
    const { store, client } = storeOver([[run("r1", "running")]]);
    await store.refresh();
    vi.mocked(client.listRuns).mockRejectedValueOnce(new Error("down"));
    await store.refresh();
    expect(store.get().runs).toHaveLength(1);
    expect(store.get().error?.message).toBe("down");
    store.clear();
    expect(store.get()).toMatchObject({ runs: null, error: null, toasts: [] });
  });

  it("shows at most three toasts, the newest, and keeps every one unread", async () => {
    const ids = ["r1", "r2", "r3", "r4", "r5"];
    const { store } = storeOver([ids.map((id) => run(id, "running")), ids.map((id) => run(id, "done"))]);
    await store.refresh();
    await store.refresh();
    expect(store.get().toasts.map((t) => t.runId)).toEqual(["r3", "r4", "r5"]);
    expect([...store.get().unread].sort()).toEqual(ids);
  });

  it("works without storage: blocked, or holding something else", async () => {
    const blocked = () => {
      throw new DOMException("blocked", "SecurityError");
    };
    vi.stubGlobal("localStorage", { getItem: blocked, setItem: blocked, removeItem: blocked });
    const { store } = storeOver([[run("r1", "running")], [run("r1", "done")]]);
    await store.refresh();
    await store.refresh();
    expect([...store.get().unread]).toEqual(["r1"]);
    store.markRead("r1");
    expect(store.get().unread.size).toBe(0);
    store.clear();
    vi.unstubAllGlobals();

    localStorage.setItem(unreadStorageKey, "{not json");
    expect(storeOver([]).store.get().unread.size).toBe(0);
    localStorage.setItem(unreadStorageKey, JSON.stringify(["r9", 7]));
    expect([...storeOver([]).store.get().unread]).toEqual(["r9"]);
  });

  it("canceling a run that had already ended shows how it ended, without an error or news", async () => {
    const { ApiError } = await import("@agentkit/agent-ui/api");
    const { store, client } = storeOver([[run("r1", "running")], [run("r1", "done")]], () => {
      throw new ApiError(409, { code: "run_final", message: "run r1 is final" });
    });
    (client as unknown as { getRun: unknown }).getRun = vi.fn(async () => run("r1", "done"));
    await store.refresh();
    const got = await store.cancel("r1");
    expect(got.status).toBe("done");
    expect(store.get().runs?.[0]?.status).toBe("done");
    await store.refresh();
    expect(store.get().toasts).toEqual([]);

    // Another 409 (the run is about to start elsewhere) is the caller's to show.
    vi.mocked(client.cancelRun).mockRejectedValueOnce(new ApiError(409, { code: "run_leased", message: "retry" }));
    await expect(store.cancel("r1")).rejects.toMatchObject({ status: 409, code: "run_leased" });
  });

  it("doesn't load while the tab is hidden, loads when it shows, and stops for good on stop()", async () => {
    vi.useFakeTimers();
    let state: DocumentVisibilityState = "hidden";
    Object.defineProperty(document, "visibilityState", { configurable: true, get: () => state });
    const show = (s: DocumentVisibilityState) => {
      state = s;
      document.dispatchEvent(new Event("visibilitychange"));
    };
    try {
      const listRuns = vi.fn(async () => ({ runs: [], nextCursor: undefined }));
      const client = { listRuns } as unknown as AgentClient;
      const store = new TaskStore({ client: () => client, unauthorized: vi.fn(), accepted: vi.fn() }, 5000);
      store.start();
      await vi.advanceTimersByTimeAsync(0);
      expect(listRuns).toHaveBeenCalledTimes(1);
      // Hidden: no load, however long.
      await vi.advanceTimersByTimeAsync(60_000);
      expect(listRuns).toHaveBeenCalledTimes(1);
      show("hidden");
      await vi.advanceTimersByTimeAsync(0);
      expect(listRuns).toHaveBeenCalledTimes(1);
      // Shown: a load now, then every 5 s.
      show("visible");
      await vi.advanceTimersByTimeAsync(0);
      expect(listRuns).toHaveBeenCalledTimes(2);
      await vi.advanceTimersByTimeAsync(5000);
      expect(listRuns).toHaveBeenCalledTimes(3);

      // Stopped while hidden, then started again: the old wait is gone, so
      // showing the tab doesn't load out of turn.
      state = "hidden";
      await vi.advanceTimersByTimeAsync(5000);
      store.stop();
      store.start();
      await vi.advanceTimersByTimeAsync(0);
      expect(listRuns).toHaveBeenCalledTimes(4);
      state = "visible";
      show("visible");
      await vi.advanceTimersByTimeAsync(0);
      expect(listRuns).toHaveBeenCalledTimes(4);
      await vi.advanceTimersByTimeAsync(5000);
      expect(listRuns).toHaveBeenCalledTimes(5);
      store.stop();
      await vi.advanceTimersByTimeAsync(60_000);
      expect(listRuns).toHaveBeenCalledTimes(5);
    } finally {
      delete (document as { visibilityState?: unknown }).visibilityState;
      vi.useRealTimers();
    }
  });
});

/** An events() that yields each script's events in turn, returning whether the end event came. */
function eventsOver(scripts: { events: Partial<RunEvent>[]; ended: boolean }[]) {
  const afters: (string | undefined)[] = [];
  const events = vi.fn((_id: string, opts: { after?: string }) => {
    afters.push(opts.after);
    const s = scripts.shift() ?? { events: [], ended: false };
    return (async function* () {
      for (const e of s.events) yield { v: 1, seq: 1, run_id: "r1", ...e } as RunEvent;
      return s.ended;
    })();
  });
  return { events, afters };
}

describe("follow", () => {
  it("replays the stored events, then follows from the last one it has until the end", async () => {
    const { events, afters } = eventsOver([
      {
        events: [
          {
            type: "run_start",
            id: "1",
            at: "2026-10-22T10:00:00Z",
            prompt: { message: { role: "user", content: [{ type: "text", text: "Summarize the incidents" }] } },
          },
          { type: "tool_call", id: "2", tool_use_id: "t1", name: "search", input: { q: "x" } },
        ],
        ended: false, // another process runs it: the stream ends early
      },
      {
        events: [
          { type: "tool_result", id: "3", tool_use_id: "t1", content: "found" },
          { type: "text_delta", id: "3.1", text: "Two incidents." },
          { type: "run_done", id: "4", at: "2026-10-22T10:00:05Z", usage: { input_tokens: 5 }, turns: 2 },
        ],
        ended: true,
      },
    ]);
    const getRun = vi.fn(async () => run("r1", "running"));
    const client = { events, getRun } as unknown as AgentClient;
    const views: RunView[] = [];
    await follow(client, "r1", (v) => views.push(v), { signal: new AbortController().signal, wait: async () => {} });
    expect(afters).toEqual([undefined, "2"]);
    const last = views.at(-1)!;
    expect(last.ended).toBe(true);
    expect(last.prompt?.text).toBe("Summarize the incidents");
    expect(last.answer.state).toBe("done");
    expect(last.answer.durationMs).toBe(5000);
    expect(last.answer.parts).toEqual([
      { kind: "tools", steps: [expect.objectContaining({ id: "t1", state: "done", result: "found" })] },
      { kind: "text", text: "Two incidents." },
    ]);
    // Every view after the first event holds the events before it: nothing taken twice.
    expect(views.map((v) => v.last)).toEqual(["1", "2", "3", "3.1", "4"]);
  });

  it("stops when the stream ends and the run is over, and when the run is gone", async () => {
    let { events } = eventsOver([{ events: [{ type: "run_start", id: "1" }], ended: false }]);
    let client = { events, getRun: vi.fn(async () => run("r1", "canceled")) } as unknown as AgentClient;
    const views: RunView[] = [];
    await follow(client, "r1", (v) => views.push(v), { signal: new AbortController().signal, wait: async () => {} });
    expect(views.at(-1)).toMatchObject({ ended: true, answer: { state: "stopped" } });

    const { ApiError } = await import("@agentkit/agent-ui/api");
    events = vi.fn(() => {
      throw new ApiError(404, { code: "not_found", message: "no run" });
    });
    client = { events } as unknown as AgentClient;
    const gone: RunView[] = [];
    await follow(client, "nope", (v) => gone.push(v), { signal: new AbortController().signal, wait: async () => {} });
    expect(gone).toHaveLength(1);
    expect(gone[0]).toMatchObject({ gone: true });
  });
});

describe("threads", () => {
  const msg = (m: Partial<ThreadMessage> & { id: string; hop: number }): ThreadMessage => ({
    from: "agent:boss",
    to: "agent:worker",
    to_conversation: "conv_t_worker",
    kind: "request",
    text: "",
    state: "done",
    created_at: "",
    ...m,
  });
  const ms = [
    msg({ id: "m1", hop: 1, from_conversation: "conv_boss", run_id: "run_w1", text: "What is 6*7?" }),
    msg({
      id: "m2",
      hop: 2,
      from: "agent:worker",
      to: "agent:boss",
      kind: "reply",
      in_reply_to: "m1",
      from_conversation: "conv_t_worker",
      to_conversation: "conv_boss",
      run_id: "run_b2",
      text: "42",
    }),
    msg({ id: "m3", hop: 3, from_conversation: "conv_boss", run_id: "run_w3", text: "Thanks" }),
    msg({ id: "m4", hop: 4, from: "agentkit", to: "agent:boss", kind: "notice", to_conversation: "conv_boss" }),
  ];

  it("names who is in a thread, in order, without the server", () => {
    expect(participants(ms)).toEqual(["boss", "worker"]);
  });

  it("links each message to the run that wrote it", () => {
    // The first came from a chat: no run of the thread wrote it.
    expect(writtenBy(ms[0]!, ms)).toBeUndefined();
    // A reply: the run that handled its request.
    expect(writtenBy(ms[1]!, ms)).toBe("run_w1");
    // A later request: the run that handled the message before it, in its sender's conversation.
    expect(writtenBy(ms[2]!, ms)).toBe("run_b2");
  });

  it("says where a thread is", () => {
    expect(threadStatus({ status: "stopped" })).toMatchObject({ word: "Stopped" });
    expect(threadStatus({ status: "open", messages: [ms[0]!] })).toMatchObject({ word: "Waiting for a reply" });
    expect(threadStatus({ status: "open", messages: [{ ...ms[0]!, state: "running" }] })).toMatchObject({
      word: "Working",
    });
    expect(threadStatus({ status: "open", messages: ms.slice(0, 2) })).toMatchObject({ word: "Done" });
  });

  it("reads the segment and the item from the URL", () => {
    expect(segmentOf("/tasks")).toBe("tasks");
    expect(segmentOf("/tasks/r1")).toBe("tasks");
    expect(segmentOf("/tasks/threads")).toBe("threads");
    expect(segmentOf("/tasks/threads/t1")).toBe("threads");
    expect(selectedOf("/tasks/run%2F1")).toBe("run/1");
    expect(selectedOf("/tasks/threads/%E0")).toBe("");
    expect(selectedOf("/tasks")).toBe("");
    expect(selectedOf("/tasks/threads")).toBe("");
    expect(selectedOf("/tasks/triggers")).toBe("");
    expect(selectedOf("/tasks/threads/t%201")).toBe("t 1");
    expect(segmentOf("/tasks/triggers")).toBe("triggers");
  });
});

/** A fake /v1 with background runs, their event streams, and threads. */
function fakeServer() {
  const state = {
    runs: [run("r1", "running"), run("r2", "done", { agent: "writer" })],
    events: new Map<string, Partial<RunEvent>[]>(),
    threads: [] as Thread[],
    triggers: [] as Trigger[],
    seen: [] as { method: string; path: string; after: string | null }[],
  };
  const sse = (events: Partial<RunEvent>[]) =>
    new Response(
      events
        .map((e) => `id: ${e.id}\nevent: ${e.type}\ndata: ${JSON.stringify({ v: 1, seq: 1, run_id: "r1", ...e })}\n\n`)
        .join(""),
      { headers: { "Content-Type": "text/event-stream" } },
    );
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const req = new Request(new URL(String(input), "http://console.test"), init);
      const url = new URL(req.url);
      const path = url.pathname;
      state.seen.push({ method: req.method, path, after: req.headers.get("Last-Event-ID") });
      if (path === "/v1/auth/config") return Response.json({ v: 1, methods: [] });
      if (path === "/v1/auth/me") return Response.json({ v: 1, tenant: "default", roles: [], scopes: ["*"] });
      if (path === "/v1/runs" && req.method === "GET") return Response.json({ v: 1, runs: state.runs });
      if (path === "/v1/triggers") return Response.json({ v: 1, triggers: state.triggers });
      const tm = /^\/v1\/triggers\/([^/]+)\/(pause|resume)$/.exec(path);
      if (tm) {
        const t = state.triggers.find((x) => x.id === tm[1])!;
        t.paused = tm[2] === "pause";
        t.version++;
        if (t.paused) delete t.next_fire_at;
        return Response.json({ v: 1, ...t });
      }
      if (path === "/v1/threads")
        return Response.json({ v: 1, threads: state.threads.map((t) => ({ ...t, messages: undefined })) });
      let m = /^\/v1\/threads\/([^/]+)$/.exec(path);
      if (m) {
        const t = state.threads.find((x) => x.id === decodeURIComponent(m![1]!));
        return t
          ? Response.json({ v: 1, ...t })
          : Response.json({ v: 1, error: { code: "not_found", message: "no" } }, { status: 404 });
      }
      m = /^\/v1\/runs\/([^/]+)(\/events|\/cancel)?$/.exec(path);
      if (m) {
        const id = decodeURIComponent(m[1]!);
        const r = state.runs.find((x) => x.id === id);
        if (!r) return Response.json({ v: 1, error: { code: "not_found", message: "no run" } }, { status: 404 });
        if (m[2] === "/cancel") {
          r.status = "canceled";
          return Response.json({ v: 1, ...r });
        }
        if (m[2] === "/events") return sse(state.events.get(id) ?? []);
        return Response.json({ v: 1, ...r });
      }
      return Response.json({ v: 1, agents: [], toolsets: [], conversations: [] });
    }),
  );
  return state;
}

const renderAt = (path: string) => render(<App router={createMemoryRouter(routes, { initialEntries: [path] })} />);

describe("the Tasks pages", () => {
  it("list the tasks in their groups, and Cancel… in the list asks, then ends one on the server", async () => {
    const server = fakeServer();
    const confirm = vi.spyOn(window, "confirm").mockReturnValueOnce(false).mockReturnValue(true);
    renderAt("/tasks");
    const list = await screen.findByRole("navigation", { name: "Tasks" }, { timeout: 5000 });
    const rows = await within(list).findAllByRole("listitem");
    expect(rows).toHaveLength(2);
    expect(rows[0]).toHaveTextContent("researcher");
    expect(rows[0]).toHaveTextContent("Running");
    expect(rows[0]).toHaveTextContent("120 tokens");
    expect(within(list).getByRole("region", { name: "Running and paused" })).toContainElement(rows[0]!);
    expect(within(list).getByRole("region", { name: "Finished" })).toContainElement(rows[1]!);
    // Only a run that goes on can be canceled.
    expect(within(rows[1]!).queryByRole("button", { name: /Cancel/ })).toBeNull();
    // Asked first: No leaves it running.
    await userEvent.click(within(rows[0]!).getByRole("button", { name: "Cancel task of researcher" }));
    expect(confirm).toHaveBeenCalledTimes(1);
    expect(server.seen.some((r) => r.path === "/v1/runs/r1/cancel")).toBe(false);
    await userEvent.click(within(rows[0]!).getByRole("button", { name: "Cancel task of researcher" }));
    await waitFor(() =>
      expect(server.seen).toContainEqual({ method: "POST", path: "/v1/runs/r1/cancel", after: null }),
    );
    await waitFor(() => expect(within(list).getAllByRole("listitem")[0]).toHaveTextContent("Canceled"));
    expect(within(list).queryByRole("button", { name: /Cancel/ })).toBeNull();
    // The agent filter.
    await userEvent.selectOptions(screen.getByRole("combobox", { name: "Agent" }), "writer");
    expect(within(list).getAllByRole("listitem")).toHaveLength(1);
    expect(within(list).getByRole("listitem")).toHaveTextContent("writer");
  });

  it("show a run's transcript from its events, with its steps, and cancel it from its page", async () => {
    const server = fakeServer();
    server.events.set("r1", [
      {
        type: "run_start",
        id: "1",
        prompt: { message: { role: "user", content: [{ type: "text", text: "Summarize the incidents" }] } },
      },
      { type: "tool_call", id: "2", tool_use_id: "t1", name: "echo", input: { text: "pineapple" } },
      { type: "tool_result", id: "3", tool_use_id: "t1", content: "pineapple" },
      { type: "text_delta", id: "3.1", text: "Working on it" },
    ]);
    renderAt("/tasks/r1");
    expect(
      await screen.findByRole("heading", { level: 1, name: "Summarize the incidents" }, { timeout: 5000 }),
    ).toBeInTheDocument();
    const log = screen.getByRole("log", { name: "Transcript of Summarize the incidents" });
    expect(await within(log).findByText("Working on it")).toBeInTheDocument();
    expect(within(log).getByRole("button", { name: /echo/i })).toBeInTheDocument();
    expect(within(log).getByRole("region", { name: "About this task" })).toHaveTextContent("r1");
    // The stream ended early: it follows again from the last event.
    await waitFor(() => expect(server.seen.filter((r) => r.path === "/v1/runs/r1/events").at(-1)?.after).toBe("3.1"), {
      timeout: 3000,
    });
    vi.spyOn(window, "confirm").mockReturnValue(true);
    await userEvent.click(screen.getByRole("button", { name: "Cancel…" }));
    await waitFor(() =>
      expect(server.seen).toContainEqual({ method: "POST", path: "/v1/runs/r1/cancel", after: null }),
    );
  });

  it("show a thread's messages in order, each linked to its runs", async () => {
    const server = fakeServer();
    const base = { to_conversation: "c", state: "done", created_at: "" };
    server.threads = [
      {
        id: "thr_1",
        status: "open",
        hops: 2,
        tokens: 1234,
        created_at: "",
        updated_at: "",
        messages: [
          {
            ...base,
            id: "m1",
            hop: 1,
            from: "agent:boss",
            to: "agent:worker",
            kind: "request",
            text: "What is 6*7?",
            run_id: "run_w",
            to_conversation: "conv_w",
            from_conversation: "conv_b",
          },
          {
            ...base,
            id: "m2",
            hop: 2,
            from: "agent:worker",
            to: "agent:boss",
            kind: "reply",
            text: "42",
            in_reply_to: "m1",
            run_id: "run_b",
            from_conversation: "conv_w",
            to_conversation: "conv_b",
          },
        ],
      },
    ];
    renderAt("/tasks/threads");
    const list = await screen.findByRole("navigation", { name: "Threads" }, { timeout: 5000 });
    const row = await within(list).findByRole("link", { name: /boss ⇄ worker/ });
    expect(row).toHaveTextContent("2 hops · worker: 42");
    await userEvent.click(row);
    expect(await screen.findByRole("heading", { level: 1, name: "boss ⇄ worker" })).toBeInTheDocument();
    const items = within(screen.getByRole("list", { name: "Messages" })).getAllByRole("listitem");
    expect(items.map((li) => li.dataset.hop)).toEqual(["1", "2"]);
    expect(items[0]).toHaveTextContent("boss");
    expect(items[0]).toHaveTextContent("What is 6*7?");
    expect(within(items[0]!).getByRole("link", { name: "run_w" })).toHaveAttribute("href", "/tasks/run_w");
    expect(items[1]).toHaveTextContent("42");
    // The reply was written by the run that handled the request, and handled by the boss's.
    expect(
      within(items[1]!)
        .getAllByRole("link")
        .map((a) => a.getAttribute("href")),
    ).toEqual(["/tasks/run_w", "/tasks/run_b"]);
  });

  it("badge the rail with the tasks waiting for you, and a toast opens the task", async () => {
    const server = fakeServer();
    renderAt("/");
    const rail = await screen.findByRole("navigation", { name: "Sections" });
    expect(await within(rail).findByRole("link", { name: "Tasks" })).toBeInTheDocument();
    await waitFor(() => expect(server.seen.some((r) => r.path === "/v1/runs")).toBe(true));
    server.runs[0]!.status = "suspended";
    // The next load (every 5 s) finds it waiting.
    const toast = await screen.findByText("Task of researcher is waiting for you.", undefined, { timeout: 8000 });
    expect(within(rail).getByRole("link", { name: "Tasks, 1 waiting for you" })).toBeInTheDocument();
    await userEvent.click(within(toast.closest("li")!).getByRole("link", { name: "Open" }));
    expect(await screen.findByRole("region", { name: "About this task" }, { timeout: 5000 })).toHaveTextContent("r1");
    // A finished task doesn't count.
    server.runs[0]!.status = "done";
    await waitFor(() => expect(within(rail).getByRole("link", { name: "Tasks" })).toBeInTheDocument(), {
      timeout: 8000,
    });
  }, 20_000);

  it("list the triggers with when they fire and how the last firing went, and pause one", async () => {
    const server = fakeServer();
    server.triggers = [
      {
        id: "daily-digest",
        agent: "researcher",
        prompt: "Digest",
        schedule: "0 8 * * 1-5",
        paused: false,
        version: 1,
        next_fire_at: iso(Date.now() + 3_600_000),
        last_firing: { id: "f1", at: iso(Date.now()), state: "started", run_id: "r1" },
      },
      { id: "deploy", prompt: "Check", webhook: { path: "/v1/hooks/deploy" }, paused: true, version: 3 },
    ];
    renderAt("/tasks/triggers");
    const table = await screen.findByRole("table", { name: "Triggers" }, { timeout: 5000 });
    const rows = within(table).getAllByRole("row").slice(1);
    expect(rows[0]).toHaveTextContent("daily-digest");
    expect(rows[0]).toHaveTextContent("0 8 * * 1-5");
    expect(rows[0]).toHaveTextContent("Started a run");
    expect(within(rows[0]!).getByRole("link", { name: "r1" })).toHaveAttribute("href", "/tasks/r1");
    expect(rows[1]).toHaveTextContent("Paused");
    expect(rows[1]).toHaveTextContent("Never fired");
    await userEvent.click(within(rows[0]!).getByRole("button", { name: "Pause daily-digest" }));
    await waitFor(() =>
      expect(server.seen).toContainEqual({ method: "POST", path: "/v1/triggers/daily-digest/pause", after: null }),
    );
    expect(await within(rows[0]!).findByRole("button", { name: "Resume daily-digest" })).toBeInTheDocument();
    expect(rows[0]).toHaveTextContent("Paused");
  });
});
