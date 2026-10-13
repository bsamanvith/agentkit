// The admin lists, loaded once by the shell for the section on screen: the
// list column and the page read the same load (one request on a slow link,
// not two).

import { listField, type Agent, type AgentClient } from "@agentkit/agent-ui/api";
import { createContext, useContext } from "react";
import type { Resource } from "./api";
import { readiness } from "./pages/readiness";

export type Toolset = { name: string; type: string; status: string; reason?: string; tool_count: number };

export const listAgents = (c: AgentClient, signal: AbortSignal) => c.listAgents({ signal });
export const listToolsets = async (c: AgentClient, signal: AbortSignal): Promise<Toolset[]> =>
  listField<Toolset>(await c.getJSON<unknown>("/v1/toolsets", { signal }), "toolsets", "/v1/toolsets");

/** The section's list, or null in a section that doesn't load it. */
export type Lists = { agents: Resource<Agent[]> | null; toolsets: Resource<Toolset[]> | null };

export const ListsContext = createContext<Lists>({ agents: null, toolsets: null });

export function useLists(): Lists {
  return useContext(ListsContext);
}

/** A toolset's status as a tone and a word. */
export function toolsetStatus(t: Toolset): { tone: "ok" | "warn"; text: string } {
  return t.status === "ready" ? { tone: "ok", text: "Ready" } : { tone: "warn", text: "Skipped" };
}

/** What a list row shows: the name, a muted line, and a status. */
export type RowInfo = { name: string; sub: string; tone: "ok" | "warn"; word: string };

export function agentRow(a: Agent): RowInfo {
  const r = readiness(a);
  return { name: a.name, sub: a.description || r.text, tone: r.tone, word: r.tone === "ok" ? "Ready" : r.text };
}

export function toolsetRow(t: Toolset): RowInfo {
  const s = toolsetStatus(t);
  const sub =
    s.tone === "ok"
      ? `${t.tool_count} ${t.tool_count === 1 ? "tool" : "tools"}`
      : `Skipped${t.reason ? ` · ${t.reason}` : ""}`;
  return { name: t.name, sub, tone: s.tone, word: s.text };
}
