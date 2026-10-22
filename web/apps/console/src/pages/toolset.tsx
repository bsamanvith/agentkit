// A toolset's detail (docs/UI.md "Toolsets", the mockup's toolsetsView):
// its status (ready, or skipped with the reason), its credential, the agents
// that use it, its MCP URL with a `claude mcp add` command, and each tool
// with a readable field list and its raw schema.

import type { Tool, Toolset } from "@agentkit/agent-ui/api";
import { Status } from "@agentkit/ui";
import { Button } from "@agentkit/ui/components/button";
import { ChevronRight, KeyRound } from "lucide-react";
import { useState } from "react";
import { Link } from "react-router";
import { useResource } from "../api";
import { listAgents } from "../lists";
import { credentialWords } from "./credential";
import { Loaded } from "./page";
import { Callout, CopyLine, Facts, Section } from "./parts";
import { mcpAddCommand, mcpURL } from "./mcp";
import { fields, type Field } from "./schema";

function FieldList({ list, label }: { list: Field[]; label: string }) {
  if (list.length === 0) return <p className="text-(length:--ak-text-sm) text-ak-muted">{label}: none.</p>;
  return (
    <table className="w-full border-collapse text-left text-(length:--ak-text-sm)">
      <caption className="pb-1 text-left text-xs font-medium text-ak-muted">{label}</caption>
      <thead className="sr-only">
        <tr>
          <th scope="col">Field</th>
          <th scope="col">Type</th>
          <th scope="col">What it is</th>
        </tr>
      </thead>
      <tbody>
        {list.map((f) => (
          <tr key={f.path} className="border-t align-top max-md:flex max-md:flex-col max-md:py-1.5">
            <th scope="row" className="py-1.5 pr-3 font-normal md:w-[34%]">
              <span
                className="inline-flex flex-wrap items-baseline gap-x-1.5 font-mono text-ak-text"
                data-depth={f.depth}
              >
                <span className={depthIndent[Math.min(f.depth, 3)]}>{f.depth > 0 ? f.path : f.name}</span>
                {f.required ? <span className="font-sans text-xs text-ak-warn">required</span> : null}
              </span>
            </th>
            <td className="py-1.5 pr-3 font-mono text-ak-text-2 md:w-[22%] max-md:py-0">{f.type}</td>
            <td className="py-1.5 text-ak-muted max-md:py-0">{f.help}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

// Indents a nested field under its parent (static classes: no inline style under the CSP).
const depthIndent = ["", "pl-3", "pl-6", "pl-9"];

function ToolItem({ tool }: { tool: Tool }) {
  const [raw, setRaw] = useState(false);
  const input = fields(tool.input_schema);
  const output = tool.output_schema === undefined ? null : fields(tool.output_schema);
  const firstLine = tool.description.split("\n")[0] ?? "";
  const meta = [tool.kind, tool.risk ? `${tool.risk} risk` : "", tool.requires_approval ? "asks for approval" : ""]
    .filter(Boolean)
    .join(" · ");
  return (
    <details className="group border-t first:border-t-0" data-tool={tool.name}>
      <summary className="flex cursor-pointer list-none items-baseline gap-2 px-3 py-2.5 hover:bg-ak-surface-hover [&::-webkit-details-marker]:hidden">
        <ChevronRight
          aria-hidden="true"
          className="size-4 flex-none translate-y-0.5 self-start text-ak-muted transition-transform group-open:rotate-90"
        />
        <span className="flex min-w-0 flex-1 flex-col gap-0.5 md:flex-row md:gap-3">
          <span className="flex-none font-mono text-(length:--ak-text-sm) font-medium text-ak-text">{tool.name}</span>
          <span className="min-w-0 truncate text-(length:--ak-text-sm) text-ak-muted">{firstLine}</span>
        </span>
      </summary>
      <div className="flex flex-col gap-3 px-3 pt-1 pb-3 md:pl-9">
        {tool.description.includes("\n") ? (
          <p className="text-(length:--ak-text-sm) whitespace-pre-wrap text-ak-text-2">{tool.description}</p>
        ) : null}
        {meta ? <p className="text-xs text-ak-muted">{meta}</p> : null}
        <FieldList list={input} label="Input" />
        {output ? <FieldList list={output} label="Output" /> : null}
        <div>
          <Button
            type="button"
            variant="link"
            size="sm"
            className="h-auto px-0"
            aria-expanded={raw}
            onClick={() => setRaw(!raw)}
          >
            {raw ? "Hide raw schema" : "Show raw schema"}
          </Button>
          {raw ? (
            <pre
              aria-label={`${tool.name} raw schema`}
              className="mt-1 max-h-96 overflow-auto rounded-md border bg-ak-surface-2 p-2.5 font-mono text-xs text-ak-text"
            >
              {JSON.stringify(
                tool.output_schema === undefined
                  ? { input_schema: tool.input_schema }
                  : { input_schema: tool.input_schema, output_schema: tool.output_schema },
                null,
                2,
              )}
            </pre>
          ) : null}
        </div>
      </div>
    </details>
  );
}

function Tools({ name }: { name: string }) {
  const res = useResource((c, signal) => c.listTools(name, { signal }), `tools\n${name}`);
  return (
    <Loaded res={res} what="its tools">
      {(list) =>
        list.tools.length === 0 ? null : (
          <Section title={`${list.tools.length} ${list.tools.length === 1 ? "tool" : "tools"}`}>
            <div className="overflow-hidden rounded-md border">
              {list.tools.map((t) => (
                <ToolItem key={t.name} tool={t} />
              ))}
            </div>
          </Section>
        )
      }
    </Loaded>
  );
}

/** The agents that use the toolset, as links. */
function UsedBy({ name }: { name: string }) {
  const res = useResource(listAgents, "agents");
  if (res.state !== "ready") return <span className="text-ak-muted">…</span>;
  const users = res.data.filter((a) => a.toolsets.some((t) => t.name === name));
  if (users.length === 0) return <>No agent</>;
  return (
    <ul aria-label="Used by" className="flex flex-wrap gap-1.5">
      {users.map((a) => (
        <li key={a.name}>
          <Link
            to={`/agents/${encodeURIComponent(a.name)}`}
            className="inline-flex rounded-md border bg-ak-surface-2 px-2 py-0.5 font-mono text-(length:--ak-text-sm) hover:bg-ak-surface-hover"
          >
            {a.name}
          </Link>
        </li>
      ))}
    </ul>
  );
}

export function ToolsetDetail({ toolset: t }: { toolset: Toolset }) {
  const cred = credentialWords(t.credential);
  const url = mcpURL(t, window.location.origin);
  return (
    <>
      {t.status === "skipped" ? (
        <Callout title={`Skipped: ${t.reason ?? "the server couldn't start it"}`}>
          It&apos;s optional, so the server started without it. Fix the cause, then restart agentkit.
        </Callout>
      ) : t.status === "unavailable" ? (
        <Callout tone="bad" title={`Unavailable: ${t.reason ?? "its tools can't be listed just now"}`}>
          The toolsets component may be down. Agents that use it go on without its tools.
        </Callout>
      ) : t.status === "review_needed" ? (
        <Callout title={`${t.name} changed or added tools since they were approved.`}>
          Those tools are off until an admin reviews them. The ones already approved keep working.
        </Callout>
      ) : null}
      {cred && cred.tone === "warn" ? (
        <Callout icon={KeyRound} title={`${cred.word}: ${cred.source}.`}>
          {cred.detail}
        </Callout>
      ) : null}
      <Facts
        rows={[
          ["Type", `${t.type}${t.runtime ? ", added by an admin at run time" : ""}`],
          [
            "Credential",
            cred ? (
              <span className="flex flex-col gap-0.5">
                <span>{cred.source}</span>
                <span className="flex flex-wrap items-center gap-x-2">
                  <Status tone={cred.tone}>{cred.word}</Status>
                  <Link to="/connections" className="text-ak-text-2 underline underline-offset-2">
                    Connections
                  </Link>
                </span>
              </span>
            ) : (
              "None needed"
            ),
          ],
          ["Used by", <UsedBy name={t.name} />],
        ]}
      />
      {t.status === "ready" && url ? (
        <Section title="Use it from Claude Code">
          <CopyLine name="MCP URL" text={url} label="Copy URL" />
          <CopyLine name="Command" text={mcpAddCommand(t.name, url)} label="Copy command" />
          <p className="text-xs text-ak-muted">
            $AGENTKIT_API_TOKEN stands for an API token from your admin (AGENTKIT_API_TOKEN on a single-token server).
            The page never has it.
          </p>
        </Section>
      ) : t.status === "ready" ? (
        <p className="text-(length:--ak-text-sm) text-ak-muted">
          This server doesn&apos;t serve toolsets over MCP (its mcp-server component isn&apos;t running).
        </p>
      ) : (
        <p className="text-(length:--ak-text-sm) text-ak-muted">The MCP URL appears once the toolset is ready.</p>
      )}
      {t.status === "skipped" ? null : <Tools name={t.name} />}
    </>
  );
}
