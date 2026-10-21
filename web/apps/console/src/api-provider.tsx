import { AgentClient, type Me } from "@agentkit/agent-ui/api";
import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { AuthContext, tokenStorageKey, userStorageKey, type Auth } from "./api";

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

function storedUser(): string | undefined {
  try {
    return sessionStorage.getItem(userStorageKey) ?? undefined;
  } catch {
    return undefined;
  }
}

function storeUser(user: string | undefined) {
  try {
    if (user === undefined) sessionStorage.removeItem(userStorageKey);
    else sessionStorage.setItem(userStorageKey, user);
  } catch {
    // Storage blocked: this page remembers it.
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
  // ended), and whose the drafts are (kept across a reload): another user
  // signing in gets a fresh start.
  const session = useRef(false);
  const lastUser = useRef<string | undefined>(undefined);
  if (lastUser.current === undefined) lastUser.current = storedUser();
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

  // known takes in who /v1/auth/me says is signed in. Drafts of another
  // user are cleared, as are those of no known user on a sign-in (they may
  // be anyone's, from before a reload).
  const known = useCallback((m: Me, signedIn: boolean) => {
    setMe(m);
    if (m.credential === "session") session.current = true;
    if (!m.username) return;
    const last = lastUser.current;
    if (last !== m.username && (last !== undefined || signedIn)) setCleared((c) => c + 1);
    lastUser.current = m.username;
    storeUser(m.username);
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
    lastUser.current = undefined;
    storeUser(undefined);
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
    client.me().then(
      (m) => known(m, false),
      () => {
        // Not signed in, or an older server: the menu just says less.
      },
    );
  }, [client, known]);

  const signIn = useCallback(
    async (username: string, password: string) => {
      await client.login(username, password);
      // A token would win over the session cookie (the server prefers a bearer).
      store.set(undefined);
      setHasToken(false);
      known(await client.me(), true);
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
    storeUser(undefined);
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
