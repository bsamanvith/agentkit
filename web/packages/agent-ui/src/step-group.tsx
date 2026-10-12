import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@agentkit/ui/components/collapsible";
import { cn } from "@agentkit/ui";
import { ChevronRight } from "lucide-react";
import { useState } from "react";
import { ToolStep, formatSeconds, type ToolStepProps } from "./tool-step";

export type StepGroupProps = {
  steps: (ToolStepProps & { id: string })[];
  className?: string;
};

/**
 * Consecutive tool calls under "Used 3 tools · 2.6 s" with a guide line.
 * One call is a plain step. Open by default up to 3 calls, collapsed above.
 * T055 adds the windowing ("Show 15 more"), always-shown failures and the
 * summary of a collapsed group (docs/UI.md "Tool calls").
 */
export function StepGroup({ steps, className }: StepGroupProps) {
  const [open, setOpen] = useState(steps.length <= 3);
  const [first] = steps;
  if (first && steps.length === 1) {
    const { id: _id, ...only } = first;
    return <ToolStep {...only} className={className} />;
  }
  const total = steps.reduce((sum, s) => sum + (s.durationMs ?? 0), 0);
  const failed = steps.filter((s) => s.state === "failed").length;
  const summary = [`Used ${steps.length} tools`, failed ? `${failed} failed` : "", formatSeconds(total)]
    .filter(Boolean)
    .join(" · ");
  return (
    <Collapsible open={open} onOpenChange={setOpen} className={cn("text-sm", className)}>
      <CollapsibleTrigger className="group flex min-h-6 items-center gap-1 rounded-sm text-ak-text-2 hover:text-ak-text">
        <ChevronRight aria-hidden="true" className="size-3.5 transition-transform group-data-[state=open]:rotate-90" />
        <span className="font-medium">{summary}</span>
      </CollapsibleTrigger>
      <CollapsibleContent className="mt-1 ml-1.5 flex flex-col gap-1 border-l border-ak-line pl-3">
        {steps.map(({ id, ...step }) => (
          <ToolStep key={id} {...step} />
        ))}
      </CollapsibleContent>
    </Collapsible>
  );
}
