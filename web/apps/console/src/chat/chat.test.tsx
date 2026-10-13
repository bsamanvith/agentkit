import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createMemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { App } from "../app";
import { routes } from "../routes";
import { dateGroup, withMeta } from "./shared";

declare global {
  interface Window {
    __xss?: number;
  }
}

type Ev = Record<string, unknown> & { type: string };

/**
 * A fake /v1 with conversations and streamed runs. Each POST /v1/runs takes
 * the next script: a list of events (sent at once), "stall" (sends run_start
 * and text, then waits until aborted), or an HTTP error.
 */
function fakeServer() {
  const state = {
    seen: [] as { method: string; path: string; body: unknown; signal?: AbortSignal | null }[],
    scripts: [] as (Ev[] | "stall" | { status: number; code: string; message?: string })[],
    conversations: new Map<string, { agent: string; status: string; messages: unknown[]; title: string }>(),
    aborted: 0,
    createError: undefined as { status: number; code: string } | undefined,
  };
  let next = 1;
  const json = (body: unknown, status = 200) => Response.json(body, { status });
  const error = (status: number, code: string, message = code) =>
    json({ v: 1, error: { code, message, retryable: false } }, status);
  const sse = (events: Ev[], stall: AbortSignal | undefined) => {
    const enc = new TextEncoder();
    const body = new ReadableStream<Uint8Array>({
      start(ctl) {
        events.forEach((e, i) =>
          ctl.enqueue(
            enc.encode(
              `id: ${i}\nevent: ${e.type}\ndata: ${JSON.stringify({ v: 1, seq: i, run_id: "run_1", ...e })}\n\n`,
            ),
          ),
        );
        if (!stall) ctl.close();
        else
          stall.addEventListener("abort", () => {
            state.aborted++;
            ctl.error(new DOMException("aborted", "AbortError"));
          });
      },
    });
    return new Response(body, { headers: { "Content-Type": "text/event-stream" } });
  };
  const fetch = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = new URL(String(input), "http://console.test");
    const method = init?.method ?? "GET";
    const body = typeof init?.body === "string" ? (JSON.parse(init.body) as Record<string, unknown>) : undefined;
    state.seen.push({ method, path: url.pathname, body, signal: init?.signal ?? null });
    const path = url.pathname;
    if (path === "/v1/agents") {
      return json({
        v: 1,
        agents: [
          { name: "researcher", description: "Finds things", default: true, toolsets: [], model: { id: "m" } },
          { name: "writer", description: "Writes", default: false, toolsets: [], model: { id: "m" } },
        ],
      });
    }
    if (path === "/v1/conversations" && method === "POST") {
      if (state.createError) return error(state.createError.status, state.createError.code);
      const id = `conv_${next++}`;
      state.conversations.set(id, {
        agent: String(body?.agent ?? "researcher"),
        status: "idle",
        messages: [],
        title: "",
      });
      return json({ v: 1, id, agent: body?.agent ?? "researcher", status: "idle", messages: [] }, 201);
    }
    if (path === "/v1/conversations") {
      const agent = url.searchParams.get("agent");
      const conversations = [...state.conversations]
        .filter(([, c]) => !agent || c.agent === agent)
        .map(([id, c]) => ({
          id,
          agent: c.agent,
          title: c.title,
          status: c.status,
          message_count: c.messages.length,
          created_at: new Date().toISOString(),
          updated_at: new Date().toISOString(),
        }));
      return json({ v: 1, conversations, next_cursor: "" });
    }
    const m = /^\/v1\/conversations\/(.+)$/.exec(path);
    if (m) {
      const c = state.conversations.get(decodeURIComponent(m[1] ?? ""));
      if (!c) return error(404, "not_found");
      if (method === "DELETE") return new Response(null, { status: 204 });
      return json({
        v: 1,
        id: m[1],
        agent: c.agent,
        status: c.status,
        created_at: "",
        updated_at: "",
        messages: c.messages,
      });
    }
    if (path === "/v1/runs" && method === "POST") {
      const script = state.scripts.shift() ?? [{ type: "run_start" }, { type: "run_done", answer: "" }];
      if (!Array.isArray(script) && script !== "stall") return error(script.status, script.code, script.message);
      const c = state.conversations.get(String(body?.conversation_id));
      if (c && body?.message) {
        c.messages.push({
          at: new Date().toISOString(),
          message: { role: "user", content: [{ type: "text", text: body.message }] },
        });
        c.title ||= String(body.message);
      }
      if (script === "stall") {
        return sse(
          [
            { type: "run_start", conversation_id: body?.conversation_id },
            { type: "text_delta", text: "Partial" },
          ],
          init?.signal ?? undefined,
        );
      }
      return sse(script, undefined);
    }
    return error(404, "not_found");
  });
  vi.stubGlobal("fetch", fetch);
  return state;
}

