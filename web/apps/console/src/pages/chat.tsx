import { Composer } from "@agentkit/agent-ui";
import type { AgentClient } from "@agentkit/agent-ui/api";
import { Status } from "@agentkit/ui";
import { useParams, useSearchParams } from "react-router";
import { useResource } from "../api";
import { Loaded, Page } from "./page";
import { readiness } from "./readiness";

const listAgents = (c: AgentClient, signal: AbortSignal) => c.listAgents({ signal });

/**
 * New chat and conversations: the shell T055 builds on. It shows the chosen
 * agent (from /new?agent=, else the default) and the composer; sending
 * comes with T055.
 */
export function ChatPage() {
  const { id } = useParams();
  const [params] = useSearchParams();
  const agents = useResource(listAgents, "agents");
  const wanted = params.get("agent");
  return (
    <Page title={id ? "Conversation" : "New chat"}>
      <Loaded res={agents} what="agents">
        {(list) => {
          const agent = list.find((a) => a.name === wanted) ?? list.find((a) => a.default) ?? list[0];
          if (!agent) {
            return (
              <p className="text-ak-muted">
                No agents are configured. An admin adds them to the server config (
                <code className="font-mono">agents:</code>).
              </p>
            );
          }
          const r = readiness(agent);
          return (
            <section aria-label="Agent" className="flex flex-col gap-1 rounded-lg border p-4">
              <div className="font-mono text-sm font-medium text-ak-text">{agent.name}</div>
              {agent.description ? <p className="text-ak-text-2">{agent.description}</p> : null}
              <Status tone={r.tone} className="text-sm">
                {r.text}
              </Status>
            </section>
          );
        }}
      </Loaded>
      <div className="mt-auto">
        <Composer onSend={() => {}} disabledReason="Sending messages isn't built yet." />
      </div>
    </Page>
  );
}
