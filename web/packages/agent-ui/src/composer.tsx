import { Button } from "@agentkit/ui/components/button";
import { cn } from "@agentkit/ui";
import { ArrowUp, Square } from "lucide-react";
import { useState, type FormEvent, type KeyboardEvent } from "react";

export type ComposerProps = {
  /** Called with the text when the user sends. */
  onSend: (text: string) => void;
  /** While the conversation answers: the send button becomes Stop and Enter does nothing. */
  running?: boolean;
  onStop?: () => void;
  /** Fully disabled (no agent, agent removed), with the reason shown. */
  disabledReason?: string;
  placeholder?: string;
  className?: string;
};

/**
 * The message box: Enter sends, Shift+Enter is a new line; Stop while
 * running. T055 adds drafts per conversation, the growing textarea and the
 * "Send disabled" reasons (docs/UI.md "Composer").
 */
export function Composer({
  onSend,
  running = false,
  onStop,
  disabledReason,
  placeholder = "Message",
  className,
}: ComposerProps) {
  const [text, setText] = useState("");
  const disabled = disabledReason !== undefined;

  function send() {
    const t = text.trim();
    if (!t || running || disabled) return;
    onSend(t);
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

  return (
    <form
      onSubmit={onSubmit}
      className={cn("flex items-end gap-2 rounded-lg border border-input bg-background p-2", className)}
    >
      <textarea
        aria-label="Message"
        value={text}
        onChange={(e) => setText(e.target.value)}
        onKeyDown={onKeyDown}
        disabled={disabled}
        placeholder={disabledReason ?? placeholder}
        rows={1}
        className="max-h-48 min-h-9 flex-1 resize-none bg-transparent px-1 py-1.5 outline-none placeholder:text-ak-muted"
      />
      {running ? (
        <Button type="button" size="icon" variant="secondary" onClick={onStop} aria-label="Stop">
          <Square aria-hidden="true" />
        </Button>
      ) : (
        <Button type="submit" size="icon" disabled={disabled || text.trim() === ""} aria-label="Send">
          <ArrowUp aria-hidden="true" />
        </Button>
      )}
    </form>
  );
}
