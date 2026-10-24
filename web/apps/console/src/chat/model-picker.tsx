// The chat's model select (T127, docs/UI.md "The model picker"): next to
// the agent picker, the models the agent allows, the default first. An
// agent with one model shows none. The pick is the chat's (tool-choices.ts):
// it goes on each run as "model".

import { modelLabel } from "@agentkit/agent-ui";
import type { Agent } from "@agentkit/agent-ui/api";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuLabel,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuTrigger,
} from "@agentkit/ui/components/dropdown-menu";
import { ChevronDown } from "lucide-react";

export function ModelPicker({
  agent,
  value,
  onPick,
}: {
  agent: Agent;
  /** The model the chat runs: its pick, else its last run's, else the agent's default. */
  value: string;
  onPick: (model: string) => void;
}) {
  // Fewer than two models to pick from means no picker.
  const choices = agent.models?.choices ?? [];
  if (choices.length < 2) return null;
  const def = agent.models?.default ?? choices[0];
  return (
    <DropdownMenu modal={false}>
      <DropdownMenuTrigger asChild>
        <button
          type="button"
          aria-label={`Model: ${modelLabel(value)}`}
          className="inline-flex h-8 min-w-0 flex-none items-center gap-1 rounded-md px-2 font-mono text-xs text-ak-text-2 hover:bg-ak-hover aria-expanded:bg-ak-select"
        >
          <span className="truncate">{modelLabel(value)}</span>
          <ChevronDown aria-hidden="true" className="size-3.5 text-ak-muted" />
        </button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="start" className="w-72">
        <DropdownMenuLabel className="text-ak-muted">Model</DropdownMenuLabel>
        <DropdownMenuRadioGroup value={value} onValueChange={onPick}>
          {choices.map((m) => (
            <DropdownMenuRadioItem key={m} value={m} className="flex flex-col items-start gap-0.5">
              <span className="font-mono">{modelLabel(m)}</span>
              {m === def ? <span className="text-xs text-ak-muted">The agent&apos;s default</span> : null}
            </DropdownMenuRadioItem>
          ))}
        </DropdownMenuRadioGroup>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
