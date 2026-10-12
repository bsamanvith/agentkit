import type { ReactNode } from "react";
import { cn } from "./lib/utils";

export type StatusTone = "ok" | "warn" | "bad" | "neutral";

const dot: Record<StatusTone, string> = {
  ok: "bg-ak-ok",
  warn: "bg-ak-warn",
  bad: "bg-ak-bad",
  neutral: "bg-ak-muted",
};

/**
 * A status: a small dot plus a word, never colour alone (docs/UI.md
 * "Accessibility"). With hideLabel the word is kept for screen readers only.
 */
export function Status({
  tone,
  children,
  hideLabel = false,
  className,
}: {
  tone: StatusTone;
  children: ReactNode;
  hideLabel?: boolean;
  className?: string;
}) {
  return (
    <span className={cn("inline-flex items-center gap-1.5 text-ak-text-2", className)} data-tone={tone}>
      <span aria-hidden="true" className={cn("size-2 shrink-0 rounded-full", dot[tone])} />
      <span className={cn(hideLabel && "sr-only")}>{children}</span>
    </span>
  );
}
