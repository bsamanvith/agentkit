// The chat's tool picker (T115, docs/UI.md "Tools in a chat"): which of the
// agent's toolsets the model gets, and whether their tools are sent
// directly or held back behind tool_search. A toolset whose credential the
// person hasn't connected says so and links to Connectors.

import type { Agent, Toolset } from "@agentkit/agent-ui/api";
import {
  DropdownMenu,
  DropdownMenuCheckboxItem,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@agentkit/ui/components/dropdown-menu";
import { Wrench } from "lucide-react";
import { Link } from "react-router";
import { needsConnecting, toggled, type Choice } from "./tool-choices";

/** The words of a mode: what the radio items and the button say. */
const modes = [
  { value: "", label: "Agent's default", detail: "As the agent is set up." },
  { value: "direct", label: "Direct", detail: "Every chosen tool is in the agent's context." },
  { value: "search", label: "Search", detail: "The agent finds tools when it needs them: fewer in its context." },
] as const;

export function ToolPicker({
  agent,
  toolsets,
  choice,
  onChange,
  disabled = false,
}: {
  agent: Agent;
  /** GET /v1/toolsets, for each toolset's credential; empty while it loads. */
  toolsets: Toolset[];
  choice: Choice;
  onChange: (c: Choice) => void;
  disabled?: boolean;
}) {
  const all = agent.toolsets.map((t) => t.name);
  if (all.length === 0) return null;
  const on = new Set(choice.toolsets ?? all);
  const count = all.filter((n) => on.has(n)).length;
  const mode = choice.search ?? "";
  const changed = choice.toolsets !== undefined || choice.search !== undefined;
  const label = `Tools: ${count} of ${all.length}${mode === "search" ? ", search" : mode === "direct" ? ", direct" : ""}`;
  return (
    <DropdownMenu modal={false}>
      <DropdownMenuTrigger asChild disabled={disabled}>
        <button
          type="button"
          aria-label={label}
          title={label}
          className="inline-flex h-9 flex-none items-center gap-1 rounded-md px-2 text-xs text-ak-text-2 hover:bg-ak-hover aria-expanded:bg-ak-select disabled:opacity-50"
        >
          <Wrench aria-hidden="true" className="size-4" />
          <span className="tabular-nums">
            {count}/{all.length}
          </span>
          {mode === "search" ? <span>· Search</span> : null}
        </button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-80">
        <DropdownMenuLabel className="text-ak-muted">Toolsets</DropdownMenuLabel>
        {agent.toolsets.map((ts) => {
          const info = toolsets.find((t) => t.name === ts.name);
          const connect = needsConnecting(info);
          return (
            <DropdownMenuCheckboxItem
              key={ts.name}
              checked={on.has(ts.name)}
              onSelect={(e) => e.preventDefault()}
              onCheckedChange={(v) => onChange(toggled(choice, all, ts.name, v === true))}
              className="flex flex-col items-start gap-0.5"
            >
              <span className="font-mono">{ts.name}</span>
              <span className="text-xs text-ak-muted">
                {(ts.tools ?? []).length} tool{(ts.tools ?? []).length === 1 ? "" : "s"}
                {ts.status !== "ready" ? ` · ${ts.status}` : connect ? " · Not connected" : ""}
              </span>
            </DropdownMenuCheckboxItem>
          );
        })}
        {agent.toolsets.some((ts) => needsConnecting(toolsets.find((t) => t.name === ts.name))) ? (
          <DropdownMenuItem asChild>
            <Link to="/connections" className="text-xs">
              Connect accounts and keys…
            </Link>
          </DropdownMenuItem>
        ) : null}
        <DropdownMenuSeparator />
        <DropdownMenuLabel className="text-ak-muted">How the agent gets them</DropdownMenuLabel>
        <DropdownMenuRadioGroup
          value={mode}
          onValueChange={(v) => onChange({ ...choice, search: v === "direct" || v === "search" ? v : undefined })}
        >
          {modes.map((m) => (
            <DropdownMenuRadioItem
              key={m.value}
              value={m.value}
              onSelect={(e) => e.preventDefault()}
              className="flex flex-col items-start gap-0.5"
            >
              <span>{m.label}</span>
              <span className="text-xs text-ak-muted">{m.detail}</span>
            </DropdownMenuRadioItem>
          ))}
        </DropdownMenuRadioGroup>
        {changed ? (
          <>
            <DropdownMenuSeparator />
            <DropdownMenuItem onSelect={() => onChange({})}>Reset to the agent&apos;s tools</DropdownMenuItem>
          </>
        ) : null}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
