// The /v1 wire types the web apps read (pkg/agentapi/wire; docs/api/openapi.yaml).
//
// Decoding is lenient, as in the Go client: unknown fields are kept but
// ignored, an event of an unknown type is passed on (never an error), and a
// message block of an unknown type is kept as an UnknownBlock to show as a
// placeholder. Only the fields the apps use are typed; add more as pages
// need them.

/** The event types a run streams. New types may be added: handle `string`. */
export type EventType =
  "run_start" | "text_delta" | "tool_call" | "tool_result" | "turn_done" | "run_done" | "run_error";

/** A run error, in a run_error event or an HTTP error body. */
export type WireError = {
  code: string;
  message: string;
  retryable?: boolean;
  category?: string;
  explanation?: string;
};

export type Usage = {
  input_tokens?: number;
  output_tokens?: number;
  cache_read_input_tokens?: number;
  cache_creation_input_tokens?: number;
  [field: string]: unknown;
};

export type TextBlock = { type: "text"; text: string };
export type ToolUseBlock = { type: "tool_use"; id: string; name: string; input: unknown };
export type ToolResultBlock = { type: "tool_result"; tool_use_id: string; content: string; is_error?: boolean };
/** A block of a type this client doesn't know: show it as a placeholder. */
export type UnknownBlock = { type: string; unknown: true; raw: unknown };
export type Block = TextBlock | ToolUseBlock | ToolResultBlock | UnknownBlock;

export type Message = { role: "user" | "assistant" | string; content: Block[] };

/**
 * One event of a run. Every event has v, type, id, seq and run_id; the other
 * fields depend on the type (pkg/agentapi/wire.Event).
 */
export type RunEvent = {
  v: number;
  type: EventType | (string & {});
  /** Orders the run's events, and is the SSE id: "12" for a stored event, "12.3" for a text_delta after it. */
  id: string;
  /** The stored events' count from 1; a text_delta has the seq of the stored event before it. */
  seq: number;
  run_id: string;
  at?: string;
  conversation_id?: string;
  turn?: number;
  block?: number;
  text?: string;
  call?: number;
  tool_use_id?: string;
  name?: string;
  input?: unknown;
  content?: string;
  is_error?: boolean;
  duration_ms?: number;
  answer?: string;
  final?: Message;
  usage?: Usage;
  turns?: number;
  error?: WireError;
  [field: string]: unknown;
};

export type AgentToolset = { name: string; status: string; tools: string[] };

export type Agent = {
  name: string;
  description: string;
  default: boolean;
  model: { provider?: string; id: string; effort?: string };
  toolsets: AgentToolset[];
  skills: string[];
  memory: boolean;
  system?: string;
};

export type ConversationSummary = {
  id: string;
  agent: string;
  title: string;
  status: string;
  message_count: number;
  created_at: string;
  updated_at: string;
};

/** A message of a conversation, with when it was added. */
export type TimedMessage = { at: string; message: Message };

/** GET /v1/conversations/{id}: the conversation and every message the model has seen. */
export type Conversation = {
  id: string;
  agent: string;
  status: string;
  created_at: string;
  updated_at: string;
  messages: TimedMessage[];
};

/** GET /v1/runs/{id}. */
export type Run = {
  id: string;
  conversation_id: string;
  agent: string;
  status: string;
  created_at: string;
  finished_at?: string;
  answer?: string;
  usage?: Usage;
  turns?: number;
  error?: WireError;
};

export type RunRequest = {
  message: string;
  conversation_id?: string;
  agent?: string;
};

function isObject(v: unknown): v is Record<string, unknown> {
  return typeof v === "object" && v !== null && !Array.isArray(v);
}

/** Decodes one block, keeping an unknown or malformed one as UnknownBlock. */
export function decodeBlock(raw: unknown): Block {
  if (isObject(raw)) {
    switch (raw.type) {
      case "text":
        if (typeof raw.text === "string") return { type: "text", text: raw.text };
        break;
      case "tool_use":
        if (typeof raw.id === "string" && typeof raw.name === "string")
          return { type: "tool_use", id: raw.id, name: raw.name, input: raw.input };
        break;
      case "tool_result":
        if (typeof raw.tool_use_id === "string")
          return {
            type: "tool_result",
            tool_use_id: raw.tool_use_id,
            content: typeof raw.content === "string" ? raw.content : "",
            is_error: raw.is_error === true,
          };
        break;
    }
  }
  return { type: isObject(raw) && typeof raw.type === "string" ? raw.type : "unknown", unknown: true, raw };
}

/** Decodes a message leniently; undefined when it isn't one at all. */
export function decodeMessage(raw: unknown): Message | undefined {
  if (!isObject(raw) || typeof raw.role !== "string") return undefined;
  const content = Array.isArray(raw.content) ? raw.content.map(decodeBlock) : [];
  return { role: raw.role, content };
}

/** Decodes a conversation leniently: messages that aren't messages are dropped. */
export function decodeConversation(raw: unknown): Conversation | undefined {
  if (!isObject(raw) || typeof raw.id !== "string") return undefined;
  const str = (v: unknown) => (typeof v === "string" ? v : "");
  const messages: TimedMessage[] = [];
  if (Array.isArray(raw.messages)) {
    for (const m of raw.messages) {
      const message = isObject(m) ? decodeMessage(m.message) : undefined;
      if (message && isObject(m)) messages.push({ at: str(m.at), message });
    }
  }
  return {
    id: raw.id,
    agent: str(raw.agent),
    status: str(raw.status),
    created_at: str(raw.created_at),
    updated_at: str(raw.updated_at),
    messages,
  };
}

/**
 * Decodes one event's data. Returns undefined when the data is not an event
 * (not JSON, not an object, or no type): the stream is then broken.
 */
export function decodeEvent(data: string): RunEvent | undefined {
  let raw: unknown;
  try {
    raw = JSON.parse(data);
  } catch {
    return undefined;
  }
  if (!isObject(raw) || typeof raw.type !== "string" || raw.type === "") return undefined;
  const ev = raw as RunEvent;
  if (raw.final !== undefined) {
    const final = decodeMessage(raw.final);
    if (final) ev.final = final;
    else delete ev.final;
  }
  return ev;
}

/** Whether the event ends its run's stream. */
export function isTerminal(ev: RunEvent): boolean {
  return ev.type === "run_done" || ev.type === "run_error";
}
