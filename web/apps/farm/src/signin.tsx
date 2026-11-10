import { Button } from "@agentkit/ui/components/button";
import { Input } from "@agentkit/ui/components/input";
import { KeyRound, Sprout } from "lucide-react";
import { useState, type FormEvent, type ReactNode } from "react";
import { auth, failureText, laneLabel, type Lane } from "./account-api";

function message(e: unknown): string {
  return e instanceof Error ? e.message : String(e);
}

/**
 * The sign-in screen: one way in per lane the server has, in its order.
 * Google is one tap on most phones; a farmer a field partner signed up
 * types the code they were given.
 */
export function SignIn({ lanes, onSignedIn }: { lanes: Lane[]; onSignedIn: () => void }) {
  const q = new URLSearchParams(window.location.search);
  const [error, setError] = useState(q.get("signin") === "failed" ? failureText(q.get("reason") ?? "") : "");
  const [busy, setBusy] = useState(false);

  const run = (p: Promise<unknown>) => {
    setBusy(true);
    setError("");
    p.then(
      () => {
        window.history.replaceState(null, "", window.location.pathname);
        onSignedIn();
      },
      (e: unknown) => setError(message(e)),
    ).finally(() => setBusy(false));
  };

  return (
    <div className="flex min-h-dvh items-center justify-center bg-ak-bg p-4">
      <div className="flex w-full max-w-sm flex-col gap-4 rounded-lg border bg-ak-surface p-5 shadow-sm">
        <h1 className="flex items-center gap-2 text-xl font-semibold">
          <Sprout className="size-6 text-ak-ok" aria-hidden="true" /> My land
        </h1>
        <p className="text-sm text-muted-foreground">Sign in to see your plots, plans and alerts.</p>
        {error && (
          <p role="alert" className="text-sm text-ak-bad">
            {error}
          </p>
        )}
        {lanes.map((l) => (
          <LaneForm key={l.name} lane={l} busy={busy} run={run} onError={setError} />
        ))}
        {lanes.some((l) => l.sign_up) && (
          <p className="text-xs text-muted-foreground">
            New here? Signing in with{" "}
            {lanes
              .filter((l) => l.sign_up)
              .map((l) => laneLabel(l.name))
              .join(" or ")}{" "}
            makes your account.
          </p>
        )}
      </div>
    </div>
  );
}

function Section({
  title,
  children,
  onSubmit,
}: {
  title: string;
  children: ReactNode;
  onSubmit: (e: FormEvent) => void;
}) {
  return (
    <form className="flex flex-col gap-2 border-t pt-3" aria-label={title} onSubmit={onSubmit}>
      <h2 className="text-sm font-medium">{title}</h2>
      {children}
    </form>
  );
}

function LaneForm({
  lane,
  busy,
  run,
  onError,
}: {
  lane: Lane;
  busy: boolean;
  run: (p: Promise<unknown>) => void;
  onError: (e: string) => void;
}) {
  const [id, setId] = useState("");
  const [secret, setSecret] = useState("");
  const [sentTo, setSentTo] = useState("");
  const label = laneLabel(lane.name);

  if (lane.kind === "redirect") {
    return (
      <Button
        size="lg"
        disabled={busy}
        onClick={() => {
          auth.start(lane.name).catch((e: unknown) => onError(message(e)));
        }}
      >
        Continue with {label}
      </Button>
    );
  }
  if (lane.kind === "secret" && lane.name === "invite") {
    return (
      <Section
        title={label}
        onSubmit={(e) => {
          e.preventDefault();
          run(auth.check(lane.name, "", secret));
        }}
      >
        <Input
          aria-label="Sign-in code"
          placeholder="XXXX-XXXX-XXXX"
          autoCapitalize="characters"
          autoComplete="one-time-code"
          value={secret}
          onChange={(e) => setSecret(e.target.value)}
        />
        <Button type="submit" variant="outline" disabled={busy || secret.trim() === ""}>
          <KeyRound /> Sign in with the code
        </Button>
      </Section>
    );
  }
  if (lane.kind === "secret") {
    return (
      <Section
        title={label}
        onSubmit={(e) => {
          e.preventDefault();
          run(auth.check(lane.name, id, secret));
        }}
      >
        <Input aria-label="Username" autoComplete="username" value={id} onChange={(e) => setId(e.target.value)} />
        <Input
          aria-label="Password"
          type="password"
          autoComplete="current-password"
          value={secret}
          onChange={(e) => setSecret(e.target.value)}
        />
        <Button type="submit" variant="outline" disabled={busy || id === "" || secret === ""}>
          Sign in
        </Button>
      </Section>
    );
  }
  // A code lane: send a code to a phone or an address, then type it.
  const email = lane.name === "email";
  return (
    <Section
      title={label}
      onSubmit={(e) => {
        e.preventDefault();
        if (!sentTo) {
          auth.send(lane.name, id).then(
            (r) => setSentTo(r.to),
            (err: unknown) => onError(message(err)),
          );
        } else {
          run(auth.verify(lane.name, sentTo, secret));
        }
      }}
    >
      <Input
        aria-label={email ? "Email address" : "Phone number"}
        type={email ? "email" : "tel"}
        inputMode={email ? "email" : "tel"}
        autoComplete={email ? "email" : "tel"}
        value={id}
        disabled={sentTo !== ""}
        onChange={(e) => setId(e.target.value)}
      />
      {sentTo && (
        <Input
          aria-label="The code you got"
          inputMode="numeric"
          autoComplete="one-time-code"
          value={secret}
          onChange={(e) => setSecret(e.target.value)}
        />
      )}
      <Button type="submit" variant="outline" disabled={busy || id === ""}>
        {sentTo ? "Sign in" : "Send me a code"}
      </Button>
    </Section>
  );
}
