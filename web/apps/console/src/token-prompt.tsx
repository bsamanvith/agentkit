import { Button } from "@agentkit/ui/components/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@agentkit/ui/components/card";
import { Input } from "@agentkit/ui/components/input";
import { useId, useState, type FormEvent } from "react";
import { useApi } from "./api";

/**
 * The API token card (docs/UI.md "Sign in", token mode), over the app,
 * which is inert behind it. T061's password sign-in replaces it when the
 * server has users.
 */
export function TokenPrompt() {
  const { prompt, setToken } = useApi();
  const [value, setValue] = useState("");
  const [empty, setEmpty] = useState(false);
  const errorId = useId();
  if (prompt === null) return null;

  function submit(e: FormEvent) {
    e.preventDefault();
    const t = value.trim();
    if (!t) {
      setEmpty(true);
      return;
    }
    setValue("");
    setToken(t);
  }

  // docs/UI.md "Sign in": the token card's errors.
  const error = empty
    ? "Enter a token"
    : prompt === "refused"
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
            Paste an API token; it&apos;s kept for this tab only.
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
                  setEmpty(false);
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
          </form>
        </CardContent>
      </Card>
    </div>
  );
}
