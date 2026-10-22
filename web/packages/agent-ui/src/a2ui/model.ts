// A2UI v0.9 surfaces as data (pkg/a2ui; docs/UI.md "A2UI basic catalog"):
// the surfaces a conversation's ui events (or, in its history, its
// successful ui_render calls) drew, folded in order, and the actions the
// person sent back. Pure functions and plain values; the renderer is
// surface.tsx, its own entry ("@agentkit/agent-ui/a2ui"), loaded lazily.
//
// Everything here is the agent's data: it is drawn with our components as
// text and values, never as markup (surface.tsx).

/** The protocol version every message carries (a2ui.Version). */
export const a2uiVersion = "v0.9";

/** The tool that shows UI (a2ui.ToolName); the thread hides its steps, since the surface is what it did. */
export const uiToolName = "ui_render";

/** One component: "id", "component" (its type) and the type's properties, as the agent sent them. */
export type Component = { id: string; component: string } & Record<string, unknown>;

/** One A2UI server-to-client message, as the agent sent it (checked by the server, a2ui.Validate). */
export type UIMessage = Record<string, unknown>;

/** A surface's message, keyed for the thread: the key of a createSurface is the surface's place in it. */
export type UIEntry = { key: string; message: UIMessage };

/** What the person did on a surface: a button's action event (a2ui.Action, ParseActionText). */
export type UIAction = {
  surfaceId: string;
  componentId: string;
  name: string;
  context: Record<string, unknown>;
  timestamp?: string;
};

export type Surface = {
  id: string;
  /** The createSurface's entry key: where the surface sits in the thread. */
  key: string;
  components: ReadonlyMap<string, Component>;
  /** The data model, as the agent set it. */
  data: unknown;
  /**
   * The conversation's count of ui messages when this surface last changed,
   * so a person's edits made before (SurfaceEdits) are known to be stale.
   */
  revision: number;
  deleted: boolean;
  /** The person's last action, while the agent hasn't changed the surface since. */
  sent?: UIAction;
};

function isObject(v: unknown): v is Record<string, unknown> {
  return typeof v === "object" && v !== null && !Array.isArray(v);
}

/** The surface a message is about, and its kind; undefined for anything else. */
export function messageKind(
  m: UIMessage,
): { kind: "createSurface" | "updateComponents" | "updateDataModel" | "deleteSurface"; surfaceId: string } | undefined {
  for (const kind of ["createSurface", "updateComponents", "updateDataModel", "deleteSurface"] as const) {
    const body = m[kind];
    if (isObject(body) && typeof body.surfaceId === "string" && body.surfaceId !== "") {
      return { kind, surfaceId: body.surfaceId };
    }
  }
  return undefined;
}

/** The A2UI messages of a ui_render call's input ({"messages": [...]}); none when it isn't one. */
export function messagesOfInput(input: unknown): UIMessage[] {
  if (!isObject(input) || !Array.isArray(input.messages)) return [];
  return input.messages.filter(isObject);
}

// ---- JSON pointers (RFC 6901) on the data model ----

/** The tokens of a JSON pointer: "" and "/" are the whole model. */
export function pointerTokens(path: string): string[] {
  if (path === "" || path === "/") return [];
  const body = path.startsWith("/") ? path.slice(1) : path;
  return body.split("/").map((t) => t.replaceAll("~1", "/").replaceAll("~0", "~"));
}

/** Joins a template item's base and a relative path ("name"); an absolute path stands alone. */
export function joinPath(base: string, path: string): string {
  if (path.startsWith("/")) return path;
  if (path === "" || path === ".") return base;
  if (base === "") return `/${path}`;
  return `${base.replace(/\/$/, "")}/${path}`;
}

/** The value at a pointer, or undefined. */
export function getAt(data: unknown, path: string): unknown {
  let v = data;
  for (const t of pointerTokens(path)) {
    if (Array.isArray(v)) v = /^\d+$/.test(t) ? v[Number(t)] : undefined;
    else if (isObject(v)) v = Object.hasOwn(v, t) ? v[t] : undefined;
    else return undefined;
  }
  return v;
}

/** The model with the value at a pointer set (undefined removes it), copying what it changes. */
export function setAt(data: unknown, path: string, value: unknown): unknown {
  const tokens = pointerTokens(path);
  const put = (node: unknown, i: number): unknown => {
    if (i === tokens.length) return value;
    const t = tokens[i] as string;
    if (Array.isArray(node) && /^\d+$/.test(t)) {
      const out = [...node];
      const n = Number(t);
      if (i === tokens.length - 1 && value === undefined) out.splice(n, 1);
      else out[n] = put(node[n], i + 1);
      return out;
    }
    const obj: Record<string, unknown> = isObject(node) ? node : {};
    if (i === tokens.length - 1 && value === undefined) {
      return Object.fromEntries(Object.entries(obj).filter(([k]) => k !== t));
    }
    return { ...obj, [t]: put(obj[t], i + 1) };
  };
  return put(data, 0);
}

// ---- Folding a conversation's messages ----

/** What the fold reads of a thread: the turns' ui messages and actions, in order. */
export type SurfaceSource = { role: string; ui?: readonly UIEntry[]; action?: UIAction };

/**
 * The surfaces of a conversation, by their createSurface's key, from its
 * turns in order. updateComponents adds components or replaces those with
 * the same IDs; updateDataModel sets the value at its path (the whole model
 * without one) or, without a value, removes it; deleteSurface removes the
 * surface. A message about a surface that doesn't exist is dropped. A
 * person's action marks its surface sent until the agent changes it.
 */
