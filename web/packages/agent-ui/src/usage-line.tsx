import { cn } from "@agentkit/ui";
import type { Usage } from "./api/types";
import { formatSeconds } from "./tool-step";

const n = (v: unknown) => (typeof v === "number" && Number.isFinite(v) ? v : 0);

/** The name a model is shown by: without the provider for the default one's ("anthropic/"). */
export function modelLabel(model: string): string {
  return model.startsWith("anthropic/") ? model.slice("anthropic/".length) : model;
}

/** "2 turns · 1,234 tokens (900 cached) · 3.2 s · claude-opus-5-5": what is known of a run, under its answer. */
export function usageText({
  usage,
  turns,
  durationMs,
  model,
}: {
  usage?: Usage;
  turns?: number;
  durationMs?: number;
  model?: string;
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
  if (model) parts.push(modelLabel(model));
  return parts.join(" · ");
}

/** The muted line under an answer: turns, tokens (cached), time and model. Nothing when nothing is known. */
export function UsageLine(props: {
  usage?: Usage;
  turns?: number;
  durationMs?: number;
  model?: string;
  className?: string;
}) {
  const text = usageText(props);
  if (!text) return null;
  return <p className={cn("text-xs text-ak-muted tabular-nums", props.className)}>{text}</p>;
}
