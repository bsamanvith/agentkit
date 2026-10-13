// The console's connection to /v1: the context ApiProvider (api-provider.tsx)
// fills, and a hook for loading a resource that turns a 401 into the token
// prompt.
//
// Auth before T061 (E11 "Auth in the web client"): the user pastes an API
// token once; it is kept in sessionStorage (cleared with the tab) and sent
// as Authorization: Bearer. A server without a token (loopback dev) needs
// none, so the prompt shows only after a 401.

import { ApiError, type AgentClient } from "@agentkit/agent-ui/api";
import { createContext, useContext, useEffect, useEffectEvent, useState } from "react";

/** The sessionStorage key of the API token. Never localStorage (T055's mutation list). */
export const tokenStorageKey = "agentkit.token";

export type Auth = {
  client: AgentClient;
  /** Whether a token is set (its value never leaves the provider). */
  hasToken: boolean;
  /** Bumped when the token changes, so loaded resources reload. */
  epoch: number;
  /**
   * Why the token prompt is open, or null when it isn't: no token yet, the
   * token just entered was refused, or a token that worked was rejected
   * later (expired or revoked).
   */
  prompt: null | "needed" | "refused" | "rejected";
  setToken: (token: string) => void;
  forgetToken: () => void;
  unauthorized: () => void;
  /** A request with the current token succeeded: a later 401 means it was revoked. */
  accepted: () => void;
};

export const AuthContext = createContext<Auth | null>(null);

export function useApi(): Auth {
  const ctx = useContext(AuthContext);
  if (!ctx) throw new Error("useApi needs an <ApiProvider> above it");
  return ctx;
}

export type Resource<T> =
  { state: "loading" } | { state: "ready"; data: T } | { state: "failed"; error: Error; retry: () => void };

/**
 * Loads a resource with the client, again whenever the token changes. A 401
 * opens the token prompt instead of failing, and is not retried until a new
 * token arrives: no request loop. With skip, nothing is loaded (hooks can't
 * be conditional) and it stays "loading".
 */
export function useResource<T>(
  load: (client: AgentClient, signal: AbortSignal) => Promise<T>,
  key: string,
  skip = false,
): Resource<T> {
  const { client, epoch, prompt, unauthorized, accepted } = useApi();
  const [attempt, setAttempt] = useState(0);
  // A result belongs to one load: a newer one (another key, token or retry)
  // shows as loading until it settles, without a setState in the effect.
  const loadId = `${key}\n${epoch}\n${attempt}`;
  const [settled, setSettled] = useState<{ loadId: string; res: Resource<T> } | null>(null);
  const run = useEffectEvent((signal: AbortSignal) => load(client, signal));

  useEffect(() => {
    if (skip || prompt !== null) return; // not wanted, or waiting for a token
    const ctl = new AbortController();
    run(ctl.signal).then(
      (data) => {
        if (ctl.signal.aborted) return; // a newer load owns the page
        accepted();
        setSettled({ loadId, res: { state: "ready", data } });
      },
      (error: unknown) => {
        if (ctl.signal.aborted) return;
        if (error instanceof ApiError && error.status === 401) {
          unauthorized();
          return;
        }
        setSettled({
          loadId,
          res: {
            state: "failed",
            error: error instanceof Error ? error : new Error(String(error)),
            retry: () => setAttempt((a) => a + 1),
          },
        });
      },
    );
    return () => ctl.abort();
  }, [client, prompt, unauthorized, accepted, loadId, skip]);

  return settled?.loadId === loadId ? settled.res : { state: "loading" };
}