export function foldSurfaces(turns: readonly SurfaceSource[]): Map<string, Surface> {
  const byKey = new Map<string, Surface>();
  const current = new Map<string, string>();
  let revision = 0;
  for (const t of turns) {
    if (t.role === "user") {
      const action = t.action;
      const key = action && current.get(action.surfaceId);
      const s = key && byKey.get(key);
      if (s && action) byKey.set(s.key, { ...s, sent: action });
      continue;
    }
    for (const { key, message } of t.ui ?? []) {
      const k = messageKind(message);
      if (!k) continue;
      revision++;
      if (k.kind === "createSurface") {
        const old = current.get(k.surfaceId);
        const prev = old && byKey.get(old);
        if (prev) byKey.set(prev.key, { ...prev, deleted: true, sent: undefined });
        byKey.set(key, { id: k.surfaceId, key, components: new Map(), data: {}, revision, deleted: false });
        current.set(k.surfaceId, key);
        continue;
      }
      const at = current.get(k.surfaceId);
      const s = at && byKey.get(at);
      if (!s) continue;
      const body = message[k.kind] as Record<string, unknown>;
      let next: Surface = { ...s, revision, sent: undefined };
      if (k.kind === "updateComponents") {
        const components = new Map(s.components);
        for (const c of Array.isArray(body.components) ? body.components : []) {
          if (isObject(c) && typeof c.id === "string" && typeof c.component === "string") {
            components.set(c.id, c as Component);
          }
        }
        next = { ...next, components };
      } else if (k.kind === "updateDataModel") {
        const path = typeof body.path === "string" ? body.path : "";
        const value = Object.hasOwn(body, "value") ? body.value : undefined;
        next = { ...next, data: pointerTokens(path).length === 0 ? (value ?? {}) : setAt(s.data, path, value) };
      } else {
        next = { ...next, deleted: true };
        current.delete(k.surfaceId);
      }
      byKey.set(s.key, next);
    }
  }
  return byKey;
}

// ---- Actions as text (a2ui.ActionText) ----

const actionOpen = "<ui_action>\n";
const actionClose = "\n</ui_action>";

/**
 * The action of a user message's text, as the server writes it
 * (a2ui.ActionText); undefined for any other text, which is then the
 * person's own words.
 */
export function parseActionText(text: string): UIAction | undefined {
  if (!text.startsWith(actionOpen) || !text.endsWith(actionClose)) return undefined;
  let m: unknown;
  try {
    m = JSON.parse(text.slice(actionOpen.length, text.length - actionClose.length));
  } catch {
    return undefined;
  }
  if (!isObject(m) || m.version !== a2uiVersion || !isObject(m.action)) return undefined;
  const a = m.action;
  if (typeof a.name !== "string" || typeof a.surfaceId !== "string" || typeof a.sourceComponentId !== "string") {
    return undefined;
  }
  return {
    surfaceId: a.surfaceId,
    componentId: a.sourceComponentId,
    name: a.name,
    context: isObject(a.context) ? a.context : {},
    ...(typeof a.timestamp === "string" ? { timestamp: a.timestamp } : {}),
  };
}

// ---- URLs ----

/**
 * Whether s is an absolute http or https URL with a host and no user info
 * or control characters: a2ui.SafeURL's rule, which the renderer applies to
 * every URL it only knows when it draws (bound to the data model, or built
 * by a function call).
 */
export function safeUrl(s: unknown): s is string {
  // eslint-disable-next-line no-control-regex
  if (typeof s !== "string" || !/^https?:\/\/[^/?#]/i.test(s) || /[\u0000-\u001f\u007f]/.test(s)) return false;
  let u: URL;
  try {
    u = new URL(s);
  } catch {
    return false;
  }
  return (u.protocol === "http:" || u.protocol === "https:") && u.host !== "" && u.username === "" && u.password === "";
}

// ---- The person's edits ----

type Edits = { revision: number; values: ReadonlyMap<string, unknown> };

const noEdits: ReadonlyMap<string, unknown> = new Map();

/**
 * What the person typed or picked on surfaces, by a scope (the conversation)
 * and surface ID, kept outside the components so it survives the thread
 * being rebuilt (a run ends, its history loads). Edits belong to the
 * surface's revision: once the agent changes the surface, they are dropped
 * and the agent's data model shows.
 */
export class SurfaceEdits {
  #edits = new Map<string, Edits>();
  #listeners = new Set<() => void>();

  subscribe = (fn: () => void) => {
    this.#listeners.add(fn);
    return () => this.#listeners.delete(fn);
  };

  /** The edits to a surface at its revision (path to value); empty when stale. Stable until changed. */
  get(scope: string, surface: Pick<Surface, "id" | "revision">): ReadonlyMap<string, unknown> {
    const e = this.#edits.get(`${scope}\n${surface.id}`);
    return e && e.revision === surface.revision ? e.values : noEdits;
  }

  set(scope: string, surface: Pick<Surface, "id" | "revision">, path: string, value: unknown) {
    const values = new Map(this.get(scope, surface));
    values.set(path, value);
    this.#edits.set(`${scope}\n${surface.id}`, { revision: surface.revision, values });
    for (const fn of this.#listeners) fn();
  }

  clear() {
    this.#edits.clear();
    for (const fn of this.#listeners) fn();
  }
}

/** The data model with the person's edits on it. */
export function withEdits(data: unknown, edits: ReadonlyMap<string, unknown>): unknown {
  let out = data;
  for (const [path, value] of edits) out = setAt(out, path, value);
  return out;
}
