import { cn } from "@agentkit/ui";
import type { Usage } from "./api/types";
import { formatSeconds } from "./tool-step";

const n = (v: unknown) => (typeof v === "number" && Number.isFinite(v) ? v : 0);

/** "2 turns · 1,234 tokens (900 cached) · 3.2 s": what is known of a run, under its answer. */
export function usageText({
  usage,
  turns,
  durationMs,
}: {
  usage?: Usage;
  turns?: number;
  durationMs?: number;
}): string {
  const parts: string[] = [];
  if (turns) parts.push(`${turns} ${turns === 1 ? "turn" : "turns"}`);
  if (usage) {
    const cached = n(usage.cache_read_input_tokens);
    const total = n(usage.input_tokens) + n(usage.output_tokens) + cached + n(usage.cache_creation_input_tokens);
    if (total > 0) {
      parts.push(
        `${total.toLocaleString("en-US")} tokens${cached > 0 ? ` (${cached.toLocaleString("en-US")} cached)` : ""}`,
      );
    }
  }
  if (durationMs !== undefined) parts.push(formatSeconds(durationMs));
  return parts.join(" · ");
}

/** The muted line under an answer: turns, tokens (cached) and time. Nothing when nothing is known. */
export function UsageLine(props: { usage?: Usage; turns?: number; durationMs?: number; className?: string }) {
  const text = usageText(props);
  if (!text) return null;
  return <p className={cn("text-xs text-ak-muted tabular-nums", props.className)}>{text}</p>;
}
