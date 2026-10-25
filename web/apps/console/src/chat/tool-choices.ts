// The chat's tool choice, per conversation plus one for New chat ("new"),
// in memory and in localStorage (T115): which of the agent's toolsets the
// model gets, and whether their tools are sent directly or found with tool
// search; and the model picked (T127). Only what the person changed is
// kept; nothing kept means the agent's defaults, and the run then carries
// no "tools" and no "model" at all.

import type { ToolChoice, Toolset, ToolSearchMode } from "@agentkit/agent-ui/api";

const prefix = "agentkit.tools.";

/**
 * A chat's choice: the toolsets checked (absent: all), the mode (absent:
 * the agent's) and the model (absent: the conversation's, or the agent's
 * default).
 */
export type Choice = { toolsets?: string[]; search?: ToolSearchMode; model?: string };

function isEmpty(c: Choice): boolean {
  return c.toolsets === undefined && c.search === undefined && c.model === undefined;
}

function read(raw: string | null): Choice {
  if (!raw) return {};
  try {
    const v = JSON.parse(raw) as Partial<Record<keyof Choice, unknown>>;
    const out: Choice = {};
    if (Array.isArray(v.toolsets) && v.toolsets.every((t) => typeof t === "string")) out.toolsets = v.toolsets;
    if (v.search === "direct" || v.search === "search") out.search = v.search;
    if (typeof v.model === "string" && v.model !== "") out.model = v.model;
    return out;
  } catch {
    return {};
  }
}

const none: Choice = {};

export class ToolChoices {
  #mem = new Map<string, Choice>();
  #listeners = new Set<() => void>();

  subscribe = (fn: () => void) => {
    this.#listeners.add(fn);
    return () => this.#listeners.delete(fn);
  };

  get = (key: string): Choice => {
    let v = this.#mem.get(key);
    if (v === undefined) {
      try {
        v = read(localStorage.getItem(prefix + key));
      } catch {
        v = none; // storage blocked: memory only
      }
      if (isEmpty(v)) v = none;
      this.#mem.set(key, v);
    }
    return v;
  };

  set(key: string, c: Choice) {
    const v: Choice = {};
    if (c.toolsets !== undefined) v.toolsets = [...c.toolsets];
    if (c.search !== undefined) v.search = c.search;
    if (c.model !== undefined) v.model = c.model;
    const empty = isEmpty(v);
    this.#mem.set(key, empty ? none : v);
    try {
      if (empty) localStorage.removeItem(prefix + key);
      else localStorage.setItem(prefix + key, JSON.stringify(v));
    } catch {
      // Storage blocked.
    }
    for (const fn of this.#listeners) fn();
  }

  /** New chat's choice becomes the conversation's, once it has an id. */
  move(from: string, to: string) {
    this.set(to, this.get(from));
    this.set(from, none);
  }

  /** What a run of the chat sends as "tools"; undefined for the agent's defaults. */
  request(key: string): ToolChoice | undefined {
    const c = this.get(key);
    if (c.toolsets === undefined && c.search === undefined) return undefined;
    return {
      ...(c.toolsets !== undefined ? { toolsets: [...c.toolsets] } : {}),
      ...(c.search ? { search: c.search } : {}),
    };
  }

  clear() {
    for (const key of [...this.#mem.keys()]) this.set(key, none);
    try {
      for (let i = localStorage.length - 1; i >= 0; i--) {
        const k = localStorage.key(i);
        if (k?.startsWith(prefix)) localStorage.removeItem(k);
      }
    } catch {
      // Storage blocked.
    }
  }
}

/**
 * The choice after the person checks or unchecks a toolset: back to
 * "absent" when every toolset of the agent is checked again.
 */
export function toggled(c: Choice, all: string[], name: string, on: boolean): Choice {
  const now = new Set(c.toolsets ?? all);
  if (on) now.add(name);
  else now.delete(name);
  const toolsets = all.filter((t) => now.has(t));
  return { ...c, toolsets: toolsets.length === all.length ? undefined : toolsets };
}

/** Credential statuses that mean the person has something to connect. */
const unconnected = new Set(["not_connected", "needs_reconnect", "missing"]);

/** Whether the toolset needs a key or account the caller hasn't connected. */
export function needsConnecting(t: Toolset | undefined): boolean {
  return t?.credential !== undefined && unconnected.has(t.credential.status);
}
