// An agent's detail (docs/UI.md "Agents", the mockup's agentsView): its
// description, model, toolsets with their status, skills, memory and what it
// answers with, and its system prompt, which GET /v1/agents/{name} sends to
// admins only (the API decides; the page shows what it gets).

import type { Agent } from "@agentkit/agent-ui/api";
import { Status } from "@agentkit/ui";
import { ChevronRight } from "lucide-react";
import { Link } from "react-router";
import { useResource } from "../api";
import { Loaded } from "./page";
import { Callout, Facts } from "./parts";

function Toolsets({ agent }: { agent: Agent }) {
  if (agent.toolsets.length === 0) return <>None</>;
  return (
    <ul aria-label="Toolsets" className="flex flex-wrap gap-1.5">
      {agent.toolsets.map((t) => {
        const ready = t.status === "ready";
        return (
          <li key={t.name}>
            <Link
              to={`/toolsets/${encodeURIComponent(t.name)}`}
              className="inline-flex items-center rounded-md border bg-ak-surface-2 px-2 py-0.5 font-mono text-(length:--ak-text-sm) hover:bg-ak-surface-hover"
            >
              <Status tone={ready ? "ok" : "warn"} className="gap-1.5 text-ak-text">
                {t.name}
                <span className="sr-only">, {ready ? "ready" : t.status}</span>
              </Status>
            </Link>
          </li>
        );
      })}
    </ul>
  );
}

function model(a: Agent): string {
  const m = a.model ?? { id: "" };
  return [m.provider, m.id].filter(Boolean).join(" · ") + (m.effort ? ` · effort ${m.effort}` : "");
}

/** The system prompt, collapsed; only when the API sent it (to admins). */
function SystemPrompt({ name }: { name: string }) {
  const res = useResource((c, signal) => c.getAgent(name, { signal }), `agent\n${name}`);
  return (
    <Loaded res={res} what="the agent's details">
      {(a) =>
        typeof a.system === "string" ? (
          <details className="group rounded-md border bg-ak-surface-2">
            <summary className="flex cursor-pointer list-none items-center gap-1.5 px-3 py-2 text-(length:--ak-text-sm) font-medium text-ak-text [&::-webkit-details-marker]:hidden">
              <ChevronRight
                aria-hidden="true"
                className="size-4 text-ak-muted transition-transform group-open:rotate-90"
              />
              System prompt (admins only)
            </summary>
            <pre className="overflow-x-auto border-t px-3 py-2.5 font-mono text-(length:--ak-text-sm) whitespace-pre-wrap text-ak-text">
              {a.system || "(empty)"}
            </pre>
          </details>
        ) : null
      }
    </Loaded>
  );
}

export function AgentDetail({ agent }: { agent: Agent }) {
  const skipped = agent.toolsets.filter((t) => t.status !== "ready");
  return (
    <>
      {agent.description ? <p className="text-ak-text-2">{agent.description}</p> : null}
      {skipped.length > 0 ? (
        <Callout
          title={`${skipped.length} of its ${agent.toolsets.length} toolsets ${skipped.length === 1 ? "isn't" : "aren't"} ready: ${skipped.map((t) => t.name).join(", ")}.`}
        >
          The agent still runs with the rest. Each toolset&apos;s page says why.
        </Callout>
      ) : null}
      <Facts
        rows={[
          ["Model", <span className="font-mono">{model(agent) || "The server's default"}</span>],
          ["Toolsets", <Toolsets agent={agent} />],
          // Lenient, as the client is: an older server may leave a list out.
          ["Skills", agent.skills?.length ? agent.skills.join(", ") : "None"],
          ["Memory", agent.memory ? "On: it keeps notes across conversations" : "Off"],
          ["Answers with", agent.ui === "a2ui" ? "Text, and forms and choices it makes (A2UI)" : "Text"],
          ["Default", agent.default ? "Yes: new chats use it unless you pick another" : "No"],
        ]}
      />
      <SystemPrompt name={agent.name} />
    </>
  );
}
