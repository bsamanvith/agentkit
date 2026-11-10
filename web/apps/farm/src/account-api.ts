// Sign-in and accounts (pkg/auth/accounts at /auth, apps/farm/farmauth at
// /api): a lazy chunk with the sign-in and account pages, so the first load
// does not grow.
import { session, sessionHeaders, signedOut } from "./session";

export type LaneKind = "redirect" | "secret" | "code";

/** A way to sign in (GET /auth/lanes). */
export interface Lane {
  name: string;
  kind: LaneKind;
  sign_up: boolean;
}

export interface Account {
  id: string;
  name: string;
  phone?: string;
  roles?: string[];
}

export interface Identity {
  lane: string;
  subject: string;
  email?: string;
  email_verified?: boolean;
  phone?: string;
  name?: string;
  created_at: string;
  last_used_at: string;
}

export type Role = "owner" | "partner" | "family" | "worker";

export interface Holding {
  id: string;
  name: string;
}

export interface Membership {
  holding: Holding;
  role: Role;
}

/** GET /api/me: the account, its farms and the one the app acts for. */
export interface Me {
  account: Account;
  farms: Membership[];
  holding: Holding;
  role: Role;
  partner: boolean;
  admin: boolean;
  csrf: string;
}

export interface MemberView {
  holding: string;
  account: string;
  role: Role;
  name: string;
  added_at: string;
}

export interface Assisted {
  account: Account;
  holding: Holding;
  code: string;
  expires: string;
}

export class AuthError extends Error {
  readonly status: number;
  readonly code: string;
  constructor(status: number, message: string, code: string) {
    super(message);
    this.status = status;
    this.code = code;
  }
}

async function req<T>(method: string, path: string, body?: unknown): Promise<T> {
  const resp = await fetch(path, {
    method,
    headers: { ...sessionHeaders(method), ...(body === undefined ? {} : { "Content-Type": "application/json" }) },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (!path.startsWith("/auth/") || path === "/auth/me") signedOut(resp.status);
  if (resp.status === 204) return undefined as T;
  const data = (await resp.json().catch(() => ({}))) as { error?: string; code?: string };
  if (!resp.ok) throw new AuthError(resp.status, data.error ?? resp.statusText, data.code ?? "");
  return data as T;
}

const enc = encodeURIComponent;

export const auth = {
  /** The lanes; null when the server has no sign-in. */
  lanes: async (): Promise<Lane[] | null> => {
    const resp = await fetch("/auth/lanes");
    if (resp.status === 404) return null;
    if (!resp.ok) throw new Error(`HTTP ${resp.status}`);
    return ((await resp.json()) as { lanes: Lane[] }).lanes;
  },
  /** Starts a redirect lane: the browser goes to the provider. */
  start: async (lane: string, link = false) => {
    const { url } = await req<{ url: string }>("POST", `/auth/${enc(lane)}/start`, {
      return: window.location.pathname,
      link,
    });
    window.location.assign(url);
  },
  check: (lane: string, id: string, secret: string) => req<unknown>("POST", `/auth/${enc(lane)}/check`, { id, secret }),
  send: (lane: string, to: string) => req<{ to: string }>("POST", `/auth/${enc(lane)}/send`, { to }),
  verify: (lane: string, to: string, code: string, link = false) =>
    req<unknown>("POST", `/auth/${enc(lane)}/verify`, { to, code, link }),
  identities: async () => (await req<{ identities: Identity[] }>("GET", "/auth/me")).identities,
  setPassword: (username: string, password: string) => req<unknown>("POST", "/auth/password", { username, password }),
  unlink: (lane: string, subject: string) => req<undefined>("POST", "/auth/unlink", { lane, subject }),
  rename: (name: string) => req<unknown>("PATCH", "/auth/me", { name }),
  signOut: () => req<undefined>("POST", "/auth/signout"),
  signOutAll: () => req<undefined>("POST", "/auth/signout-all"),
};

export const farmsApi = {
  me: async (): Promise<Me> => {
    const me = await req<Me>("GET", "/api/me");
    session.csrf = me.csrf;
    return me;
  },
  members: async (holding: string) =>
    (await req<{ members: MemberView[] }>("GET", `/api/holdings/${enc(holding)}/members`)).members,
  rename: (holding: string, name: string) => req<Holding>("PATCH", `/api/holdings/${enc(holding)}`, { name }),
  invite: (holding: string, role: Role) =>
    req<{ code: string; expires: string }>("POST", `/api/holdings/${enc(holding)}/invites`, { role }),
  setRole: (holding: string, account: string, role: Role) =>
    req<unknown>("PUT", `/api/holdings/${enc(holding)}/members/${enc(account)}`, { role }),
  remove: (holding: string, account: string) =>
    req<undefined>("DELETE", `/api/holdings/${enc(holding)}/members/${enc(account)}`),
  signInCode: (holding: string, account: string) =>
    req<{ code: string; expires: string }>("POST", `/api/holdings/${enc(holding)}/members/${enc(account)}/code`),
  join: (code: string) => req<Membership>("POST", "/api/join", { code }),
  assist: (name: string, phone: string, farm: string) => req<Assisted>("POST", "/api/assist", { name, phone, farm }),
};

/** What the person sees for a lane. */
export function laneLabel(name: string): string {
  switch (name) {
    case "google":
      return "Google";
    case "password":
      return "Username and password";
    case "invite":
      return "Code from your field partner";
    case "phone":
      return "Phone number";
    case "email":
      return "Email";
  }
  return name;
}

/** Why a redirect sign-in came back failed (?signin=failed&reason=...). */
export function failureText(reason: string): string {
  switch (reason) {
    case "denied":
      return "Sign-in was cancelled.";
    case "expired":
      return "That sign-in took too long or was already used. Please try again.";
    case "no_account":
      return "No account signs in this way yet. Sign in another way and link it, or ask your field partner.";
    case "linked_elsewhere":
      return "That Google account already belongs to another account here.";
    case "session":
      return "You were signed out while linking. Sign in and try again.";
  }
  return "Sign-in did not work. Please try again.";
}

export const roleLabels: Record<Role, string> = {
  owner: "Owner",
  partner: "Field partner",
  family: "Family",
  worker: "Worker (can only look)",
};
