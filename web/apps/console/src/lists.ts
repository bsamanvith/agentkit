// The admin lists, loaded once by the shell for the section on screen: the
// list column and the page read the same load (one request on a slow link,
// not two).

import type { Agent, AgentClient, Toolset } from "@agentkit/agent-ui/api";
import type { StatusTone } from "@agentkit/ui";
import { createContext, useContext } from "react";
import type { Resource } from "./api";
import { readiness } from "./pages/readiness";

export type { Toolset };

export const listAgents = (c: AgentClient, signal: AbortSignal) => c.listAgents({ signal });
export const listToolsets = (c: AgentClient, signal: AbortSignal) => c.listToolsets({ signal });

/**
 * The section's list, or null in a section that doesn't load it, and ways
 * to load a list again after a page changed it (a toolset added or
 * reviewed, an agent saved or published).
 */
export type Lists = {
  agents: Resource<Agent[]> | null;
  toolsets: Resource<Toolset[]> | null;
  reloadAgents: () => void;
  reloadToolsets: () => void;
};

export const ListsContext = createContext<Lists>({
  agents: null,
  toolsets: null,
  reloadAgents: () => {},
  reloadToolsets: () => {},
});

/** Whether the server keeps agent definitions (T075): it marks its config agents read-only, or lists stored ones. */
export function keepsAgents(agents: Agent[]): boolean {
  return agents.some((a) => a.read_only === true || a.version !== undefined);
}

export function useLists(): Lists {
  return useContext(ListsContext);
}

/** A toolset's status as a tone and a word. */
export function toolsetStatus(t: Toolset): { tone: StatusTone; text: string } {
  if (t.status === "ready") return { tone: "ok", text: "Ready" };
  if (t.status === "unavailable") return { tone: "bad", text: "Unavailable" };
  if (t.status === "review_needed") return { tone: "warn", text: "Review needed" };
  if (t.status === "disabled") return { tone: "neutral", text: "Disabled" };
  return { tone: "warn", text: "Skipped" };
}

/** What a list row shows: the name, a muted line, and a status. */
export type RowInfo = { name: string; sub: string; tone: StatusTone; word: string };

export function agentRow(a: Agent): RowInfo {
  const r = readiness(a);
  return { name: a.name, sub: a.description || r.text, tone: r.tone, word: r.tone === "ok" ? "Ready" : r.text };
}

export function toolsetRow(t: Toolset): RowInfo {
  const s = toolsetStatus(t);
  const sub =
    s.tone === "ok"
      ? `${t.tool_count} ${t.tool_count === 1 ? "tool" : "tools"}`
      : `${s.text}${t.reason ? ` · ${t.reason}` : ""}`;
  return { name: t.name, sub, tone: s.tone, word: s.text };
}