function renderAt(path: string) {
  const router = createMemoryRouter(routes, { initialEntries: [path] });
  render(<App router={router} />);
  return router;
}

const box = () => screen.getByRole("textbox", { name: "Message" });

beforeEach(() => {
  sessionStorage.clear();
  localStorage.clear();
  window.__xss = undefined;
});
afterEach(() => {
  vi.unstubAllGlobals();
  expect(window.__xss).toBeUndefined();
});

describe("a new chat", () => {
  it("creates the conversation, then streams the answer into it", async () => {
    const server = fakeServer();
    server.scripts.push([
      { type: "run_start", conversation_id: "conv_1" },
      { type: "tool_call", tool_use_id: "t1", name: "echo", input: { text: "hi" } },
      { type: "tool_result", tool_use_id: "t1", name: "echo", content: '{"text":"hi"}', duration_ms: 1200 },
      { type: "text_delta", text: "See **[Go](https://go.dev/doc)**." },
      { type: "run_done", answer: "", usage: { input_tokens: 10, output_tokens: 5 }, turns: 2 },
    ]);
    const router = renderAt("/new");
    await screen.findByRole("button", { name: "Agent: researcher" });
    await userEvent.type(box(), "Hello{Enter}");

    await waitFor(() => expect(router.state.location.pathname).toBe("/c/conv_1"));
    const posts = server.seen.filter((r) => r.method === "POST");
    expect(posts.map((r) => [r.path, r.body])).toEqual([
      ["/v1/conversations", { agent: "researcher" }],
      ["/v1/runs", { message: "Hello", conversation_id: "conv_1" }],
    ]);
    const thread = await screen.findByRole("log", { name: "Hello" });
    expect(await within(thread).findByRole("link", { name: "Go" })).toHaveAttribute("href", "https://go.dev/doc");
    expect(within(thread).getByRole("button", { name: /Ran echo/ })).toBeInTheDocument();
    expect(within(thread).getByText("2 turns · 15 tokens", { exact: false })).toBeInTheDocument();
    expect(within(thread).getByRole("list", { name: "Sources" })).toHaveTextContent("go.dev/doc");
    expect(screen.getByRole("heading", { level: 1, name: "Hello" })).toBeInTheDocument();
    // The list shows the new conversation.
    const list = screen.getByRole("navigation", { name: "Conversations" });
    expect(await within(list).findByRole("link", { name: /Hello/ })).toHaveAttribute("aria-current", "page");
    expect(box()).toHaveValue("");
  });

  it("uses the agent picked, and remembers it", async () => {
    const server = fakeServer();
    renderAt("/new?agent=writer");
    expect(await screen.findByRole("button", { name: "Agent: writer" })).toBeInTheDocument();
    await userEvent.type(box(), "Hi{Enter}");
    await waitFor(() => expect(server.seen.some((r) => r.path === "/v1/runs")).toBe(true));
    expect(server.seen.find((r) => r.path === "/v1/conversations" && r.method === "POST")?.body).toEqual({
      agent: "writer",
    });
    expect(localStorage.getItem("agentkit.agent")).toBe("writer");
  });

  it("keeps the message when the conversation can't be created", async () => {
    const server = fakeServer();
    server.createError = { status: 503, code: "shutting_down" };
    renderAt("/new");
    await screen.findByRole("button", { name: "Agent: researcher" });
    await userEvent.type(box(), "Not now{Enter}");
    expect(await screen.findByRole("alert")).toHaveTextContent("shutting_down");
    expect(box()).toHaveValue("Not now");
  });
});

