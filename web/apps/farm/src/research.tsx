import { Button } from "@agentkit/ui/components/button";
import { Input } from "@agentkit/ui/components/input";
import { Status } from "@agentkit/ui";
import { ExternalLink, Globe, X } from "lucide-react";
import { useEffect, useState } from "react";
import { api } from "./api";
import type { ResearchProvider } from "./types";

function message(e: unknown): string {
  return e instanceof Error ? e.message : String(e);
}

/**
 * Web research settings: the farmer connects Exa or Parallel with their own
 * key, which the server seals in its vault and never sends back. The
 * assistant then searches the web for seeds, prices and practices.
 */
export function ResearchSettings({ onClose }: { onClose: () => void }) {
  const [providers, setProviders] = useState<ResearchProvider[]>();
  const [keys, setKeys] = useState<Record<string, string>>({});
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");

  useEffect(() => {
    api.research().then(setProviders, (e: unknown) => setError(message(e)));
  }, []);

  const act = (id: string, p: Promise<ResearchProvider[]>) => {
    setBusy(id);
    setError("");
    p.then(
      (ps) => {
        setProviders(ps);
        setKeys((k) => ({ ...k, [id]: "" }));
      },
      (e: unknown) => setError(message(e)),
    ).finally(() => setBusy(""));
  };

  return (
    <div
      className="fixed inset-0 z-[1100] flex items-center justify-center bg-black/40 p-4"
      onClick={onClose}
      role="presentation"
    >
      <div
        role="dialog"
        aria-modal="true"
        aria-labelledby="research-title"
        className="flex w-full max-w-md flex-col gap-4 rounded-lg border bg-background p-5 shadow-lg"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="flex items-center justify-between">
          <h2 id="research-title" className="flex items-center gap-2 font-semibold">
            <Globe className="size-5" aria-hidden="true" /> Web research
          </h2>
          <Button variant="ghost" size="sm" onClick={onClose} aria-label="Close">
            <X />
          </Button>
        </div>
        <p className="text-sm text-muted-foreground">
          Connect a search service with your own key so the assistant can look up seed varieties, prices and practices
          across India and abroad. Your key is locked in this app&apos;s vault and never shown again.
        </p>
        {error && (
          <p role="alert" className="text-sm text-ak-bad">
            {error}
          </p>
        )}
        {!providers && !error && <p className="text-sm text-muted-foreground">Loading…</p>}
        {providers?.map((p) => (
          <form
            key={p.id}
            className="flex flex-col gap-2 rounded-md border p-3"
            aria-label={p.name}
            onSubmit={(e) => {
              e.preventDefault();
              act(p.id, api.connectResearch(p.id, keys[p.id] ?? ""));
            }}
          >
            <div className="flex items-center justify-between">
              <span className="font-medium">{p.name}</span>
              <Status tone={p.connected ? "ok" : "neutral"}>{p.connected ? "Connected" : "Not connected"}</Status>
            </div>
            <div className="flex gap-2">
              <Input
                type="password"
                autoComplete="off"
                placeholder={p.connected ? "Paste a new key to replace it" : "Paste your API key"}
                aria-label={`${p.name} API key`}
                value={keys[p.id] ?? ""}
                onChange={(e) => setKeys((k) => ({ ...k, [p.id]: e.target.value }))}
              />
              <Button type="submit" size="sm" disabled={busy === p.id || !(keys[p.id] ?? "").trim()}>
                {p.connected ? "Replace" : "Connect"}
              </Button>
            </div>
            <div className="flex items-center justify-between text-xs">
              <a href={p.sign_up} target="_blank" rel="noreferrer" className="flex items-center gap-1 underline">
                Get a key <ExternalLink className="size-3" aria-hidden="true" />
              </a>
              {p.connected && (
                <button
                  type="button"
                  className="text-ak-bad underline"
                  disabled={busy === p.id}
                  onClick={() => act(p.id, api.disconnectResearch(p.id))}
                >
                  Disconnect
                </button>
              )}
            </div>
          </form>
        ))}
      </div>
    </div>
  );
}
