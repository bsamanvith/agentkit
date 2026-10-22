import { Button } from "@agentkit/ui/components/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@agentkit/ui/components/card";
import { Input } from "@agentkit/ui/components/input";
import { useId, useState, type FormEvent } from "react";
import { useApi } from "./api";

/**
 * The API token card (docs/UI.md "Sign in", token mode), over the app,
 * which is inert behind it. SignIn (sign-in.tsx) shows it when the server
 * takes tokens and not passwords, or the user asked for it; onPassword goes
 * back to the password card.
 */
export function TokenPrompt({ onPassword }: { onPassword?: (() => void) | undefined }) {
  const { prompt, setToken, notice } = useApi();
  const [value, setValue] = useState("");
  const [invalid, setInvalid] = useState<null | "empty" | "unsendable">(null);
  const errorId = useId();
  if (prompt === null) return null;

  function submit(e: FormEvent) {
    e.preventDefault();
    const t = value.trim();
    if (!t) {
      setInvalid("empty");
      return;
    }
    // A token is printable ASCII. A pasted zero-width space or curly quote
    // can't go in a header (fetch throws), so say so here, with no request.
    if (!/^[\x21-\x7e]+$/.test(t)) {
      setInvalid("unsendable");
      return;
    }
    setValue("");
    setToken(t);
  }

  // docs/UI.md "Sign in": the token card's errors.
  const error =
    invalid === "empty"
      ? "Enter a token"
      : invalid === "unsendable" || prompt === "refused"
        ? "That token wasn't accepted."
        : prompt === "rejected"
          ? "Your token was rejected or has expired."
          : null;
  return (
    <div className="fixed inset-0 z-50 grid place-items-center bg-ak-bg p-4">
      <Card role="dialog" aria-modal="true" aria-labelledby="token-title" className="w-full max-w-sm shadow-ak">
        <CardHeader>
          <CardTitle id="token-title" className="text-lg">
            Sign in to agentkit
          </CardTitle>
          <CardDescription className="text-ak-muted">
            {notice ?? "Paste an API token; it's kept for this tab only."}
          </CardDescription>
        </CardHeader>
        <CardContent>
          <form onSubmit={submit} className="flex flex-col gap-3" noValidate>
            <label className="flex flex-col gap-1.5 text-sm font-medium">
              API token
              <Input
                type="password"
                autoComplete="off"
                spellCheck={false}
                autoFocus
                value={value}
                onChange={(e) => {
                  setValue(e.target.value);
                  setInvalid(null);
                }}
                aria-invalid={error !== null}
                aria-describedby={error ? errorId : undefined}
              />
            </label>
            {error ? (
              <p id={errorId} className="text-sm text-ak-bad">
                {error}
              </p>
            ) : null}
            <Button type="submit">Continue</Button>
            {onPassword ? (
              <Button type="button" variant="link" className="h-auto p-0 text-ak-muted" onClick={onPassword}>
                Sign in with a password instead
              </Button>
            ) : null}
          </form>
        </CardContent>
      </Card>
    </div>
  );
}
