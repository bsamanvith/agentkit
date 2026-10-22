import { describe, expect, it } from "vitest";
import { decodeEvent, decodeMessage, type RunEvent, type TimedMessage } from "./api/types";
import {
  applyEvent,
  endAnswer,
  promptOf,
  sourcesOf,
  startAnswer,
  threadFromHistory,
  type AssistantTurn,
  type Part,
  type Step,
  type Turn,
} from "./thread";

const ev = (e: Partial<RunEvent> & { type: string }): RunEvent => ({ v: 1, id: "1", seq: 1, run_id: "run_1", ...e });

/** Plays events on a fresh answer, checking the invariants after each one. */
function play(events: RunEvent[], end?: "stopped" | "lost"): AssistantTurn {
  let a = startAnswer("a", 1000);
  events.forEach((e, i) => {
    const before = a;
    const copy = structuredClone(a);
    a = applyEvent(a, e, 1000 + (i + 1) * 100);
    invariants(a);
    // Never edits the answer it was given: React compares by identity.
    expect(before).toEqual(copy);
  });
  if (end) a = endAnswer(a, end, 9000);
  invariants(a);
  return a;
}

const steps = (parts: Part[]): Step[] => parts.flatMap((p) => (p.kind === "tools" ? p.steps : []));

/** What holds for every answer: unique steps, no empty text, nothing running once it ended. */
function invariants(a: AssistantTurn) {
  const ids = steps(a.parts).map((s) => s.id);
  expect(new Set(ids).size).toBe(ids.length);
  for (const p of a.parts) {
    if (p.kind === "text") expect(p.text).not.toBe("");
    if (p.kind === "tools") expect(p.steps.length).toBeGreaterThan(0);
  }
  // Two text parts never touch: deltas join.
  a.parts.forEach((p, i) => {
    if (i > 0) expect(p.kind === "text" && a.parts[i - 1]?.kind === "text").toBe(false);
  });
  if (a.state !== "streaming") expect(steps(a.parts).filter((s) => s.state === "running")).toEqual([]);
}

describe("a run's events", () => {
  it("stream text, then tool calls as one group, then the answer", () => {
    const a = play([
      ev({ type: "run_start", conversation_id: "conv_1" }),
      ev({ type: "text_delta", text: "Let me " }),
      ev({ type: "text_delta", text: "check." }),
      ev({ type: "tool_call", tool_use_id: "t1", name: "echo", input: { text: "hi" } }),
      ev({ type: "tool_call", tool_use_id: "t2", name: "calculate", input: { op: "add", a: 1, b: 2 } }),
      ev({ type: "tool_result", tool_use_id: "t2", content: '{"result":3}', duration_ms: 12 }),
      ev({ type: "tool_result", tool_use_id: "t1", content: "boom", is_error: true, duration_ms: 30 }),
      ev({ type: "turn_done", turn: 1 }),
      ev({ type: "text_delta", text: "Done." }),
      ev({ type: "run_done", answer: "Done.", turns: 2, usage: { input_tokens: 10, output_tokens: 5 } }),
    ]);
    expect(a.state).toBe("done");
    expect(a.runId).toBe("run_1");
    expect(a.turns).toBe(2);
    expect(a.usage).toEqual({ input_tokens: 10, output_tokens: 5 });
    expect(a.durationMs).toBe(1000);
    expect(a.parts).toEqual([
      { kind: "text", text: "Let me check." },
      {
        kind: "tools",
        steps: [
          { id: "t1", name: "echo", input: { text: "hi" }, state: "failed", result: "boom", durationMs: 30 },
          {
            id: "t2",
            name: "calculate",
            input: { op: "add", a: 1, b: 2 },
            state: "done",
            result: '{"result":3}',
            durationMs: 12,
          },
        ],
      },
      { kind: "text", text: "Done." },
    ]);
  });

  it("show the tools a turn loaded as a step after its calls", () => {
    const a = play([
      ev({ type: "tool_call", tool_use_id: "t1", name: "tool_search", input: { query: "weather" } }),
      ev({ type: "tool_result", tool_use_id: "t1", content: "" }),
      ev({ id: "4", type: "turn_done", turn: 1, record: { loaded: ["weather_now", "weather_week"] } }),
      ev({ id: "5", type: "turn_done", turn: 2, record: { loaded: [] } }),
      ev({ type: "run_done" }),
    ]);
    expect(steps(a.parts).map((s) => s.label ?? s.name)).toEqual(["tool_search", "Loaded 2 tools"]);
    expect(steps(a.parts)[1]).toMatchObject({ state: "done", input: { tools: ["weather_now", "weather_week"] } });
  });

  it("ignore event types this client doesn't know, and keep going", () => {
    const a = play([
      ev({ type: "run_start" }),
      ev({ type: "something_new", text: "not shown" }),
      ev({ type: "text_delta", text: "Hi" }),
      ev({ type: "run_done" }),
    ]);
    expect(a.parts).toEqual([{ kind: "text", text: "Hi" }]);
    expect(a.state).toBe("done");
  });

  it("end in an error, with running steps stopped and the text kept", () => {
    const a = play([
      ev({ type: "text_delta", text: "Partial" }),
      ev({ type: "tool_call", tool_use_id: "t1", name: "echo", input: {} }),
      ev({ type: "run_error", error: { code: "overloaded", message: "busy", retryable: true } }),
    ]);
    expect(a.state).toBe("failed");
    expect(a.error).toEqual({ code: "overloaded", message: "busy", retryable: true });
    expect(steps(a.parts)[0]?.state).toBe("stopped");
    expect(a.parts[0]).toEqual({ kind: "text", text: "Partial" });
  });

  it("stopped by the user keep the partial answer and stop the running step", () => {
    const a = play(
      [ev({ type: "text_delta", text: "Working" }), ev({ type: "tool_call", tool_use_id: "t1", name: "echo" })],
      "stopped",
    );
    expect(a.state).toBe("stopped");
    expect(a.parts[0]).toEqual({ kind: "text", text: "Working" });
    expect(steps(a.parts)[0]?.state).toBe("stopped");
  });

  it("change nothing after the run ended", () => {
    const done = play([ev({ type: "text_delta", text: "A" }), ev({ type: "run_done" })]);
    expect(applyEvent(done, ev({ type: "text_delta", text: "B" }), 0)).toBe(done);
    expect(endAnswer(done, "lost", 0)).toBe(done);
  });

  it("decoded from the wire keep a tool result's markup as text", () => {
    const e = decodeEvent(
      JSON.stringify({
        v: 1,
        type: "tool_result",
        seq: 3,
        run_id: "r",
        tool_use_id: "t1",
        content: "<script>x</script>",
      }),
    )!;
    const a = play([ev({ type: "tool_call", tool_use_id: "t1", name: "echo" }), e]);
    expect(steps(a.parts)[0]?.result).toBe("<script>x</script>");
  });
});

