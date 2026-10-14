// What the chat's modules share that isn't a component.

import type { Turn } from "@agentkit/agent-ui";
import type { WireError } from "@agentkit/agent-ui/api";
import type { AnswerMeta } from "./runs";

/** The composer's textarea, for "Skip to message box", "/" and New chat's focus. */
export const composerId = "composer";

/** The search box, for Mod+K. */
export const searchId = "chat-search";

/** Today, Yesterday, Previous 7 days, Older: by the conversation's last change. */
export function dateGroup(iso: string, now: Date): string {
  const t = new Date(iso);
  if (Number.isNaN(t.getTime())) return "Older";
  const day = (d: Date) => new Date(d.getFullYear(), d.getMonth(), d.getDate()).getTime();
  const days = Math.round((day(now) - day(t)) / 86_400_000);
  if (days <= 0) return "Today";
  if (days === 1) return "Yesterday";
  if (days < 7) return "Previous 7 days";
  return "Older";
}

/** Starts fetching the Markdown chunk, so the first answer doesn't wait for it. */
export function preloadMarkdown() {
  void import("@agentkit/agent-ui/markdown");
}

/** What a run error says and offers (docs/UI.md "Run errors"); Retry means Resume. */
const runErrors: Record<string, { text: string; action?: "retry" | "resume" | "new" }> = {
  rate_limited: { text: "The model provider is rate-limiting requests. Try again in a minute.", action: "retry" },
  overloaded: { text: "The model provider is overloaded right now.", action: "retry" },
  timeout: { text: "The answer took longer than the run limit.", action: "resume" },
  shutdown: { text: "The server restarted while answering.", action: "resume" },
  unexpected_stop: { text: "The model stopped unexpectedly.", action: "resume" },
  cancelled: { text: "The answer was stopped.", action: "resume" },
  max_tokens: { text: "The answer was cut off at the output limit. Ask it to continue." },
  budget_exceeded: { text: "This run used up its budget (turns or tokens)." },
  refused: { text: "The model declined this request. Rephrase it or ask something else." },
  context_too_long: { text: "This conversation is too long for the model.", action: "new" },
  model_auth: { text: "The server's model API key was rejected. Ask an admin." },
  model_rejected: { text: "The model rejected the request." },
  internal: { text: "Something went wrong on the server." },
};

export function runErrorText(error: WireError): { text: string; action?: "retry" | "resume" | "new" } {
  const known = runErrors[error.code];
  if (!known) return { text: `${error.message} (${error.code})`, ...(error.retryable ? { action: "retry" } : {}) };
  let text = known.text;
  if (error.code === "refused" && error.explanation) text = `${error.explanation} ${text}`;
  if (error.code === "model_rejected" && error.message) text = `${text} ${error.message}`;
  return { ...known, text, ...(error.retryable && !known.action ? { action: "retry" } : {}) };
}

/** The history's answers with what this tab knows of them (usage, errors), by exchange. */
export function withMeta(turns: Turn[], meta: ReadonlyMap<number, AnswerMeta>): Turn[] {
  if (meta.size === 0) return turns;
  const out: Turn[] = [];
  let exchange = 0;
  const flush = (next: Turn | undefined) => {
    // An exchange whose run failed before any text: its answer is the error alone.
    const m = meta.get(exchange);
    const last = out[out.length - 1];
    if (m && exchange > 0 && last?.role === "user" && next?.role !== "assistant" && m.state !== "done") {
      out.push({ role: "assistant", key: `m${exchange}`, parts: [], ...m });
    }
  };
  for (const t of turns) {
    if (t.role === "user") {
      flush(t);
      exchange++;
      out.push(t);
    } else {
      const m = meta.get(exchange);
      out.push(m ? { ...t, ...m, state: m.state === "streaming" ? t.state : m.state } : t);
    }
  }
  flush(undefined);
  return out;
}
