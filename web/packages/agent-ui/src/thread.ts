// What a chat thread shows, built from a conversation's messages (history)
// and from a run's events (live). Pure functions, so the console's run store
// and any other app read runs the same way (docs/UI.md "Chat", "Tool calls").
//
// Untrusted content stays data here: text, tool inputs and results are
// strings and values, never markup.

import type { Block, Message, RunEvent, TimedMessage, Usage, WireError } from "./api/types";

/** A tool call: running until its result, then done or failed; stopped when the run stopped first. */
export type Step = {
  id: string;
  name: string;
  input: unknown;
  state: "running" | "done" | "failed" | "stopped";
  /** What the model was shown (tool_result content). */
  result?: string;
  durationMs?: number;
  /** The step's line when it is not a tool call: "Loaded 2 tools". */
  label?: string;
};

export type Part =
  | { kind: "text"; text: string }
  /** Consecutive tool calls with no text between them. */
  | { kind: "tools"; steps: Step[] }
  /** A block of a type this client doesn't know: shown as a placeholder. */
  | { kind: "unknown"; type: string };

export type UserTurn = { role: "user"; key: string; text: string; pending?: boolean };

export type AssistantTurn = {
  role: "assistant";
  key: string;
  parts: Part[];
  /**
   * streaming: the run goes on; done: it answered; failed: run_error (see
   * error); stopped: the user stopped it; lost: the stream broke.
   */
  state: "streaming" | "done" | "failed" | "stopped" | "lost";
  runId?: string;
  usage?: Usage;
  turns?: number;
  durationMs?: number;
  error?: WireError;
  /** Wall-clock start, for the duration of a live run. */
  startedAt?: number;
};

export type Turn = UserTurn | AssistantTurn;

/** The text of a message's text blocks, joined. */
function textOf(blocks: Block[]): string {
  return blocks
    .filter((b): b is Extract<Block, { type: "text" }> => b.type === "text")
    .map((b) => b.text)
    .join("");
}

/** Adds text to the parts, joining it to text just before (deltas of one block). */
function addText(parts: Part[], text: string): Part[] {
  if (text === "") return parts;
  const last = parts[parts.length - 1];
  if (last?.kind === "text") return [...parts.slice(0, -1), { kind: "text", text: last.text + text }];
  return [...parts, { kind: "text", text }];
}

/** Adds a running step to the last group, or starts one. */
function addStep(parts: Part[], step: Step): Part[] {
  const last = parts[parts.length - 1];
  if (last?.kind === "tools") return [...parts.slice(0, -1), { kind: "tools", steps: [...last.steps, step] }];
  return [...parts, { kind: "tools", steps: [step] }];
}

/** Applies f to the step with this ID, wherever it is. */
function updateStep(parts: Part[], id: string, f: (s: Step) => Step): Part[] {
  return parts.map((p) =>
    p.kind === "tools" && p.steps.some((s) => s.id === id)
      ? { kind: "tools", steps: p.steps.map((s) => (s.id === id ? f(s) : s)) }
      : p,
  );
}

/** Every running step marked stopped: the run ended before their results. */
function stopRunning(parts: Part[]): Part[] {
  return parts.map((p) =>
    p.kind === "tools" && p.steps.some((s) => s.state === "running")
      ? { kind: "tools", steps: p.steps.map((s) => (s.state === "running" ? { ...s, state: "stopped" } : s)) }
      : p,
  );
}

const ms = (at: string) => {
  const t = Date.parse(at);
  return Number.isNaN(t) ? undefined : t;
};

/**
 * The thread of a conversation's history. A user message with text starts an
 * exchange; the assistant messages after it (and the tool results between
 * them) are one answer. A message the model saw only as tool results never
 * shows as a user bubble. Times come from the messages' timestamps.
 */
export function threadFromHistory(messages: TimedMessage[]): Turn[] {
  const turns: Turn[] = [];
  let answer: AssistantTurn | undefined;
  let askedAt: number | undefined;
  messages.forEach(({ at, message }: { at: string; message: Message }, i) => {
    const key = `h${i}`;
    if (message.role === "user") {
      for (const b of message.content) {
        if ("unknown" in b) continue;
        if (b.type === "tool_result" && answer) {
          answer.parts = updateStep(answer.parts, b.tool_use_id, (s) => ({
            ...s,
            state: b.is_error ? "failed" : "done",
            result: b.content,
          }));
        }
      }
      const text = textOf(message.content);
      if (text !== "") {
        answer = undefined;
        askedAt = ms(at);
        turns.push({ role: "user", key, text });
      }
      return;
    }
    if (!answer) {
      answer = { role: "assistant", key, parts: [], state: "done" };
      turns.push(answer);
    }
    for (const b of message.content) {
      if ("unknown" in b) answer.parts = [...answer.parts, { kind: "unknown", type: b.type }];
      else if (b.type === "text") answer.parts = addText(answer.parts, b.text);
      else if (b.type === "tool_use")
        answer.parts = addStep(answer.parts, { id: b.id, name: b.name, input: b.input, state: "running" });
    }
    const end = ms(at);
    if (askedAt !== undefined && end !== undefined && end >= askedAt) answer.durationMs = end - askedAt;
  });
  // A call whose result never came: the run stopped there.
  for (const t of turns) if (t.role === "assistant") t.parts = stopRunning(t.parts);
  return turns;
}

