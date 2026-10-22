import { ApiError } from "@agentkit/agent-ui/api";
import { Button } from "@agentkit/ui/components/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@agentkit/ui/components/card";
import { Input } from "@agentkit/ui/components/input";
import { Spinner } from "@agentkit/ui/components/spinner";
import { useEffect, useId, useRef, useState, type FormEvent } from "react";
import { useApi } from "./api";
import { TokenPrompt } from "./token-prompt";

/**
 * The sign-in card (docs/UI.md "Sign in"), over the app, which is inert
 * behind it: the password card when the server has users (with "Use an API
 * token instead" when it takes keys too), else the token card. Nothing shows
 * until GET /v1/auth/config says which.
 */
export function SignIn() {
  const { prompt, methods } = useApi();
  const [useToken, setUseToken] = useState(false);
  if (prompt === null || methods === null) return null;
  const password = methods.includes("password");
  const token = methods.includes("token");
  if (password && !(token && useToken)) {
    return <PasswordCard onToken={token ? () => setUseToken(true) : undefined} />;
  }
  return <TokenPrompt onPassword={password ? () => setUseToken(false) : undefined} />;
}

type Failure = "wrong" | "throttled" | "unreachable";

function PasswordCard({ onToken }: { onToken: (() => void) | undefined }) {
  const { prompt, signIn, notice } = useApi();
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [empty, setEmpty] = useState<{ username: boolean; password: boolean }>({ username: false, password: false });
  const [failure, setFailure] = useState<Failure | null>(null);
  const [busy, setBusy] = useState(false);
  const passwordRef = useRef<HTMLInputElement>(null);
  const usernameError = useId();
  const passwordError = useId();
  const formError = useId();
  // Seconds until a throttled user may try again, counting down.
  const [wait, setWait] = useState(0);
  useEffect(() => {
    if (wait <= 0) return;
    const t = setTimeout(() => setWait((w) => w - 1), 1000);
    return () => clearTimeout(t);
  }, [wait]);
  const throttled = failure === "throttled" && wait > 0;

  async function submit(e: FormEvent) {
    e.preventDefault();
    if (busy || throttled) return;
    const u = username.trim();
    const missing = { username: u === "", password: password === "" };
    setEmpty(missing);
    if (missing.username || missing.password) return;
    setBusy(true);
    setFailure(null);
    try {
      await signIn(u, password);
      setPassword("");
    } catch (err) {
      if (err instanceof ApiError && err.status === 401) {
        setFailure("wrong");
        setPassword("");
        // After the render: the input is disabled until then.
        requestAnimationFrame(() => passwordRef.current?.focus());
      } else if (err instanceof ApiError && err.status === 429) {
        setFailure("throttled");
        setWait(Math.max(1, Math.ceil(err.retryAfter ?? 30)));
      } else {
        setFailure("unreachable");
      }
    } finally {
      setBusy(false);
    }
  }

  const error =
    failure === "wrong"
      ? "Wrong username or password."
      : throttled
        ? `Too many attempts. Try again in ${wait} s.`
        : failure === "unreachable"
          ? "Couldn't reach the server. Try again."
          : null;
  const wrong = failure === "wrong";
  const description =
    prompt === "rejected"
      ? "Your session ended. Sign in again; your draft is kept."
      : (notice ?? "Sign in with your username and password.");

  return (
    <div className="fixed inset-0 z-50 grid place-items-center bg-ak-bg p-4">
      <Card
        role="dialog"
        aria-modal="true"
        aria-labelledby="signin-title"
        aria-describedby="signin-description"
        className="w-full max-w-sm shadow-ak"
      >
        <CardHeader>
          <CardTitle id="signin-title" className="text-lg">
            Sign in to agentkit
          </CardTitle>
          <CardDescription id="signin-description" className="text-ak-muted">
            {description}
          </CardDescription>
        </CardHeader>
        <CardContent>
          <form onSubmit={submit} className="flex flex-col gap-3" noValidate aria-busy={busy}>
            <label className="flex flex-col gap-1.5 text-sm font-medium">
              Username
              <Input
                name="username"
                autoComplete="username"
                autoCapitalize="none"
                spellCheck={false}
                autoFocus
                disabled={busy}
                value={username}
                onChange={(e) => {
                  setUsername(e.target.value);
                  setEmpty((m) => ({ ...m, username: false }));
                }}
                aria-invalid={empty.username || wrong}
                aria-describedby={empty.username ? usernameError : error ? formError : undefined}
              />
            </label>
            {empty.username ? (
              <p id={usernameError} className="-mt-1.5 text-sm text-ak-bad">
                Enter your username
              </p>
            ) : null}
            <label className="flex flex-col gap-1.5 text-sm font-medium">
              Password
              <Input
                ref={passwordRef}
                name="password"
                type="password"
                autoComplete="current-password"
                disabled={busy}
                value={password}
                onChange={(e) => {
                  setPassword(e.target.value);
                  setEmpty((m) => ({ ...m, password: false }));
                }}
                aria-invalid={empty.password || wrong}
                aria-describedby={empty.password ? passwordError : error ? formError : undefined}
              />
            </label>
            {empty.password ? (
              <p id={passwordError} className="-mt-1.5 text-sm text-ak-bad">
                Enter your password
              </p>
            ) : null}
            {error ? (
              <p id={formError} role="alert" className="text-sm text-ak-bad">
                {error}
              </p>
            ) : null}
            <Button type="submit" disabled={busy || throttled}>
              {busy ? (
                <>
                  <Spinner className="size-3.5" />
                  Signing in…
                </>
              ) : (
                "Sign in"
              )}
            </Button>
            {onToken ? (
              <Button type="button" variant="link" className="h-auto p-0 text-ak-muted" onClick={onToken}>
                Use an API token instead
              </Button>
            ) : null}
          </form>
        </CardContent>
      </Card>
    </div>
  );
}
