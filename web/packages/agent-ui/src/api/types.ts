// The /v1 wire types the web apps read (pkg/agentapi/wire; docs/api/openapi.yaml).
//
// Decoding is lenient, as in the Go client: unknown fields are kept but
// ignored, an event of an unknown type is passed on (never an error), and a
// message block of an unknown type is kept as an UnknownBlock to show as a
// placeholder. Only the fields the apps use are typed; add more as pages
// need them.

/** The event types a run streams. New types may be added: handle `string`. */
export type EventType =
  "run_start" | "text_delta" | "tool_call" | "tool_result" | "turn_done" | "run_done" | "run_error" | "ui";

/** A run error, in a run_error event or an HTTP error body. */
export type WireError = {
  code: string;
  message: string;
  retryable?: boolean;
  category?: string;
  explanation?: string;
  /** auth_required: the sign-in the MCP server asks for, "oauth" (MCP OAuth) or "headers". */
  auth?: string;
  /** invalid_definition: the fields that need a fix, by their path in the definition ("models.allow[0]"). */
  fields?: FieldError[];
};

/** One field of a refused definition: its path ("" for the whole definition) and what is wrong. */
export type FieldError = { field?: string; message: string };

/** GET /v1/auth/config: how to sign in ("password", "token"; none on an open server). */
export type AuthConfig = { methods: string[] };

/** GET /v1/auth/me: who the caller is. */
export type Me = {
  username?: string;
  tenant: string;
  /** A signed-in user's roles ("user", "admin"); empty for a token. */
  roles: string[];
  scopes: string[];
  /** "session", "api_key", "jwt"; absent on a server without credentials. */
  credential?: string;
  /** A session's CSRF token, which AgentClient sends on its other requests. */
  csrf?: string;
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
  /** run_start: the message the run started from, when the server has it ({at, message}, undecoded). */
  prompt?: unknown;
  /** run_start: the model the run runs, "<provider>/<id>" (T125). */
  model?: string;
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
  /** ui: the A2UI surface, and the A2UI message (v0.9) about it, as the server checked it. */
  surface_id?: string;
  message?: unknown;
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
  /** "a2ui": the agent shows A2UI surfaces (ui events) and its runs take ui_action. */
  ui?: string;
  /** A stored agent's published version; absent for an agent of the config file. */
  version?: number;
  /** An agent of the config file, on a server that keeps agents: the API can't change it. */
  read_only?: boolean;
  /** The models the agent may run, which a run picks with RunRequest.model (T125). */
  models?: AgentModels;
  /** Admins: the descriptions the agent gives its tools instead of their own (T126). */
  tool_descriptions?: Record<string, string>;
};

/** An agent's models (pkg/agentapi/wire.AgentModels): names are "<provider>/<id>". */
export type AgentModels = {
  default: string;
  /** The models allowed by name, the default first. */
  allow: string[];
  allow_patterns?: string[];
  /** What a picker offers: the default, the names allowed, then the known models a pattern allows. */
  choices: string[];
};

/** GET /v1/models. */
export type ModelList = {
  v: number;
  default: string;
  providers: { name: string; models: string[] }[];
};

/** GET /v1/agents/{name}/versions: one version of a stored agent. It never changes. */
export type AgentVersion = {
  name: string;
  version: number;
  published: boolean;
  /** The agent as saved: an entry of "agents" in the serve config file. */
  definition: Record<string, unknown>;
  author?: string;
  message?: string;
  created_at: string;
};

/** GET /v1/agents/{name}/versions. */
export type AgentVersionList = {
  /** An agent of the config file: it has no stored versions (change it in the file). */
  read_only?: boolean;
  /** The highest version number given so far: what if_latest compares with, even after that draft was deleted. */
  latest: number;
  /** The version new runs use; 0 for none. */
  published: number;
  versions: AgentVersion[];
};

/** POST /v1/agents/{name}/versions. */
export type SaveAgentRequest = {
  definition: Record<string, unknown>;
  message?: string;
  /** Save only if the latest version is this one (409 conflict otherwise). */
  if_latest?: number;
  publish?: boolean;
};

/** POST /v1/toolsets: an MCP server to add by URL. Header values are ${VAR} references, never secrets. */
export type AddToolsetRequest = {
  type: "mcp";
  name: string;
  url: string;
  /** "auto" (the default), "http" (Streamable HTTP) or "sse". */
  transport?: string;
  /** "none" (the default), "headers" or "oauth" (MCP OAuth: the tools are listed once an account is connected). */
  auth?: string;
  headers?: Record<string, string>;
  /** "tenant" (the default): one shared connection; "user", with auth "oauth": each person connects their own. */
  scope?: string;
};

