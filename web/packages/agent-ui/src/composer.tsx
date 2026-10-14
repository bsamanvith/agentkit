import { Button } from "@agentkit/ui/components/button";
import { cn } from "@agentkit/ui";
import { ArrowUp, Square } from "lucide-react";
import { useState, type FormEvent, type KeyboardEvent } from "react";

export type ComposerProps = {
  /** Called with the text when the user sends. */
  onSend: (text: string) => void;
  /** While the conversation answers: the send button becomes Stop and Enter does nothing (the draft waits). */
  running?: boolean;
  onStop?: () => void;
  /** Fully disabled (no agent, agent removed), with the reason shown. */
  disabledReason?: string;
  /** Typing works but Send doesn't (offline, a pending message), with the reason shown. */
  sendDisabledReason?: string;
  placeholder?: string;
  /** The draft, when the caller keeps it (per conversation); otherwise the composer keeps its own. */
  value?: string;
  onValueChange?: (text: string) => void;
  /** The textarea's id, for "Skip to message box" and focus. */
  id?: string;
  className?: string;
};

/**
 * The message box (docs/UI.md "Composer"): Enter sends, Shift+Enter is a
 * new line. While running the send button is Stop and Enter does nothing.
 * The box grows with its text up to 8 lines, then scrolls.
 */
export function Composer({
  onSend,
  running = false,
  onStop,
  disabledReason,
  sendDisabledReason,
  placeholder = "Message",
  value,
  onValueChange,
  id,
  className,
}: ComposerProps) {
  const [own, setOwn] = useState("");
  const text = value ?? own;
  const setText = (t: string) => {
    if (value === undefined) setOwn(t);
    onValueChange?.(t);
  };
  const disabled = disabledReason !== undefined;
  const canSend = !running && !disabled && sendDisabledReason === undefined && text.trim() !== "";

  function send() {
    if (!canSend) return;
    onSend(text.trim());
    setText("");
  }
  function onKeyDown(e: KeyboardEvent<HTMLTextAreaElement>) {
    if (e.key === "Enter" && !e.shiftKey && !e.nativeEvent.isComposing) {
      e.preventDefault();
      send();
    }
  }
  function onSubmit(e: FormEvent) {
    e.preventDefault();
    send();
  }

  const reason = disabledReason ?? sendDisabledReason;
  return (
    <form onSubmit={onSubmit} className={cn("flex flex-col gap-1", className)}>
      <div className="flex items-end gap-2 rounded-lg border border-input bg-background p-2 focus-within:border-ak-line-strong">
        <textarea
          id={id}
          aria-label="Message"
          aria-describedby={reason && id ? `${id}-reason` : undefined}
          value={text}
          onChange={(e) => setText(e.target.value)}
          onKeyDown={onKeyDown}
          disabled={disabled}
          placeholder={disabledReason ?? placeholder}
          rows={1}
          className="max-h-[calc(8lh+12px)] min-h-9 flex-1 resize-none bg-transparent px-1 py-1.5 outline-none field-sizing-content placeholder:text-ak-muted"
        />
        {running ? (
          <Button type="button" size="icon" variant="secondary" onClick={onStop} aria-label="Stop">
            <Square aria-hidden="true" />
          </Button>
        ) : (
          <Button type="submit" size="icon" disabled={!canSend} aria-label="Send">
            <ArrowUp aria-hidden="true" />
          </Button>
        )}
      </div>
      {sendDisabledReason && !disabled ? (
        <p id={id ? `${id}-reason` : undefined} className="px-1 text-xs text-ak-muted">
          {sendDisabledReason}
        </p>
      ) : null}
    </form>
  );
}