/** A conversation with a message and an answer, at /c/conv_1. */
function withConversation(server: ReturnType<typeof fakeServer>, status = "idle", extra: unknown[] = []) {
  server.conversations.set("conv_1", {
    agent: "researcher",
    status,
    title: "First",
    messages: [
      { at: "2026-10-14T10:00:00Z", message: { role: "user", content: [{ type: "text", text: "First" }] } },
      { at: "2026-10-14T10:00:02Z", message: { role: "assistant", content: [{ type: "text", text: "An answer" }] } },
      ...extra,
    ],
  });
}

describe("a conversation", () => {
  it("shows its history, with tool steps and hostile content as text", async () => {
    const server = fakeServer();
    const hostile = `<img src=x onerror="window.__xss=1"><script>window.__xss=1</script>`;
    withConversation(server, "idle", [
      { at: "2026-10-14T10:01:00Z", message: { role: "user", content: [{ type: "text", text: "Echo" }] } },
      {
        at: "2026-10-14T10:01:01Z",
        message: {
          role: "assistant",
          content: [{ type: "tool_use", id: "t1", name: "echo", input: { text: hostile } }],
        },
      },
      {
        at: "2026-10-14T10:01:02Z",
        message: { role: "user", content: [{ type: "tool_result", tool_use_id: "t1", content: hostile }] },
      },
      {
        at: "2026-10-14T10:01:03Z",
        message: {
          role: "assistant",
          content: [{ type: "text", text: `${hostile} [x](javascript:window.__xss=1)` }],
        },
      },
    ]);
    renderAt("/c/conv_1");
    const thread = await screen.findByRole("log", { name: "First" });
    expect(await within(thread).findByText("An answer")).toBeInTheDocument();
    await userEvent.click(within(thread).getByRole("button", { name: /Ran echo/ }));
    expect(within(thread).getByLabelText("Result")).toHaveTextContent(hostile);
    expect(thread.querySelector("img, script, a[href^='javascript']")).toBeNull();
    await waitFor(() => expect(thread.querySelectorAll("a")).toHaveLength(0));
    expect(screen.getByRole("button", { name: "Agent: researcher" })).toBeInTheDocument();
  });

  it("sends a follow-up with the conversation's ID", async () => {
    const server = fakeServer();
    withConversation(server);
    server.scripts.push([
      { type: "run_start" },
      { type: "text_delta", text: "Second answer" },
      { type: "run_done", answer: "Second answer" },
    ]);
    renderAt("/c/conv_1");
    await screen.findByText("An answer");
    await userEvent.type(box(), "Again{Enter}");
    expect(await screen.findByText("Second answer")).toBeInTheDocument();
    expect(server.seen.find((r) => r.path === "/v1/runs")?.body).toEqual({
      message: "Again",
      conversation_id: "conv_1",
    });
    expect(within(screen.getByRole("log")).getByText("First")).toBeInTheDocument();
  });

  it("stops on Stop: the stream closes, the answer says so, and the composer is ready", async () => {
    const server = fakeServer();
    withConversation(server);
    server.scripts.push("stall");
    renderAt("/c/conv_1");
    await screen.findByText("An answer");
    await userEvent.type(box(), "Long one{Enter}");
    expect(await screen.findByText("Partial")).toBeInTheDocument();
    const run = server.seen.find((r) => r.path === "/v1/runs");
    expect(run?.signal?.aborted).toBe(false);

    await userEvent.click(screen.getByRole("button", { name: "Stop" }));
    expect(run?.signal?.aborted).toBe(true);
    expect(server.aborted).toBe(1);
    expect(await screen.findByText("Stopped. This part isn't saved.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Resume: let researcher continue" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Send" })).toBeInTheDocument();
    expect(screen.getByText("Partial")).toBeInTheDocument();
  });

  it("stops on Esc", async () => {
    const server = fakeServer();
    withConversation(server);
    server.scripts.push("stall");
    renderAt("/c/conv_1");
    await screen.findByText("An answer");
    await userEvent.type(box(), "Long one{Enter}");
    await screen.findByText("Partial");
    await userEvent.keyboard("{Escape}");
    expect(await screen.findByText("Stopped. This part isn't saved.")).toBeInTheDocument();
    expect(server.aborted).toBe(1);
  });

  it("keeps a message the server refused as interrupted, and Resume sends no message", async () => {
    const server = fakeServer();
    withConversation(server);
    server.scripts.push({ status: 409, code: "conversation_interrupted" }, [
      { type: "run_start" },
      { type: "run_done", answer: "" },
    ]);
    renderAt("/c/conv_1");
    await screen.findByText("An answer");
    await userEvent.type(box(), "Next{Enter}");
    expect(await screen.findByText(/The last answer didn't finish/)).toBeInTheDocument();
    expect(box()).toHaveValue("Next");
    expect(screen.queryByText("Next", { selector: "[data-role=user] *" })).toBeNull();

    await userEvent.click(screen.getByRole("button", { name: "Resume" }));
    await waitFor(() =>
      expect(server.seen.filter((r) => r.path === "/v1/runs").at(-1)?.body).toEqual({
        message: "",
        conversation_id: "conv_1",
      }),
    );
  });

  it("shows a run error inline, and Retry resumes", async () => {
    const server = fakeServer();
    withConversation(server);
    server.scripts.push([
      { type: "run_start" },
      { type: "text_delta", text: "Half" },
      { type: "run_error", error: { code: "overloaded", message: "overloaded", retryable: true } },
    ]);
    renderAt("/c/conv_1");
    await screen.findByText("An answer");
    await userEvent.type(box(), "Try{Enter}");
    expect(await screen.findByRole("alert")).toHaveTextContent("The model provider is overloaded right now.");
    await userEvent.click(screen.getByRole("button", { name: "Retry" }));
    await waitFor(() => expect(server.seen.filter((r) => r.path === "/v1/runs")).toHaveLength(2));
    expect(server.seen.filter((r) => r.path === "/v1/runs")[1]?.body).toEqual({
      message: "",
      conversation_id: "conv_1",
    });
  });

  it("puts a message that got a 401 back in the box and asks for the token", async () => {
    const server = fakeServer();
    withConversation(server);
    server.scripts.push({ status: 401, code: "unauthorized" });
    renderAt("/c/conv_1");
    await screen.findByText("An answer");
    await userEvent.type(box(), "Secret{Enter}");
    expect(await screen.findByLabelText("API token")).toBeInTheDocument();
    expect(box()).toHaveValue("Secret");
  });

  it("keeps drafts per conversation, in sessionStorage", async () => {
    const server = fakeServer();
    withConversation(server);
    const router = renderAt("/c/conv_1");
    await screen.findByText("An answer");
    await userEvent.type(box(), "Half-typed");
    expect(sessionStorage.getItem("agentkit.draft.conv_1")).toBe("Half-typed");
    await act(() => router.navigate("/new"));
    await waitFor(() => expect(box()).toHaveValue(""));
    await act(() => router.navigate("/c/conv_1"));
    await waitFor(() => expect(box()).toHaveValue("Half-typed"));
  });

  it("goes on while you look elsewhere, and is still there when you come back", async () => {
    const server = fakeServer();
    withConversation(server);
    server.scripts.push("stall");
    const router = renderAt("/c/conv_1");
    await screen.findByText("An answer");
    await userEvent.type(box(), "Long one{Enter}");
    await screen.findByText("Partial");
    await act(() => router.navigate("/agents"));
    expect(server.aborted).toBe(0);
    await act(() => router.navigate("/c/conv_1"));
    expect(await screen.findByText("Partial")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Stop" })).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Stop" }));
    expect(server.aborted).toBe(1);
  });

  it("says when it doesn't exist", async () => {
    fakeServer();
    renderAt("/c/nope");
    expect(await screen.findByRole("heading", { level: 1, name: "Not found" })).toBeInTheDocument();
  });

  it("is read-only when its agent is gone", async () => {
    const server = fakeServer();
    withConversation(server);
    const c = server.conversations.get("conv_1");
    if (c) c.agent = "retired";
    renderAt("/c/conv_1");
    expect(await screen.findByRole("button", { name: "Agent: retired (removed)" })).toBeInTheDocument();
    expect(box()).toBeDisabled();
  });
});

describe("the Chats list", () => {
  it("lists conversations with messages, filters by agent and searches loaded rows", async () => {
    const server = fakeServer();
    withConversation(server);
    server.conversations.set("conv_2", { agent: "writer", status: "interrupted", title: "Poem", messages: [{}] });
    server.conversations.set("conv_3", { agent: "writer", status: "idle", title: "", messages: [] });
    renderAt("/new");
    const list = await screen.findByRole("navigation", { name: "Conversations" });
    await within(list).findByRole("link", { name: /First/ });
    expect(within(list).getByRole("link", { name: /Poem/ })).toHaveTextContent("Interrupted");
    expect(within(list).getAllByRole("link")).toHaveLength(2); // the empty one isn't listed
    expect(within(list).getByRole("heading", { name: "Today" })).toBeInTheDocument();

    await userEvent.type(screen.getByRole("searchbox", { name: "Search loaded chats" }), "poe");
    expect(within(list).getAllByRole("link")).toHaveLength(1);
    await userEvent.clear(screen.getByRole("searchbox", { name: "Search loaded chats" }));

    await userEvent.selectOptions(screen.getByRole("combobox", { name: "Agent" }), "writer");
    await waitFor(() => expect(within(list).queryByRole("link", { name: /First/ })).toBeNull());
    expect(server.seen.some((r) => r.path === "/v1/conversations" && r.method === "GET")).toBe(true);
  });

  it("focuses search on Mod+K, and Mod+Shift+O opens a new chat", async () => {
    fakeServer();
    const router = renderAt("/agents");
    await screen.findByRole("heading", { level: 1, name: "researcher" });
    await userEvent.keyboard("{Control>}k{/Control}");
    await waitFor(() => expect(screen.getByRole("searchbox", { name: "Search loaded chats" })).toHaveFocus());
    await userEvent.keyboard("{Control>}{Shift>}O{/Shift}{/Control}");
    await waitFor(() => expect(router.state.location.pathname).toBe("/new"));
    await waitFor(() => expect(box()).toHaveFocus());
  });
});

describe("dateGroup", () => {
  const now = new Date(2026, 9, 14, 12);
  it.each([
    [new Date(2026, 9, 14, 1), "Today"],
    [new Date(2026, 9, 13, 23), "Yesterday"],
    [new Date(2026, 9, 9), "Previous 7 days"],
    [new Date(2026, 9, 1), "Older"],
  ])("%s", (d, want) => expect(dateGroup(d.toISOString(), now)).toBe(want));
  it("is Older for a bad date", () => expect(dateGroup("x", now)).toBe("Older"));
});

describe("withMeta", () => {
  it("adds this tab's usage to the history's answers, and an answer for a run that failed silent", () => {
    const turns = withMeta(
      [
        { role: "user", key: "u1", text: "a" },
        { role: "assistant", key: "a1", parts: [], state: "done" },
        { role: "user", key: "u2", text: "b" },
      ],
      new Map([
        [1, { state: "done" as const, turns: 2 }],
        [2, { state: "failed" as const, error: { code: "overloaded", message: "x" } }],
      ]),
    );
    expect(turns.map((t) => (t.role === "assistant" ? `${t.state}:${t.turns ?? ""}` : t.text))).toEqual([
      "a",
      "done:2",
      "b",
      "failed:",
    ]);
  });
});