const msg = (at: string, raw: unknown): TimedMessage => ({ at, message: decodeMessage(raw)! });

describe("a conversation's history", () => {
  const history = [
    msg("2026-10-14T10:00:00Z", { role: "user", content: [{ type: "text", text: "echo hi" }] }),
    msg("2026-10-14T10:00:01Z", {
      role: "assistant",
      content: [
        { type: "text", text: "Sure." },
        { type: "tool_use", id: "t1", name: "echo", input: { text: "hi" } },
      ],
    }),
    msg("2026-10-14T10:00:01Z", {
      role: "user",
      content: [{ type: "tool_result", tool_use_id: "t1", content: '{"text":"hi"}' }],
    }),
    msg("2026-10-14T10:00:03Z", { role: "assistant", content: [{ type: "text", text: "It said hi." }] }),
    msg("2026-10-14T10:01:00Z", { role: "user", content: [{ type: "text", text: "again" }] }),
    msg("2026-10-14T10:01:02Z", {
      role: "assistant",
      content: [
        { type: "tool_use", id: "t2", name: "echo", input: { text: "x" } },
        { type: "mystery", data: 1 },
      ],
    }),
  ];

  it("is one bubble per user text and one answer per exchange, with results on their steps", () => {
    const turns: Turn[] = threadFromHistory(history);
    expect(turns.map((t) => t.role)).toEqual(["user", "assistant", "user", "assistant"]);
    const [, first, , second] = turns as [Turn, AssistantTurn, Turn, AssistantTurn];
    expect(first.parts).toEqual([
      { kind: "text", text: "Sure." },
      {
        kind: "tools",
        steps: [{ id: "t1", name: "echo", input: { text: "hi" }, state: "done", result: '{"text":"hi"}' }],
      },
      { kind: "text", text: "It said hi." },
    ]);
    expect(first.durationMs).toBe(3000);
    // A call with no result (the run stopped there) and an unknown block.
    expect(second.parts).toEqual([
      { kind: "tools", steps: [{ id: "t2", name: "echo", input: { text: "x" }, state: "stopped" }] },
      { kind: "unknown", type: "mystery" },
    ]);
    for (const t of turns) if (t.role === "assistant") invariants(t);
  });

  it("of a run that stopped before answering ends with the user's message", () => {
    const turns = threadFromHistory(history.slice(0, 1));
    expect(turns).toEqual([{ role: "user", key: "h0", text: "echo hi" }]);
  });
});

describe("sources", () => {
  it("are the http(s) links of the answer, once each, never javascript:", () => {
    expect(
      sourcesOf(
        "See [Go](https://go.dev/doc/go1.27) and https://go.dev/doc/go1.27, also <https://example.com/a>. " +
          "[x](javascript:alert(1)) ftp://x.org/y and (https://pkg.go.dev/net/http).",
      ),
    ).toEqual(["https://go.dev/doc/go1.27", "https://example.com/a", "https://pkg.go.dev/net/http"]);
  });
});

describe("promptOf", () => {
  const start = (prompt: unknown) => ev({ type: "run_start", prompt });
  it("is the run_start's prompt as a user turn", () => {
    const p = { at: "2026-10-22T10:00:00Z", message: { role: "user", content: [{ type: "text", text: "Summarize" }] } };
    expect(promptOf(start(p), "k")).toEqual({ role: "user", key: "k", text: "Summarize" });
  });
  it.each([
    ["no prompt", undefined],
    ["not a message", { message: 7 }],
    ["no text (a resume)", { message: { role: "user", content: [] } }],
    ["the model's", { message: { role: "assistant", content: [{ type: "text", text: "hi" }] } }],
  ])("is undefined for %s", (_, p) => {
    expect(promptOf(start(p), "k")).toBeUndefined();
  });
  it("is undefined for another event", () => {
    expect(promptOf(ev({ type: "turn_done", prompt: {} }), "k")).toBeUndefined();
  });
});