/** A tool's definition as its MCP server sends it, raw: hostile text, shown only as text. */
export type ToolDefinition = {
  title?: string;
  description?: string;
  input_schema?: unknown;
  output_schema?: unknown;
  annotations?: Record<string, unknown>;
};

/** One tool of a toolset added at run time, as its review shows it. */
export type ReviewTool = {
  name: string;
  /** approved, new, changed, removed, rejected. */
  status: string;
  /** The definition the server lists now (absent for a removed tool): sent back to decide on it. */
  fingerprint?: string;
  /** What the model is given: the description, sanitized. */
  description?: string;
  current?: ToolDefinition;
  approved?: ToolDefinition;
  /** The fields that differ between approved and current. */
  changes?: string[];
  kind: string;
  risk: string;
  reviewed_by?: string;
  reviewed_at?: string;
};

/** POST /v1/toolsets and the review endpoints: a toolset added at run time. */
export type ServerToolset = {
  name: string;
  type: string;
  url: string;
  transport: string;
  auth: string;
  /** The headers sent; never their values. */
  header_names?: string[];
  scope: string;
  enabled: boolean;
  /** auth "oauth": the authorization server accounts are connected at. */
  issuer?: string;
  oauth_client_id?: string;
  /** auth "oauth": the caller's connection (connected, not_connected, needs_reconnect); its slot is the toolset's name. */
  connection?: string;
  /** ready, review_needed, disabled, unavailable. */
  status: string;
  reason?: string;
  server: { name?: string; version?: string };
  tools: ReviewTool[];
  version: number;
  created_at: string;
  updated_at: string;
};

/** POST /v1/toolsets/{name}/review: one decision, on the definition the review showed. */
export type ToolDecision = {
  name: string;
  fingerprint: string;
  decision: "approve" | "reject";
  kind?: string;
  risk?: string;
};

/** A header a registry server's remote takes. */
export type RegistryHeader = { name: string; description?: string; required: boolean; secret: boolean };

/** An endpoint a registry server is hosted at. */
export type RegistryRemote = {
  /** As POST /v1/toolsets takes it: "http" or "sse". */
  transport: string;
  url: string;
  /** The {variables} in url to fill in before adding. */
  variables?: string[];
  headers?: RegistryHeader[];
  /** false: the tenant's mcp_servers.allow doesn't include url. */
  allowed: boolean;
};

/** A server a registry search found: third-party text, never a trust signal. */
export type RegistryServer = {
  name: string;
  suggested_name: string;
  title?: string;
  description?: string;
  version?: string;
  status: string;
  remotes: RegistryRemote[];
};

/** GET /v1/mcp-registry/search. */
export type RegistrySearch = { query: string; registry: string; restricted: boolean; servers: RegistryServer[] };

/**
 * Where a toolset's credential comes from, and whether it is there
 * (GET /v1/toolsets). Never its value: env and slot are names.
 */
export type ToolsetCredential = {
  /** configured, missing, keyless, not_needed; for a slot the caller's: connected, not_connected, needs_reconnect. */
  status: string;
  /** The environment variable the server reads the key from. */
  env?: string;
  /** The credential slot the toolset uses. */
  slot?: string;
  /** "tenant" (shared) or "user" (each user's own). */
  scope?: string;
};

/** GET /v1/toolsets: one toolset, ready or skipped with the reason. */
export type Toolset = {
  name: string;
  type: string;
  /** ready, skipped, unavailable, review_needed, disabled. */
  status: string;
  reason?: string;
  /** Where the server serves it over MCP, relative to its base URL; absent when it doesn't. */
  mcp_path?: string;
  tool_count: number;
  credential?: ToolsetCredential;
  /** Added at run time (POST /v1/toolsets). */
  runtime?: boolean;
};

/** What a tool shows in transcripts (tool metadata). */
export type ToolDisplay = { title?: string; icon?: string; present?: string; past?: string };

/** GET /v1/toolsets/{name}/tools: a tool's definition and what its metadata says. */
export type Tool = {
  name: string;
  description: string;
  input_schema: unknown;
  output_schema?: unknown;
  kind?: string;
  risk?: string;
  classified?: boolean;
  interrupts?: boolean;
  requires_approval?: boolean;
  tags?: string[];
  display?: ToolDisplay;
};

/** GET /v1/toolsets/{name}/tools. A skipped toolset has no tools. */
export type ToolList = { toolset: string; status: string; tools: Tool[] };

