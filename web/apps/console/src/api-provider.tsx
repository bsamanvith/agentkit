import { AgentClient, type Me } from "@agentkit/agent-ui/api";
import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { AuthContext, tokenStorageKey, type Auth } from "./api";

/**
 * The token, in memory, and in sessionStorage once a request with it has
 * succeeded, so a reload never takes a refused token for an expired one.
 * Storage may be blocked; then it lasts for this page.
 */
class TokenStore {
  #value: string | undefined;
  #unproven = false;

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

  /** Whether the token was entered in this page and no request with it has succeeded yet. */
  get unproven(): boolean {
    return this.#unproven;
  }

  proven() {
    if (!this.#unproven || this.#value === undefined) return;
    this.#unproven = false;
    this.#save(this.#value);
  }

  /** Sets a token the user just entered (kept once proven), or forgets it. */
  set(token: string | undefined) {
    this.#value = token;
    this.#unproven = token !== undefined;
    this.#save(undefined);
  }

  #save(token: string | undefined) {
    try {
      if (token === undefined) sessionStorage.removeItem(tokenStorageKey);
      else sessionStorage.setItem(tokenStorageKey, token);
    } catch {
      // Storage blocked.
    }
  }
}

/** One AgentClient for the app, and how it signs in: the API token it sends, or a session. */
export function ApiProvider({ children, client: given }: { children: ReactNode; client?: AgentClient }) {
  const [store] = useState(() => new TokenStore());
  const [hasToken, setHasToken] = useState(() => store.get() !== undefined);
  const [epoch, setEpoch] = useState(0);
  const [prompt, setPrompt] = useState<Auth["prompt"]>(null);
  const [methods, setMethods] = useState<string[] | null>(null);
  const [me, setMe] = useState<Me | null>(null);
  const [cleared, setCleared] = useState(0);
  const [notice, setNotice] = useState<string | null>(null);
  // Whether a session was signed in on this page (a later 401 means it
  // ended), and whose: another user signing in gets a fresh start.
  const session = useRef(false);
  const lastUser = useRef<string | undefined>(undefined);
  const client = useMemo(() => given ?? new AgentClient({ token: () => store.get() }), [given, store]);

  // The card asks the server how to sign in, once, when it first opens.
  const asking = prompt !== null && methods === null;
  useEffect(() => {
    if (!asking) return;
    const ctl = new AbortController();
    client.authConfig({ signal: ctl.signal }).then(
      (c) => setMethods(c.methods.length > 0 ? c.methods : ["token"]),
      () => {
        if (!ctl.signal.aborted) setMethods(["token"]);
      },
    );
    return () => ctl.abort();
  }, [asking, client]);

  const known = useCallback((m: Me) => {
    setMe(m);
    if (m.credential === "session") session.current = true;
    if (m.username) lastUser.current = m.username;
  }, []);

  const setToken = useCallback(
    (t: string) => {
      session.current = false;
      store.set(t);
      setHasToken(true);
      setPrompt(null);
      setNotice(null);
      setEpoch((e) => e + 1);
    },
    [store],
  );
  const forgetToken = useCallback(() => {
    store.set(undefined);
    setHasToken(false);
    setMe(null);
    setPrompt("needed");
    setCleared((c) => c + 1);
    setEpoch((e) => e + 1);
  }, [store]);
  const unauthorized = useCallback(
    () =>
      setPrompt(
        session.current ? "rejected" : store.get() === undefined ? "needed" : store.unproven ? "refused" : "rejected",
      ),
    [store],
  );
  const accepted = useCallback(() => store.proven(), [store]);
  const loadMe = useCallback(() => {
    client.me().then(known, () => {
      // Not signed in, or an older server: the menu just says less.
    });
  }, [client, known]);

  const signIn = useCallback(
    async (username: string, password: string) => {
      await client.login(username, password);
      // A token would win over the session cookie (the server prefers a bearer).
      store.set(undefined);
      setHasToken(false);
      const m = await client.me();
      if (lastUser.current !== undefined && lastUser.current !== m.username) setCleared((c) => c + 1);
      known(m);
      setNotice(null);
      setPrompt(null);
      setEpoch((e) => e + 1);
    },
    [client, known, store],
  );
  const signOut = useCallback(async () => {
    let ended = true;
    try {
      await client.logout();
    } catch {
      ended = false;
    }
    session.current = false;
    lastUser.current = undefined;
    store.set(undefined);
    setHasToken(false);
    setMe(null);
    setNotice(ended ? null : "Signed out on this device. The server session ends when it expires.");
    setPrompt("needed");
    setCleared((c) => c + 1);
    setEpoch((e) => e + 1);
  }, [client, store]);

  const value = useMemo(
    () => ({
      client,
      hasToken,
      methods,
      me,
      loadMe,
      signIn,
      signOut,
      cleared,
      notice,
      epoch,
      prompt,
      setToken,
      forgetToken,
      unauthorized,
      accepted,
    }),
    [
      client,
      hasToken,
      methods,
      me,
      loadMe,
      signIn,
      signOut,
      cleared,
      notice,
      epoch,
      prompt,
      setToken,
      forgetToken,
      unauthorized,
      accepted,
    ],
  );
  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}
