import { cn } from "@agentkit/ui";
import type { ReactNode } from "react";

export type MessageProps = {
  role: "user" | "assistant";
  /** The text, rendered as React text, never as HTML. */
  text: string;
  /**
   * What to show in place of an assistant's text: the console passes the
   * Markdown renderer's output (markdown.tsx), which it loads lazily.
   */
  body?: ReactNode;
  /** Pending (sent, no run_start yet) or streaming. */
  state?: "done" | "pending" | "streaming";
  /** Steps, chips and the usage line, under an assistant's text. */
  children?: ReactNode;
  /** The assistant's run, as data-run-id (support, and the browser tests). */
  runId?: string;
  className?: string;
};

/**
 * A message in the thread: the user's as a grey bubble on the right, the
 * assistant's as plain text (docs/UI.md "Chat").
 */
export function Message({ role, text, body, state = "done", children, runId, className }: MessageProps) {
  if (role === "user") {
    return (
      <div className={cn("flex justify-end", className)} data-role="user">
        <div
          className={cn(
            "max-w-[85%] rounded-lg bg-ak-select px-3 py-2 break-words whitespace-pre-wrap",
            state === "pending" ? "text-ak-muted" : "text-ak-text",
          )}
        >
          {text}
        </div>
      </div>
    );
  }
  return (
    <div
      className={cn("flex flex-col gap-2", className)}
      data-role="assistant"
      data-run-id={runId}
      aria-busy={state === "streaming"}
    >
      {body ?? (text ? <div className="break-words whitespace-pre-wrap text-ak-text">{text}</div> : null)}
      {children}
    </div>
  );
}