/** A new answer, streaming, for a run just sent. */
export function startAnswer(key: string, now: number): AssistantTurn {
  return { role: "assistant", key, parts: [], state: "streaming", startedAt: now };
}

/**
 * The answer after one event of its run. Unknown event types change
 * nothing (the API only grows); events after the run ended are ignored.
 */
export function applyEvent(a: AssistantTurn, ev: RunEvent, now: number): AssistantTurn {
  if (a.state !== "streaming") return a;
  const took = a.startedAt !== undefined ? now - a.startedAt : undefined;
  switch (ev.type) {
    case "run_start":
      return { ...a, runId: ev.run_id };
    case "text_delta":
      return typeof ev.text === "string" ? { ...a, parts: addText(a.parts, ev.text) } : a;
    case "tool_call":
      if (typeof ev.tool_use_id !== "string") return a;
      return {
        ...a,
        parts: addStep(a.parts, { id: ev.tool_use_id, name: ev.name ?? "tool", input: ev.input, state: "running" }),
      };
    case "tool_result": {
      if (typeof ev.tool_use_id !== "string") return a;
      const result = typeof ev.content === "string" ? ev.content : "";
      return {
        ...a,
        parts: updateStep(a.parts, ev.tool_use_id, (s) => ({
          ...s,
          state: ev.is_error ? "failed" : "done",
          result,
          ...(typeof ev.duration_ms === "number" ? { durationMs: ev.duration_ms } : {}),
        })),
      };
    }
    case "turn_done": {
      // Deferred tools the turn loaded (a tool search, a skill): a step of its own.
      const rec = ev.record as { loaded?: unknown } | undefined;
      const loaded = Array.isArray(rec?.loaded) ? rec.loaded.filter((n): n is string => typeof n === "string") : [];
      if (loaded.length === 0) return a;
      const n = loaded.length;
      return {
        ...a,
        parts: addStep(a.parts, {
          id: `loaded-${ev.id}`,
          name: "tools_loaded",
          input: { tools: loaded },
          state: "done",
          label: `Loaded ${n} ${n === 1 ? "tool" : "tools"}`,
        }),
      };
    }
    case "run_done":
      return { ...a, state: "done", usage: ev.usage, turns: ev.turns, durationMs: took, parts: stopRunning(a.parts) };
    case "run_error":
      return {
        ...a,
        state: "failed",
        error: ev.error ?? { code: "unknown", message: "The run failed." },
        usage: ev.usage,
        turns: ev.turns,
        durationMs: took,
        parts: stopRunning(a.parts),
      };
    default:
      return a;
  }
}

/** The answer after the run ended without its end event: stopped by the user, or the stream was lost. */
export function endAnswer(a: AssistantTurn, how: "stopped" | "lost", now: number): AssistantTurn {
  if (a.state !== "streaming") return a;
  return {
    ...a,
    state: how,
    parts: stopRunning(a.parts),
    durationMs: a.startedAt !== undefined ? now - a.startedAt : undefined,
  };
}

/** The answer's text, all its text parts joined (source chips come from it). */
export function answerText(a: AssistantTurn): string {
  return a.parts
    .filter((p): p is Extract<Part, { kind: "text" }> => p.kind === "text")
    .map((p) => p.text)
    .join("\n\n");
}

/**
 * The http(s) links in an answer's Markdown, once each, in order: explicit
 * links and bare URLs (docs/UI.md: tool results alone don't make chips).
 */
export function sourcesOf(markdown: string): string[] {
  const seen = new Set<string>();
  const out: string[] = [];
  for (const m of markdown.matchAll(/\]\((https?:\/\/[^\s)]+)\)|<?(https?:\/\/[^\s<>()[\]"']+[^\s<>()[\]"'.,;:!?])/g)) {
    const raw = m[1] ?? m[2];
    if (!raw) continue;
    let u: URL;
    try {
      u = new URL(raw);
    } catch {
      continue;
    }
    if (!seen.has(u.href)) {
      seen.add(u.href);
      out.push(u.href);
    }
  }
  return out;
}
