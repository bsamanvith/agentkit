import { AgentClient } from "@agentkit/agent-ui/api";
import { useCallback, useMemo, useState, type ReactNode } from "react";
import { AuthContext, tokenStorageKey, type Auth } from "./api";

/** The token, in memory and in sessionStorage. Storage may be blocked; then it lasts for this page. */
class TokenStore {
  #value: string | undefined;

  constructor() {
    try {
      this.#value = sessionStorage.getItem(tokenStorageKey) ?? undefined;
    } catch {
      this.#value = undefined;
    }
  }

  get(): string | undefined {
    return this.#value;
  }

  set(token: string | undefined) {
    this.#value = token;
    try {
      if (token === undefined) sessionStorage.removeItem(tokenStorageKey);
      else sessionStorage.setItem(tokenStorageKey, token);
    } catch {
      // Storage blocked.
    }
  }
}

/** One AgentClient for the app, and the API token it sends. */
export function ApiProvider({ children, client: given }: { children: ReactNode; client?: AgentClient }) {
  const [store] = useState(() => new TokenStore());
  const [hasToken, setHasToken] = useState(() => store.get() !== undefined);
  const [epoch, setEpoch] = useState(0);
  const [prompt, setPrompt] = useState<Auth["prompt"]>(null);
  const client = useMemo(() => given ?? new AgentClient({ token: () => store.get() }), [given, store]);

  const setToken = useCallback(
    (t: string) => {
      store.set(t);
      setHasToken(true);
      setPrompt(null);
      setEpoch((e) => e + 1);
    },
    [store],
  );
  const forgetToken = useCallback(() => {
    store.set(undefined);
    setHasToken(false);
    setPrompt("needed");
    setEpoch((e) => e + 1);
  }, [store]);
  const unauthorized = useCallback(() => setPrompt(store.get() ? "rejected" : "needed"), [store]);

  const value = useMemo(
    () => ({ client, hasToken, epoch, prompt, setToken, forgetToken, unauthorized }),
    [client, hasToken, epoch, prompt, setToken, forgetToken, unauthorized],
  );
  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}
