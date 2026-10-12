import { BadResponseError, type AgentClient, type Agent } from "@agentkit/agent-ui/api";
import { Status } from "@agentkit/ui";
import { Link } from "react-router";
import { useResource } from "../api";
import { readiness } from "./readiness";
import { Loaded, Page } from "./page";

type Toolset = { name: string; type: string; status: string; reason?: string; tool_count: number };

const listAgents = (c: AgentClient, signal: AbortSignal) => c.listAgents({ signal });
const listToolsets = async (c: AgentClient, signal: AbortSignal): Promise<Toolset[]> => {
  const body = await c.getJSON<{ toolsets?: unknown } | null>("/v1/toolsets", { signal });
  const list = body?.toolsets ?? [];
  if (!Array.isArray(list)) throw new BadResponseError('/v1/toolsets: "toolsets" is not a list');
  return list as Toolset[];
};

/** Agents (T064 builds the detail): name, description, readiness. */
export function AgentsPage() {
  const agents = useResource(listAgents, "agents");
  return (
    <Page title="Agents">
      <Loaded res={agents} what="agents">
        {(list: Agent[]) => (
          <ul className="flex flex-col divide-y rounded-lg border">
            {list.map((a) => {
              const r = readiness(a);
              return (
                <li key={a.name} className="flex flex-col gap-1 p-3 sm:flex-row sm:items-center sm:gap-4">
                  <Link to={`/agents/${encodeURIComponent(a.name)}`} className="font-mono text-sm font-medium">
                    {a.name}
                  </Link>
                  <span className="min-w-0 flex-1 truncate text-ak-text-2">{a.description}</span>
                  <Status tone={r.tone} className="text-sm">
                    {r.text}
                  </Status>
                </li>
              );
            })}
          </ul>
        )}
      </Loaded>
    </Page>
  );
}

/** Toolsets (T064 builds the detail): name, status, tool count. */
export function ToolsetsPage() {
  const toolsets = useResource(listToolsets, "toolsets");
  return (
    <Page title="Toolsets">
      <Loaded res={toolsets} what="toolsets">
        {(list) =>
          list.length === 0 ? (
            <p className="text-ak-muted">No toolsets are configured.</p>
          ) : (
            <ul className="flex flex-col divide-y rounded-lg border">
              {list.map((t) => (
                <li key={t.name} className="flex items-center gap-4 p-3">
                  <span className="font-mono text-sm font-medium">{t.name}</span>
                  <Status tone={t.status === "ready" ? "ok" : "warn"} className="text-sm">
                    {t.status === "ready" ? "Ready" : `Skipped${t.reason ? `: ${t.reason}` : ""}`}
                  </Status>
                  <span className="ml-auto text-sm text-ak-muted">
                    {t.tool_count} {t.tool_count === 1 ? "tool" : "tools"}
                  </span>
                </li>
              ))}
            </ul>
          )
        }
      </Loaded>
    </Page>
  );
}

/** Connections (T064 with E07's T044/T045). */
export function ConnectionsPage() {
  return (
    <Page title="Connections">
      <p className="text-ak-muted">Connecting your own keys and accounts comes with the credentials API.</p>
    </Page>
  );
}

/** An item page that T064 fills in. */
export function DetailPage({ kind }: { kind: "Agent" | "Toolset" }) {
  return (
    <Page title={kind}>
      <p className="text-ak-muted">Details are not built yet.</p>
    </Page>
  );
}

export function NotFoundPage() {
  return (
    <Page title="Not found">
      <p className="text-ak-text-2">This page doesn&apos;t exist.</p>
      <Link to="/" className="text-sm underline">
        Back to chats
      </Link>
    </Page>
  );
}
