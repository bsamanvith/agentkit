// A checklist of the server's toolsets (GET /v1/toolsets), for choosing an
// agent's: each with its connector name when the console knows its type
// (connectors-catalog.ts), its tool count, and whether it is down or needs
// an account or key connected first. The agent builder uses it.

import type { Toolset } from "@agentkit/agent-ui/api";
import { cn } from "@agentkit/ui/lib/utils";
import { needsConnecting } from "../chat/tool-choices";
import { connectorInfo } from "./connectors-catalog";

/** What a toolset's row says under its name. */
function toolsetDetail(t: Toolset): string {
  const parts = [`${t.tool_count} tool${t.tool_count === 1 ? "" : "s"}`];
  if (t.status !== "ready") parts.push(t.status);
  else if (needsConnecting(t)) parts.push("Not connected");
  return parts.join(" · ");
}

export function ToolsetChecklist({
  toolsets,
  value,
  onChange,
  legend = "Toolsets",
  disabled = false,
}: {
  toolsets: Toolset[];
  /** The names checked, in the order they were checked. */
  value: string[];
  onChange: (names: string[]) => void;
  legend?: string;
  disabled?: boolean;
}) {
  return (
    <fieldset className="flex flex-col gap-1.5" disabled={disabled}>
      <legend className="mb-1.5 text-(length:--ak-text-sm) font-medium text-ak-text">{legend}</legend>
      {toolsets.length === 0 ? <p className="text-xs text-ak-muted">The server serves no toolsets.</p> : null}
      {toolsets.map((t) => {
        const title = connectorInfo(t.type)?.name;
        const checked = value.includes(t.name);
        return (
          <label key={t.name} className="flex items-start gap-2 text-(length:--ak-text-sm)">
            <input
              type="checkbox"
              className="mt-1"
              checked={checked}
              onChange={(e) => onChange(e.target.checked ? [...value, t.name] : value.filter((n) => n !== t.name))}
            />
            <span className="flex flex-col">
              <span className="text-ak-text">
                {title ?? <span className="font-mono">{t.name}</span>}
                {title ? <span className="ml-1.5 font-mono text-xs text-ak-muted">{t.name}</span> : null}
              </span>
              <span className={cn("text-xs", t.status === "ready" ? "text-ak-muted" : "text-ak-warn")}>
                {toolsetDetail(t)}
              </span>
            </span>
          </label>
        );
      })}
    </fieldset>
  );
}
