import { lazy, Suspense, useCallback, useEffect, useState } from "react";
import { App } from "./app";
import type { Lane, Me } from "./account-api";
import { rememberedHolding, session } from "./session";

// The sign-in screen loads only when it is needed.
const SignIn = lazy(() => import("./signin").then((m) => ({ default: m.SignIn })));

type State =
  { kind: "loading" } | { kind: "open" } | { kind: "signin"; lanes: Lane[] } | { kind: "in"; lanes: Lane[]; me: Me };

/** Asks the server whether it has sign-in, and who is signed in. */
async function load(): Promise<State> {
  const lanesResp = await fetch("/auth/lanes");
  if (lanesResp.status === 404) return { kind: "open" };
  const { lanes } = (await lanesResp.json()) as { lanes: Lane[] };
  const me = (holding: string) => fetch("/api/me", { headers: holding ? { "X-Farm-Holding": holding } : {} });
  let resp = await me(rememberedHolding());
  if (resp.status === 404) resp = await me(""); // that farm is no longer theirs
  if (!resp.ok) {
    session.csrf = session.holding = "";
    return { kind: "signin", lanes };
  }
  const m = (await resp.json()) as Me;
  session.csrf = m.csrf;
  session.holding = m.holding.id;
  return { kind: "in", lanes, me: m };
}

/**
 * Gate: without sign-in on the server (no /auth/lanes) the app opens as it
 * always did; with it, the app opens for the signed-in account's farm, or
 * the sign-in screen does.
 */
export function Gate() {
  const [state, setState] = useState<State>({ kind: "loading" });
  const check = useCallback(() => {
    load().then(setState, () => setState({ kind: "open" }));
  }, []);
  useEffect(() => {
    check();
    window.addEventListener("farm:signed-out", check);
    return () => window.removeEventListener("farm:signed-out", check);
  }, [check]);

  switch (state.kind) {
    case "loading":
      return null;
    case "open":
      return <App />;
    case "signin":
      return (
        <Suspense fallback={null}>
          <SignIn lanes={state.lanes} onSignedIn={check} />
        </Suspense>
      );
    case "in":
      return <App account={{ me: state.me, lanes: state.lanes, reload: check }} />;
  }
}
