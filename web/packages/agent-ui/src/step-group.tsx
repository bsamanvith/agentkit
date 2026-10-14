import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@agentkit/ui/components/collapsible";
import { cn } from "@agentkit/ui";
import { ChevronRight } from "lucide-react";
import { useState } from "react";
import { ToolStep, formatSeconds, type ToolStepProps } from "./tool-step";

export type StepGroupProps = {
  steps: (ToolStepProps & { id: string })[];
  className?: string;
};

/** How many calls an open group shows before "Show N more" (docs/UI.md "Tool calls"). */
const windowSize = 5;

/**
 * Consecutive tool calls under "Used 3 tools · 2.6 s" with a guide line
 * (docs/UI.md "Tool calls"). One call is a plain step. A group is open by
 * default up to 3 calls and collapsed above; the user's choice stays while
 * the run goes on. Open, a finished group shows its first 5 calls and a
 * running one its latest 5, with a button for the rest; failed calls are
 * always shown.
 */
export function StepGroup({ steps, className }: StepGroupProps) {
  const [chosen, setOpen] = useState<boolean | undefined>(undefined);
  const [all, setAll] = useState(false);
  const [first] = steps;
  if (first && steps.length === 1) {
    const { id: _id, ...only } = first;
    return <ToolStep {...only} className={className} />;
  }
  const open = chosen ?? steps.length <= 3;
  const running = steps.some((s) => s.state === "running");
  const total = steps.reduce((sum, s) => sum + (s.durationMs ?? 0), 0);
  const failed = steps.filter((s) => s.state === "failed").length;
  const summary = [
    `Used ${steps.length} tools`,
    failed ? `${failed} failed` : "",
    total > 0 ? formatSeconds(total) : "",
  ]
    .filter(Boolean)
    .join(" · ");

  // The window: the first (or, running, the latest) calls, plus every failed one.
  const inWindow = (i: number) => (running ? i >= steps.length - windowSize : i < windowSize);
  const shown = all ? steps : steps.filter((s, i) => inWindow(i) || s.state === "failed");
  const hidden = steps.length - shown.length;
  const more =
    hidden > 0 ? (
      <button
        type="button"
        onClick={() => setAll(true)}
        className="self-start rounded-sm text-xs text-ak-muted underline underline-offset-2 hover:text-ak-text"
      >
        {running ? `Show ${hidden} earlier` : `Show ${hidden} more`}
      </button>
    ) : null;
  return (
    <Collapsible open={open} onOpenChange={setOpen} className={cn("text-sm", className)}>
      <CollapsibleTrigger className="group flex min-h-6 items-center gap-1 rounded-sm text-ak-text-2 hover:text-ak-text">
        <ChevronRight aria-hidden="true" className="size-3.5 transition-transform group-data-[state=open]:rotate-90" />
        <span className="font-medium">{summary}</span>
      </CollapsibleTrigger>
      <CollapsibleContent className="mt-1 ml-1.5 flex flex-col gap-1 border-l border-ak-line pl-3">
        {running ? more : null}
        {shown.map(({ id, ...step }) => (
          <ToolStep key={id} {...step} />
        ))}
        {running ? null : more}
      </CollapsibleContent>
    </Collapsible>
  );
}
