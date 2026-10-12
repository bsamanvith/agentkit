import { Spinner } from "@agentkit/ui/components/spinner";
import { cn } from "@agentkit/ui";
import { FileText, Globe, Calculator, Wrench, type LucideIcon } from "lucide-react";

/** What kind of tool a step ran: picks its icon (docs/UI.md "Tool calls"). */
export type ToolKind = "search" | "page" | "calc" | "tool";

/** A step's state. "stopped" is a call cut short by Stop: neutral, not a failure. */
export type ToolStepState = "running" | "done" | "failed" | "stopped";

const icons: Record<ToolKind, LucideIcon> = { search: Globe, page: FileText, calc: Calculator, tool: Wrench };

export type ToolStepProps = {
  kind?: ToolKind;
  state: ToolStepState;
  /** The verb phrase: present tense while running ("Searching the web"), past after ("Searched the web"). */
  label: string;
  /** The key argument, in muted text: the query in quotes, the page hosts. */
  detail?: string;
  /** How long the call took, in milliseconds (done, failed). */
  durationMs?: number;
  className?: string;
};

/**
 * One tool call as a quiet line: icon, verb phrase, key argument, time.
 * T055 adds the details (request, results, raw JSON) on a Collapsible.
 * Everything here is text: tool output is never rendered as HTML.
 */
export function ToolStep({ kind = "tool", state, label, detail, durationMs, className }: ToolStepProps) {
  const Icon = icons[kind];
  return (
    <div
      className={cn(
        "flex min-h-6 items-center gap-2 text-sm",
        state === "failed" ? "text-ak-bad" : "text-ak-text-2",
        className,
      )}
      data-state={state}
    >
      {state === "running" ? (
        <Spinner className="size-3.5" />
      ) : (
        <Icon aria-hidden="true" className="size-3.5 shrink-0" />
      )}
      <span className="font-medium">
        {state === "stopped" ? `Stopped: ${label.charAt(0).toLowerCase()}${label.slice(1)}` : label}
        {state === "failed" ? " failed" : ""}
      </span>
      {detail ? <span className="min-w-0 truncate text-ak-muted">{detail}</span> : null}
      {durationMs !== undefined && state !== "running" ? (
        <span className="ml-auto shrink-0 text-xs text-ak-muted tabular-nums">{formatSeconds(durationMs)}</span>
      ) : null}
    </div>
  );
}

/** 1400 → "1.4 s". */
export function formatSeconds(ms: number): string {
  return `${(ms / 1000).toFixed(1)} s`;
}
