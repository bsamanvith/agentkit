import type { Agent } from "@agentkit/agent-ui/api";

/** Whether every toolset of an agent is ready, as one status. */
export function readiness(agent: Agent): { tone: "ok" | "warn"; text: string } {
  const notReady = agent.toolsets.filter((t) => t.status !== "ready");
  if (agent.toolsets.length === 0) return { tone: "ok", text: "Ready" };
  if (agent.toolsets.length === 1 && notReady.length === 0) return { tone: "ok", text: "Its toolset is ready" };
  if (notReady.length === 0) return { tone: "ok", text: `All ${agent.toolsets.length} toolsets ready` };
  return { tone: "warn", text: `${notReady.length} of ${agent.toolsets.length} toolsets not ready` };
}