/** One path of the caller's memory of an agent (GET /v1/agents/{name}/memory). Plain text. */
export type MemoryView = {
  agent: string;
  /** "user": the caller's own memory; "tenant": one the tenant shares. */
  scope: string;
  path: string;
  /** A directory: entries lists it, two levels deep. Otherwise text is the file's. */
  dir: boolean;
  size: number;
  text?: string;
  entries?: MemoryEntry[];
  usage: { bytes: number; files: number };
  limits: { file_bytes: number; scope_bytes: number; files: number };
};

export type MemoryEntry = { path: string; dir: boolean; size: number };

/** GET /v1/credentials: a slot as the caller sees it. It never holds a value. */
export type CredentialInfo = {
  slot: string;
  /** "tenant" or "user". */
  scope: string;
  /** connected, not_connected, needs_reconnect. */
  status: string;
  /** api_key, basic, client_credentials, oauth2: the stored one's. */
  kind?: string;
  /** An account can be connected (POST /v1/credentials/{slot}/connect). */
  oauth: boolean;
  toolsets: string[];
  version?: number;
  created_at?: string;
  updated_at?: string;
  expiry?: string;
};

/** PUT /v1/credentials/{slot}: the console sets API keys only. */
export type PutCredentialRequest = { scope?: string; kind: "api_key"; api_key: string };

/** POST /v1/credentials/{slot}/connect's answer: where to send the browser. */
export type ConnectResponse = { authorize_url: string; expires_at: string };

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
  /** Set for a task (POST /v1/tasks, or a message from another agent): a background run. */
  background?: boolean;
  /** The runner pool it was placed in. */
  pool?: string;
  /** The stored agent's version it runs (absent for a config agent). */
  agent_version?: number;
};

/** The body of POST /v1/tasks. */
export type TaskRequest = { input: string; agent?: string; notify?: { url: string } };

/** One message of a thread between agents (GET /v1/threads/{id}). */
export type ThreadMessage = {
  id: string;
  /** Its position in the thread, from 1. */
  hop: number;
  /** agent:<name>, or agentkit for the server's notices. */
  from: string;
  to: string;
  from_conversation?: string;
  to_conversation: string;
  kind: "request" | "reply" | "notice" | (string & {});
  text: string;
  in_reply_to?: string;
  state: "queued" | "running" | "done" | (string & {});
  /** The run of its recipient that processed it. */
  run_id?: string;
  created_at: string;
  processed_at?: string;
};

/** One time a trigger fired (T072). */
export type Firing = {
  id: string;
  scheduled_for?: string;
  at: string;
  /** pending, started (run_id), skipped (reason), failed (reason). */
  state: "pending" | "started" | "skipped" | "failed" | (string & {});
  run_id?: string;
  reason?: string;
};

/** A trigger: what starts background runs of an agent on a schedule or a webhook (GET /v1/triggers). */
export type Trigger = {
  id: string;
  agent?: string;
  prompt: string;
  schedule?: string;
  webhook?: { path: string };
  paused: boolean;
  /** From the server's config: the API only pauses and resumes it. */
  static?: boolean;
  next_fire_at?: string;
  last_firing?: Firing;
  version: number;
};

/** A thread of messages between agents; GET /v1/threads lists it without messages. */
export type Thread = {
  id: string;
  /** open takes messages; stopped was stopped over a limit (error says which). */
  status: "open" | "stopped" | (string & {});
  error?: WireError;
  /** The number of messages in it. */
  hops: number;
  tokens: number;
  created_at: string;
  updated_at: string;
  messages?: ThreadMessage[];
};

/**
 * The person's action on an A2UI surface, sent in place of a message: the
 * button's action event with its context resolved (pkg/agentapi/wire.UIAction).
 */
export type UIActionRequest = {
  surface_id: string;
  component_id: string;
  action: string;
  context?: Record<string, unknown>;
};

export type RunRequest = {
  /** The message; empty resumes an interrupted conversation. Absent with ui_action. */
  message?: string;
  conversation_id?: string;
  agent?: string;
  ui_action?: UIActionRequest;
  /** Narrows the agent's tools for this run (pkg/agentapi/wire.ToolChoice). */
  tools?: ToolChoice;
  /** The model of the run, among the agent's models (Agent.models); absent: the conversation's, or the default. */
  model?: string;
};

/** How a run offers its toolsets' tools: all sent, or found with tool_search. */
export type ToolSearchMode = "direct" | "search";

/**
 * A run's choice of tools: which of its agent's toolsets the model gets
 * (absent: all; []: none), and whether their tools are sent directly or
 * found with tool search (absent: the agent's).
 */
export type ToolChoice = {
  toolsets?: string[];
  search?: ToolSearchMode;
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
