import { Composer } from "@agentkit/agent-ui";
import { Status } from "@agentkit/ui";
import { cn } from "@agentkit/ui/lib/utils";
import { SquarePen } from "lucide-react";
import { Link, useParams, useSearchParams } from "react-router";
import { useResource } from "../api";
import { useLayout } from "../layout";
import { listAgents } from "../lists";
import { iconButton, Loaded, Page } from "./page";
import { readiness } from "./readiness";

/**
 * New chat and conversations: the shell T055 builds on. It shows the chosen
 * agent (from /new?agent=, else the default) and the composer; sending
 * comes with T055.
 */
export function ChatPage() {
  const { id } = useParams();
  const [params] = useSearchParams();
  const { phone } = useLayout();
  const agents = useResource(listAgents, "agents");
  const wanted = params.get("agent");
  return (
    <Page
      title={id ? "Conversation" : "New chat"}
      muted={!id}
      body="bare"
      aside={
        // The drawer's New chat is a tap further away on a phone.
        phone ? (
          <Link to="/new" aria-label="New chat" className={iconButton}>
            <SquarePen aria-hidden="true" className="size-4" />
          </Link>
        ) : undefined
      }
    >
      <div className={cn("min-h-0 flex-1 overflow-auto", phone ? "px-4 pt-5 pb-2" : "px-6 pt-7 pb-3")}>
        <div className="mx-auto flex max-w-(--ak-chat-w) flex-col gap-5">
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
        </div>
      </div>
      <div
        className={cn(
          "flex-none",
          phone ? "px-4 pt-1.5 pb-[calc(12px+env(safe-area-inset-bottom,0px))]" : "px-6 pt-2 pb-3.5",
        )}
      >
        <div className="mx-auto max-w-(--ak-chat-w)">
          <Composer onSend={() => {}} disabledReason="Sending messages isn't built yet." />
        </div>
      </div>
    </Page>
  );
}
