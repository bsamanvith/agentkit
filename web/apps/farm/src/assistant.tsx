import { Markdown } from "@agentkit/agent-ui/markdown";
import { Button } from "@agentkit/ui/components/button";
import { Input } from "@agentkit/ui/components/input";
import { Send, X } from "lucide-react";
import { useState, type FormEvent } from "react";
import { api } from "./api";

interface Turn {
  who: "you" | "assistant";
  text: string;
  tools?: string[];
}

/** The farm assistant: an agentkit agent with the farm tools, in the farmer's language. */
export function Assistant({ onClose }: { onClose: () => void }) {
  const [turns, setTurns] = useState<Turn[]>([]);
  const [conversation, setConversation] = useState<string>();
  const [message, setMessage] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const send = async (e: FormEvent) => {
    e.preventDefault();
    const text = message.trim();
    if (!text || busy) return;
    setTurns((t) => [...t, { who: "you", text }]);
    setMessage("");
    setBusy(true);
    setError("");
    try {
      const r = await api.ask(text, conversation);
      setConversation(r.conversation);
      setTurns((t) => [...t, { who: "assistant", text: r.text, tools: r.tools }]);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <aside aria-label="Farm assistant" className="flex h-full flex-col border-l bg-background">
      <div className="flex items-center justify-between border-b p-3">
        <h2 className="font-semibold">Ask about your land</h2>
        <Button variant="ghost" size="icon-sm" onClick={onClose} aria-label="Close the assistant">
          <X />
        </Button>
      </div>
      <div className="flex flex-1 flex-col gap-3 overflow-y-auto p-3" aria-live="polite">
        {turns.length === 0 && (
          <p className="text-sm text-muted-foreground">
            Ask in any language: “How big is my east field in guntas?”, “నా పొలాలు అన్నీ సరిగ్గా ఉన్నాయా?”
          </p>
        )}
        {turns.map((t, i) => (
          <div
            key={i}
            className={
              t.who === "you" ? "self-end rounded-lg bg-primary px-3 py-2 text-sm text-primary-foreground" : "text-sm"
            }
          >
            {t.who === "you" ? <p className="whitespace-pre-wrap">{t.text}</p> : <Markdown text={t.text} />}
            {t.tools && t.tools.length > 0 && (
              <p className="mt-1 text-xs text-muted-foreground">Used {t.tools.join(", ")}</p>
            )}
          </div>
        ))}
        {busy && <p className="text-sm text-muted-foreground">Thinking…</p>}
        {error && (
          <p className="text-sm text-ak-bad" role="alert">
            {error}
          </p>
        )}
      </div>
      <form onSubmit={send} className="flex gap-2 border-t p-3">
        <Input aria-label="Message" value={message} onChange={(e) => setMessage(e.target.value)} placeholder="Ask…" />
        <Button type="submit" size="icon" disabled={busy || !message.trim()} aria-label="Send">
          <Send />
        </Button>
      </form>
    </aside>
  );
}
