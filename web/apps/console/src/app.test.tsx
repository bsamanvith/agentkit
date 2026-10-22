import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Agent } from "@agentkit/agent-ui/api";
import { lazy } from "react";
import { createMemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, onTestFinished, vi } from "vitest";
import { tokenStorageKey, userStorageKey } from "./api";
import { listStorageKey } from "./layout";
import { App } from "./app";
import { Page } from "./pages/page";
import { toolsetRow } from "./lists";
import { readiness } from "./pages/readiness";
import { inShell, routes } from "./routes";
import { sectionOf } from "./sections";

const agents = {
  v: 1,
  agents: [
    { name: "researcher", description: "Finds things", default: true, toolsets: [{ name: "web", status: "ready" }] },
  ],
};

/**
 * A fake /v1 that wants the bearer token server.token ("good"), recording
 * each request but those to /v1/auth/ (the sign-in card's and the account
 * menu's), which it answers as a server with keys and no users.
 */
/** GET /v1/auth/me of a server without credentials (anyone may do anything), else undefined. */
const openMe = (input: RequestInfo | URL) =>
  new URL(String(input), "http://console.test").pathname === "/v1/auth/me"
    ? Response.json({ v: 1, tenant: "default", roles: [], scopes: [] })
    : undefined;

function fakeServer(server = { token: "good" }) {
  const seen: Request[] = [];
  const fetch = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const req = new Request(new URL(String(input), "http://console.test"), init);
    const path = new URL(req.url).pathname;
    const bearer = req.headers.get("Authorization") === `Bearer ${server.token}`;
    if (path === "/v1/auth/config") return Response.json({ v: 1, methods: ["token"] });
    if (path === "/v1/auth/me" && bearer) {
      return Response.json({ v: 1, tenant: "default", roles: [], scopes: ["*"], credential: "api_key" });
    }
    // The shell's watch of background runs (Tasks): not what these tests count.
    const watch = path === "/v1/runs" && new URL(req.url).searchParams.get("background") === "true";
    if (watch && bearer) return Response.json({ v: 1, runs: [] });
    // Who is signed in (the provider asks once per sign-in): not counted either.
    if (!watch && !path.startsWith("/v1/auth/")) seen.push(req);
    if (!bearer) {
      const body = { v: 1, error: { code: "unauthenticated", message: "token required", retryable: false } };
      return Response.json(body, { status: 401 });
    }
    if (path === "/v1/agents") return Response.json(agents);
    if (path === "/v1/toolsets") return Response.json({ v: 1, toolsets: [] });
    return Response.json({ v: 1, error: { code: "not_found", message: "no", retryable: false } }, { status: 404 });
  });
  vi.stubGlobal("fetch", fetch);
  return seen;
}

/** /agents shows its first agent on a laptop or desktop: the h1 is its name. */
const researcher = () => screen.findByRole("heading", { level: 1, name: "researcher" });

/** Answer the phone media query (< 768 px) as matched. */
function asPhone() {
  vi.stubGlobal("matchMedia", (query: string) => ({
    matches: query.includes("max-width: 767px"),
    media: query,
    addEventListener: () => {},
    removeEventListener: () => {},
  }));
}

function renderAt(path: string) {
  return render(<App router={createMemoryRouter(routes, { initialEntries: [path] })} />);
}

beforeEach(() => {
  sessionStorage.clear();
  localStorage.clear();
  document.documentElement.removeAttribute("data-theme");
});
afterEach(() => vi.unstubAllGlobals());

describe("sectionOf", () => {
  it.each([
    ["/", "Chats"],
    ["/new", "Chats"],
    ["/c/conv_1", "Chats"],
    ["/agents", "Agents"],
    ["/agents/researcher", "Agents"],
    ["/toolsets/web", "Toolsets"],
    ["/connections", "Connections"],
    ["/agentsx", "Chats"],
    ["/nowhere", "Chats"],
  ])("%s is %s", (path, label) => {
    expect(sectionOf(path).label).toBe(label);
  });
});

describe("readiness", () => {
  const agent = (statuses: string[]): Agent => ({
    name: "a",
    description: "",
    default: true,
    model: { id: "m" },
    toolsets: statuses.map((status, i) => ({ name: `t${i}`, status, tools: [] })),
    skills: [],
    memory: false,
  });
  it.each([
    [[], "ok", "Ready"],
    [["ready"], "ok", "Its toolset is ready"],
    [["ready", "ready"], "ok", "All 2 toolsets ready"],
    [["ready", "failed", "pending"], "warn", "2 of 3 toolsets not ready"],
  ])("%j", (statuses, tone, text) => {
    expect(readiness(agent(statuses))).toEqual({ tone, text });
  });
});

describe("toolsetRow", () => {
  it.each([
    [{ status: "ready", tool_count: 1 }, "ok", "Ready", "1 tool"],
    [{ status: "skipped", reason: "no key", tool_count: 0 }, "warn", "Skipped", "Skipped · no key"],
    [{ status: "unavailable", reason: "down", tool_count: 0 }, "bad", "Unavailable", "Unavailable · down"],
  ])("%j", (t, tone, word, sub) => {
    expect(toolsetRow({ name: "x", type: "mcp", ...t })).toEqual({ name: "x", sub, tone, word });
  });
});

describe("the token", () => {
  it("is asked for after a 401, once, and then sent as a bearer token", async () => {
    const seen = fakeServer();
    renderAt("/agents");

    const input = await screen.findByLabelText("API token");
    expect(screen.getByText(/kept for this tab only/)).toBeInTheDocument();
    expect(seen).toHaveLength(1); // the prompt waits: no request loop
    expect(seen[0]?.headers.has("Authorization")).toBe(false);

    await userEvent.click(screen.getByRole("button", { name: "Continue" }));
    expect(screen.getByText("Enter a token")).toBeInTheDocument();

    await userEvent.type(input, "good");
    await userEvent.click(screen.getByRole("button", { name: "Continue" }));
    expect(await researcher()).toBeInTheDocument();
    expect(screen.queryByLabelText("API token")).not.toBeInTheDocument();
    expect(seen.at(-1)?.headers.get("Authorization")).toBe("Bearer good");
    expect(sessionStorage.getItem(tokenStorageKey)).toBe("good");
    expect(localStorage.getItem(tokenStorageKey)).toBeNull();
  });

  it("is read from sessionStorage, and a rejected one says so", async () => {
    sessionStorage.setItem(tokenStorageKey, "stale");
    const seen = fakeServer();
    renderAt("/agents");

    expect(await screen.findByText("Your token was rejected or has expired.")).toBeInTheDocument();
    expect(seen[0]?.headers.get("Authorization")).toBe("Bearer stale");
  });

  it("that was just entered and refused says so, not that it expired", async () => {
    fakeServer();
    renderAt("/agents");
    const input = await screen.findByLabelText("API token");
    await userEvent.click(screen.getByRole("button", { name: "Continue" }));
    expect(screen.getByText("Enter a token")).toBeInTheDocument();

    await userEvent.type(input, "bad");
    await userEvent.click(screen.getByRole("button", { name: "Continue" }));
    expect(await screen.findByText("That token wasn't accepted.")).toBeInTheDocument();
    expect(screen.queryByText("Enter a token")).not.toBeInTheDocument();
    expect(screen.queryByText(/rejected or has expired/)).not.toBeInTheDocument();
    // Not kept: after a reload it would be taken for one that expired.
    expect(sessionStorage.getItem(tokenStorageKey)).toBeNull();
  });

  it("that worked and was revoked later says it was rejected or expired", async () => {
    const server = { token: "good" };
    fakeServer(server);
    renderAt("/agents");
    await userEvent.type(await screen.findByLabelText("API token"), "good");
    await userEvent.click(screen.getByRole("button", { name: "Continue" }));
    await researcher();

    server.token = "rotated";
    await userEvent.click(screen.getByRole("link", { name: "Toolsets" }));
    expect(await screen.findByText("Your token was rejected or has expired.")).toBeInTheDocument();
    expect(screen.queryByText("That token wasn't accepted.")).not.toBeInTheDocument();
  });

  it("that can't be sent (a pasted zero-width space) is refused with no request", async () => {
    const seen = fakeServer();
    renderAt("/agents");
    await userEvent.type(await screen.findByLabelText("API token"), "good\u200b");
    await userEvent.click(screen.getByRole("button", { name: "Continue" }));
    expect(screen.getByText("That token wasn't accepted.")).toBeInTheDocument();
    expect(seen).toHaveLength(1);
    expect(sessionStorage.getItem(tokenStorageKey)).toBeNull();
  });

  it("keeps the app inert behind the prompt", async () => {
    fakeServer();
    renderAt("/agents");
    const input = await screen.findByLabelText("API token");
    const nav = screen.getByRole("navigation", { name: "Sections", hidden: true });
    expect(nav.closest("[inert]")).not.toBeNull();

    await userEvent.type(input, "good");
    await userEvent.click(screen.getByRole("button", { name: "Continue" }));
    await researcher();
    expect(nav.closest("[inert]")).toBeNull();
  });

  it("keeps the shell's shortcuts off while it is asked for", async () => {
    fakeServer();
    renderAt("/agents/researcher");
    await screen.findByLabelText("API token");
    await userEvent.keyboard("{Control>}{Shift>}S{/Shift}{/Control}");
    expect(localStorage.getItem(listStorageKey)).toBeNull();

    await userEvent.type(screen.getByLabelText("API token"), "good");
    await userEvent.click(screen.getByRole("button", { name: "Continue" }));
    await researcher();
    expect(screen.getByRole("button", { name: "List" })).toHaveAttribute("aria-expanded", "true");
  });

  it("can be forgotten from the account menu", async () => {
    sessionStorage.setItem(tokenStorageKey, "good");
    const seen = fakeServer();
    renderAt("/agents");
    await researcher();

    await userEvent.click(screen.getByRole("button", { name: "Account and theme" }));
    const before = seen.length;
    await userEvent.click(await screen.findByRole("menuitem", { name: "Forget token" }));
    expect(sessionStorage.getItem(tokenStorageKey)).toBeNull();
    expect(await screen.findByLabelText("API token")).toBeInTheDocument();
    // Forgetting isn't a failure: the card shows no error.
    expect(screen.getByRole("dialog").textContent).not.toMatch(/rejected|accepted|Enter a token/);
    // And it makes no request without one: the reload the forget starts ran
    // its effect in the click (fetch is called synchronously), and waited.
    expect(seen).toHaveLength(before);
  });

  it("is not offered to forget when there is none", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL) => openMe(input) ?? Response.json(agents)),
    );
    renderAt("/agents");
    await researcher();
    await userEvent.click(screen.getByRole("button", { name: "Account and theme" }));
    expect(await screen.findByRole("menu")).toBeInTheDocument();
    expect(screen.queryByRole("menuitem", { name: "Forget token" })).not.toBeInTheDocument();
  });

  it("is not asked for when the server needs none", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL) => openMe(input) ?? Response.json(agents)),
    );
    renderAt("/");
    expect(await screen.findByText("Finds things")).toBeInTheDocument();
    expect(screen.queryByLabelText("API token")).not.toBeInTheDocument();
  });
});

/**
 * A fake /v1 with local users (ann:secret-pw, bob:other-pw) and keys: a
 * session cookie is modelled by server.session (who is signed in), and a
 * request other than GET with it must carry its CSRF token.
 */
function sessionServer(opts: { methods?: string[]; login?: () => Response | undefined; logout?: number } = {}) {
  const server = { session: undefined as string | undefined, csrf: "csrf-1", requests: [] as Request[] };
  const users: Record<string, string> = { ann: "secret-pw", bob: "other-pw" };
  const error = (status: number, code: string, headers?: HeadersInit) =>
    Response.json({ v: 1, error: { code, message: code, retryable: false } }, { status, headers });
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const req = new Request(new URL(String(input), "http://console.test"), init);
      server.requests.push(req);
      const path = new URL(req.url).pathname;
      if (path === "/v1/auth/config") return Response.json({ v: 1, methods: opts.methods ?? ["password", "token"] });
      if (path === "/v1/auth/login") {
        const forced = opts.login?.();
        if (forced) return forced;
        const { username, password } = (await req.json()) as { username: string; password: string };
        if (users[username] !== password) return error(401, "unauthenticated");
        server.session = username;
        return new Response(null, { status: 204 });
      }
      const bearer = req.headers.get("Authorization");
      if (bearer === "Bearer good") {
        if (path === "/v1/auth/me")
          return Response.json({ v: 1, tenant: "default", roles: [], scopes: ["*"], credential: "api_key" });
      } else if (bearer !== null || server.session === undefined) {
        // A bearer token wins over the cookie, as on the server.
        return error(401, "unauthenticated");
      } else if (req.method !== "GET" && req.headers.get("X-Agentkit-CSRF") !== server.csrf) {
        return error(403, "csrf_failed");
      }
      if (path === "/v1/auth/me") {
        const roles = server.session === "ann" ? ["admin"] : ["user"];
        return Response.json({
          v: 1,
          username: server.session,
          tenant: "acme",
          roles,
          scopes: [],
          credential: "session",
          csrf: server.csrf,
        });
      }
      if (path === "/v1/auth/logout") {
        if (opts.logout) return error(opts.logout, "internal");
        server.session = undefined;
        return new Response(null, { status: 204 });
      }
      if (path === "/v1/agents") return Response.json(agents);
      if (path === "/v1/toolsets") return Response.json({ v: 1, toolsets: [] });
      if (path === "/v1/conversations") return Response.json({ v: 1, conversations: [] });
      return error(404, "not_found");
    }),
  );
  return server;
}

async function signInAs(username: string, password: string) {
  await userEvent.type(await screen.findByLabelText("Username"), username);
  await userEvent.type(screen.getByLabelText("Password"), password);
  await userEvent.click(screen.getByRole("button", { name: "Sign in" }));
}

describe("signing in with a password", () => {
  it("shows admin navigation only once who is signed in says admin", async () => {
    sessionServer();
    renderAt("/tasks");
    const sections = await screen.findByRole("navigation", { name: "Sections", hidden: true });
    // Not known yet: no admin navigation.
    expect(within(sections).queryByRole("link", { name: "Agents", hidden: true })).toBeNull();
    await signInAs("bob", "other-pw");
    const tabs = await screen.findByRole("navigation", { name: "Tabs" });
    expect(within(tabs).getByRole("link", { name: "Threads" })).toBeInTheDocument();
    expect(within(tabs).queryByRole("link", { name: "Triggers" })).toBeNull();
    expect(within(sections).queryByRole("link", { name: "Agents" })).toBeNull();
    expect(within(sections).queryByRole("link", { name: "Toolsets" })).toBeNull();
    expect(within(sections).getByRole("link", { name: "Tasks" })).toBeInTheDocument();
  });

  it("shows a user's phone drawer no admin sections", async () => {
    asPhone();
    sessionServer();
    renderAt("/tasks");
    await signInAs("bob", "other-pw");
    await screen.findByRole("navigation", { name: "Tabs" });
    const drawer = screen.getByRole("dialog", { name: "Menu", hidden: true });
    const sections = within(drawer).getByRole("navigation", { name: "Sections", hidden: true });
    expect(within(sections).getByRole("link", { name: /Tasks/, hidden: true })).toBeInTheDocument();
    expect(within(sections).queryByRole("link", { name: /Agents|Toolsets/, hidden: true })).toBeNull();
  });

  it("shows an admin the admin navigation", async () => {
    sessionServer();
    renderAt("/tasks");
    await signInAs("ann", "secret-pw");
    const sections = screen.getByRole("navigation", { name: "Sections" });
    expect(await within(sections).findByRole("link", { name: "Agents" })).toBeInTheDocument();
    expect(within(sections).getByRole("link", { name: "Toolsets" })).toBeInTheDocument();
    const tabs = await screen.findByRole("navigation", { name: "Tabs" });
    expect(within(tabs).getByRole("link", { name: "Triggers" })).toBeInTheDocument();
  });

  it("asks for a username and password, then uses the session", async () => {
    const server = sessionServer();
    renderAt("/agents");
    await userEvent.click(await screen.findByRole("button", { name: "Sign in" }));
    expect(screen.getByText("Enter your username")).toBeInTheDocument();
    expect(screen.getByText("Enter your password")).toBeInTheDocument();
    expect(server.requests.some((r) => r.url.endsWith("/v1/auth/login"))).toBe(false);

    await signInAs("ann", "secret-pw");
    expect(await researcher()).toBeInTheDocument();
    expect(screen.queryByLabelText("Password")).not.toBeInTheDocument();
    // No token: the cookie signs the requests, and nothing is kept in storage.
    expect(server.requests.at(-1)?.headers.has("Authorization")).toBe(false);
    expect(sessionStorage.getItem(tokenStorageKey)).toBeNull();
  });

  it("says a wrong username or password once, and clears and focuses the password", async () => {
    sessionServer();
    renderAt("/agents");
    await signInAs("ann", "nope-nope");
    expect(await screen.findByText("Wrong username or password.")).toBeInTheDocument();
    const pw = screen.getByLabelText("Password");
    expect(pw).toHaveValue("");
    await waitFor(() => expect(pw).toHaveFocus());
    expect(pw).toHaveAttribute("aria-invalid", "true");
    expect(screen.getByLabelText("Username")).toHaveAttribute("aria-invalid", "true");
    expect(screen.getByLabelText("Username")).toHaveValue("ann");
  });

  it("counts down a throttled sign-in with the button disabled", async () => {
    sessionServer({
      login: () =>
        Response.json(
          { v: 1, error: { code: "too_many_attempts", message: "slow down", retryable: true } },
          { status: 429, headers: { "Retry-After": "2" } },
        ),
    });
    renderAt("/agents");
    await signInAs("ann", "secret-pw");
    expect(await screen.findByText("Too many attempts. Try again in 2 s.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Sign in" })).toBeDisabled();
    expect(await screen.findByText("Too many attempts. Try again in 1 s.")).toBeInTheDocument();
    await waitFor(() => expect(screen.getByRole("button", { name: "Sign in" })).toBeEnabled());
  });

  it("says it couldn't reach the server on a 5xx", async () => {
    sessionServer({ login: () => Response.json({ v: 1, error: { code: "internal", message: "x" } }, { status: 500 }) });
    renderAt("/agents");
    await signInAs("ann", "secret-pw");
    expect(await screen.findByText("Couldn't reach the server. Try again.")).toBeInTheDocument();
  });

  it("offers the token card when the server takes keys too, and only then", async () => {
    sessionServer();
    const view = renderAt("/agents");
    await userEvent.click(await screen.findByRole("button", { name: "Use an API token instead" }));
    await userEvent.type(screen.getByLabelText("API token"), "good");
    await userEvent.click(screen.getByRole("button", { name: "Continue" }));
    expect(await researcher()).toBeInTheDocument();
    view.unmount();
    sessionStorage.clear(); // the token just used

    sessionServer({ methods: ["password"] });
    renderAt("/agents");
    await screen.findByLabelText("Password");
    expect(screen.queryByRole("button", { name: "Use an API token instead" })).not.toBeInTheDocument();
  });

  it("drops a token that stopped working, so the session's cookie is used", async () => {
    sessionStorage.setItem(tokenStorageKey, "revoked");
    const server = sessionServer();
    renderAt("/agents");
    expect(await screen.findByText("Your session ended. Sign in again; your draft is kept.")).toBeInTheDocument();
    await signInAs("ann", "secret-pw");
    expect(await researcher()).toBeInTheDocument();
    expect(server.requests.at(-1)?.headers.has("Authorization")).toBe(false);
    expect(sessionStorage.getItem(tokenStorageKey)).toBeNull();
  });

  it("shows who is signed in, and signs out, sending the CSRF token", async () => {
    const server = sessionServer();
    renderAt("/agents");
    await signInAs("ann", "secret-pw");
    await researcher();

    await userEvent.click(screen.getByRole("button", { name: "Account and theme" }));
    expect(await screen.findByText("ann · admin · acme")).toBeInTheDocument();
    expect(screen.queryByRole("menuitem", { name: "Forget token" })).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("menuitem", { name: "Sign out" }));
    expect(await screen.findByLabelText("Password")).toBeInTheDocument();
    const logout = server.requests.find((r) => r.url.endsWith("/v1/auth/logout"));
    expect(logout?.headers.get("X-Agentkit-CSRF")).toBe("csrf-1");
    expect(server.session).toBeUndefined();
    expect(screen.queryByText(/Signed out on this device/)).not.toBeInTheDocument();
  });

  it("still signs out here when the server can't end the session, and says so", async () => {
    sessionServer({ logout: 500 });
    renderAt("/agents");
    await signInAs("ann", "secret-pw");
    await researcher();
    await userEvent.click(screen.getByRole("button", { name: "Account and theme" }));
    await userEvent.click(await screen.findByRole("menuitem", { name: "Sign out" }));
    expect(
      await screen.findByText("Signed out on this device. The server session ends when it expires."),
    ).toBeInTheDocument();
    expect(screen.getByLabelText("Password")).toBeInTheDocument();
  });

  it("says the session ended when a later request gets a 401", async () => {
    const server = sessionServer();
    renderAt("/agents");
    await signInAs("ann", "secret-pw");
    await researcher();

    server.session = undefined; // expired on the server
    await userEvent.click(screen.getByRole("link", { name: "Toolsets" }));
    expect(await screen.findByText("Your session ended. Sign in again; your draft is kept.")).toBeInTheDocument();
    await signInAs("ann", "secret-pw");
    await waitFor(() => expect(screen.queryByLabelText("Password")).not.toBeInTheDocument());
  });

  it("keeps the draft for the same user after the session ends, and not for another", async () => {
    const server = sessionServer();
    renderAt("/new");
    await signInAs("ann", "secret-pw");
    const box = () => screen.getByRole("textbox", { name: "Message" });
    await waitFor(() => expect(box()).toBeEnabled());
    await userEvent.type(box(), "half a thought");

    server.session = undefined;
    await userEvent.click(screen.getByRole("link", { name: "Agents" }));
    await signInAs("ann", "secret-pw");
    await userEvent.click(await screen.findByRole("link", { name: "Chats" }));
    await userEvent.click((await screen.findAllByRole("link", { name: "New chat" }))[0]!);
    expect(await screen.findByRole("textbox", { name: "Message" })).toHaveValue("half a thought");

    server.session = undefined;
    await userEvent.click(screen.getByRole("link", { name: "Agents" }));
    await signInAs("bob", "other-pw");
    await userEvent.click(await screen.findByRole("link", { name: "Chats" }));
    await userEvent.click((await screen.findAllByRole("link", { name: "New chat" }))[0]!);
    expect(await screen.findByRole("textbox", { name: "Message" })).toHaveValue("");
  });

  it("knows whose the drafts are after a reload: kept for that user, cleared for another or no one known", async () => {
    const box = () => screen.getByRole("textbox", { name: "Message" });
    const server = sessionServer();
    let view = renderAt("/new");
    await signInAs("ann", "secret-pw");
    await waitFor(() => expect(box()).toBeEnabled());
    await userEvent.type(box(), "ann's thought");
    view.unmount(); // a reload, and the session ends meanwhile
    server.session = undefined;

    view = renderAt("/new");
    await signInAs("ann", "secret-pw");
    expect(await screen.findByRole("textbox", { name: "Message" })).toHaveValue("ann's thought");
    view.unmount();
    server.session = undefined;

    view = renderAt("/new");
    await signInAs("bob", "other-pw");
    await waitFor(() => expect(box()).toBeEnabled());
    expect(box()).toHaveValue("");
    expect(sessionStorage.getItem(userStorageKey)).toBe("bob");
    view.unmount();
    server.session = undefined;

    // A draft of no known user (from before a reload) is not handed to whoever signs in.
    sessionStorage.removeItem(userStorageKey);
    sessionStorage.setItem("agentkit.draft.new", "whose?");
    renderAt("/new");
    await signInAs("ann", "secret-pw");
    await waitFor(() => expect(box()).toBeEnabled());
    expect(box()).toHaveValue("");
  });
});

describe("a page", () => {
  it("that failed to load says why, and Retry loads it again", async () => {
    let calls = 0;
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL) => {
        if (String(input).includes("background=true")) return Response.json({ v: 1, runs: [] });
        const me = openMe(input);
        if (me) return me;
        // The agent's own page (its system prompt): not the list either.
        if (String(input).includes("/v1/agents/")) return Response.json({ v: 1, ...agents.agents[0] });
        calls++;
        if (calls === 1) {
          const body = { v: 1, error: { code: "internal", message: "the store is down", retryable: true } };
          return Response.json(body, { status: 503, headers: { "Retry-After": "5" } });
        }
        return Response.json(agents);
      }),
    );
    renderAt("/agents");
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("Couldn't load agents.");
    expect(alert).toHaveTextContent("the store is down (503 internal). Try again in 5 s.");

    // The list column says it too, briefly, but only the page is an alert.
    expect(screen.getAllByRole("alert")).toHaveLength(1);
    await userEvent.click(within(alert).getByRole("button", { name: "Retry" }));
    expect(await researcher()).toBeInTheDocument();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(calls).toBe(2);
  });

  it("that can't reach the server says so", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => {
        throw new TypeError("Failed to fetch");
      }),
    );
    renderAt("/agents");
    expect(await screen.findByRole("alert")).toHaveTextContent("Couldn't reach the server.");
  });

  it("with a list that isn't a list fails, not shows none", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => Response.json({ v: 1, toolsets: {} })),
    );
    renderAt("/toolsets");
    expect(await screen.findByRole("alert")).toHaveTextContent("Couldn't load toolsets.");
    expect(screen.queryByText("No toolsets are configured.")).not.toBeInTheDocument();
  });

  it("with a null list (Go's nil slice) shows none", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => Response.json({ v: 1, toolsets: null })),
    );
    renderAt("/toolsets");
    expect(await screen.findByText("No toolsets are configured.")).toBeInTheDocument();
  });

  it("that failed for a reason other than the network says that reason", async () => {
    // A token saved by hand with a character a header can't carry: the
    // browser throws before sending, so "Couldn't reach the server" would lie.
    sessionStorage.setItem(tokenStorageKey, "good\u200b");
    fakeServer();
    renderAt("/agents");
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("Couldn't load agents.");
    expect(alert).not.toHaveTextContent("Couldn't reach the server.");
  });

  it("that doesn't exist is Not found, with a way back", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL) => openMe(input) ?? Response.json(agents)),
    );
    renderAt("/nowhere");
    expect(await screen.findByRole("heading", { level: 1, name: "Not found" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Back to chats" })).toHaveAttribute("href", "/");
  });
});

describe("a page that throws", () => {
  function Boom(): never {
    throw new Error("the page broke");
  }

  it("shows an error in the shell, which going elsewhere clears", async () => {
    const logged = vi.spyOn(console, "error").mockImplementation(() => {}); // React and the router log it
    onTestFinished(() => logged.mockRestore());
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL) => openMe(input) ?? Response.json(agents)),
    );
    const pages = routes[0]?.children?.[0]?.children ?? [];
    render(
      <App
        router={createMemoryRouter(inShell([{ path: "/boom", element: <Boom /> }, ...pages]), {
          initialEntries: ["/boom"],
        })}
      />,
    );

    const heading = await screen.findByRole("heading", { level: 1, name: "This page couldn't be shown" });
    expect(screen.getByText("the page broke")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Reload" })).toBeInTheDocument();
    await waitFor(() => expect(heading).toHaveFocus());
    expect(document.title).toBe("This page couldn't be shown · agentkit");

    const sections = screen.getByRole("navigation", { name: "Sections" });
    await userEvent.click(await within(sections).findByRole("link", { name: "Agents" }));
    expect(await researcher()).toBeInTheDocument();
    expect(screen.queryByText("the page broke")).not.toBeInTheDocument();
  });

  it("that arrives while the account menu is open leaves the menu open", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL) => openMe(input) ?? Response.json(agents)),
    );
    let arrive: () => void = () => {};
    const Late = lazy(
      () =>
        new Promise<{ default: () => React.JSX.Element }>((resolve) => {
          arrive = () => resolve({ default: () => <Page title="Late">here</Page> });
        }),
    );
    render(
      <App
        router={createMemoryRouter(inShell([{ path: "/late", element: <Late /> }]), { initialEntries: ["/late"] })}
      />,
    );
    await userEvent.click(await screen.findByRole("button", { name: "Account and theme" }));
    const menu = await screen.findByRole("menu");
    await act(async () => arrive());
    expect(await screen.findByRole("heading", { level: 1, name: "Late" })).toBeInTheDocument();
    expect(menu).toBeInTheDocument();
    expect(screen.getByRole("menu")).toBe(menu);
  });

  it("whose code is gone (a tab older than the server) says to reload", async () => {
    const logged = vi.spyOn(console, "error").mockImplementation(() => {});
    onTestFinished(() => logged.mockRestore());
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL) => openMe(input) ?? Response.json(agents)),
    );
    const Gone = lazy(() => Promise.reject(new TypeError("Failed to fetch dynamically imported module: /assets/x.js")));
    render(
      <App
        router={createMemoryRouter(inShell([{ path: "/gone", element: <Gone /> }]), { initialEntries: ["/gone"] })}
      />,
    );
    expect(await screen.findByRole("heading", { level: 1, name: "The console was updated" })).toBeInTheDocument();
    expect(screen.getByText("Reload to get the new version.")).toBeInTheDocument();
    expect(screen.queryByText(/Failed to fetch/)).not.toBeInTheDocument();
  });
});

describe("the shell", () => {
  beforeEach(() => {
    vi.stubGlobal(
      "fetch",
      vi.fn(
        async (input: RequestInfo | URL) =>
          openMe(input) ??
          (String(input).endsWith("/v1/toolsets")
            ? Response.json({ v: 1, toolsets: [{ name: "web", type: "mcp", status: "ready", tool_count: 3 }] })
            : Response.json(agents)),
      ),
    );
  });

  it("shows the first agent beside the list, and marks both", async () => {
    renderAt("/agents");
    expect(await researcher()).toBeInTheDocument();
    await waitFor(() => expect(document.title).toBe("researcher · agentkit"));
    const sections = screen.getByRole("navigation", { name: "Sections" });
    expect(await within(sections).findByRole("link", { name: "Agents" })).toHaveAttribute("aria-current", "page");
    const list = screen.getByRole("navigation", { name: "Agents" });
    expect(within(list).getByRole("link", { name: /researcher/ })).toHaveAttribute("aria-current", "page");
    expect(screen.getByRole("link", { name: "Chat with researcher" })).toHaveAttribute("href", "/new?agent=researcher");
    expect(screen.getByRole("link", { name: "Skip to content" })).toHaveAttribute("href", "#content");
    expect(screen.queryByRole("link", { name: /Back to/ })).not.toBeInTheDocument(); // phones only
  });

  it("shows a toolset's tool count in the list", async () => {
    renderAt("/toolsets/web");
    expect(await screen.findByRole("heading", { level: 1, name: "web" })).toBeInTheDocument();
    const list = screen.getByRole("navigation", { name: "Toolsets" });
    expect(within(list).getByRole("link", { name: /web/ })).toHaveTextContent("3 tools");
  });

  it("says when an agent isn't configured", async () => {
    renderAt("/agents/nobody");
    expect(await screen.findByRole("heading", { level: 1, name: "No agent named nobody" })).toBeInTheDocument();
  });

  it("hides and shows the list, by button or Mod+Shift+S, and remembers", async () => {
    renderAt("/agents/researcher");
    await researcher();
    const toggle = screen.getByRole("button", { name: "List" });
    expect(toggle).toHaveAttribute("aria-expanded", "true");

    await userEvent.click(toggle);
    expect(toggle).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByRole("navigation", { name: "Agents" })).not.toBeInTheDocument();
    expect(localStorage.getItem(listStorageKey)).toBe("hidden");

    await userEvent.keyboard("{Control>}{Shift>}S{/Shift}{/Control}");
    expect(toggle).toHaveAttribute("aria-expanded", "true");
    expect(screen.getByRole("navigation", { name: "Agents" })).toBeInTheDocument();
    expect(localStorage.getItem(listStorageKey)).toBe("shown");
  });

  it("starts with the list hidden when you hid it", async () => {
    localStorage.setItem(listStorageKey, "hidden");
    renderAt("/agents/researcher");
    await researcher();
    expect(screen.queryByRole("navigation", { name: "Agents" })).not.toBeInTheDocument();
  });

  it("starts with the list hidden below 900 px, unless you showed it", async () => {
    vi.stubGlobal("innerWidth", 800);
    renderAt("/agents/researcher");
    await researcher();
    expect(screen.queryByRole("navigation", { name: "Agents" })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "List" })).toHaveAttribute("aria-expanded", "false");
  });

  it("has no list in Connections", async () => {
    renderAt("/connections");
    expect(await screen.findByRole("heading", { level: 1, name: "Connections" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "List" })).not.toBeInTheDocument();
  });

  it("shows the Chats list beside a new chat", async () => {
    renderAt("/new");
    expect(await screen.findByRole("heading", { level: 1, name: "New chat" })).toBeInTheDocument();
    const list = screen.getByRole("navigation", { name: "Conversations" });
    expect(within(list).getByText("No conversations yet.")).toBeInTheDocument();
  });

  it("shows a pinned theme from localStorage on <html>", async () => {
    localStorage.setItem("agentkit.theme", "dark");
    renderAt("/");
    await waitFor(() => expect(document.documentElement).toHaveAttribute("data-theme", "dark"));
  });
});

describe("on a phone", () => {
  beforeEach(() => {
    asPhone();
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL) => openMe(input) ?? Response.json(agents)),
    );
  });

  it("has no rail; Menu opens the drawer, with the app behind inert", async () => {
    renderAt("/new");
    await screen.findByRole("heading", { level: 1, name: "New chat" });
    const menu = screen.getByRole("button", { name: "Menu" });
    const drawer = screen.getByRole("dialog", { name: "Menu", hidden: true });
    expect(drawer).toHaveAttribute("inert");
    expect(screen.queryByRole("button", { name: "List" })).not.toBeInTheDocument();

    await userEvent.click(menu);
    expect(menu).toHaveAttribute("aria-expanded", "true");
    expect(drawer).not.toHaveAttribute("inert");
    expect(document.getElementById("content")).toHaveAttribute("inert");
    expect(within(drawer).getByRole("navigation", { name: "Conversations" })).toBeInTheDocument();
    const sections = within(drawer).getByRole("navigation", { name: "Sections" });
    await waitFor(() => expect(within(sections).getByRole("link", { name: "Chats" })).toHaveFocus());
  });

  it("closes the drawer on Esc, back to the Menu button", async () => {
    renderAt("/new");
    await screen.findByRole("heading", { level: 1, name: "New chat" });
    const menu = screen.getByRole("button", { name: "Menu" });
    await userEvent.click(menu);
    await waitFor(() => expect(screen.getByRole("link", { name: "Chats" })).toHaveFocus());
    await userEvent.keyboard("{Escape}");
    expect(menu).toHaveAttribute("aria-expanded", "false");
    await waitFor(() => expect(menu).toHaveFocus());
    expect(document.getElementById("content")).not.toHaveAttribute("inert");
  });

  it("closes the drawer on Esc with focus nowhere in it", async () => {
    renderAt("/new");
    await screen.findByRole("heading", { level: 1, name: "New chat" });
    const menu = screen.getByRole("button", { name: "Menu" });
    await userEvent.click(menu);
    await waitFor(() => expect(screen.getByRole("link", { name: "Chats" })).toHaveFocus());
    (document.activeElement as HTMLElement).blur(); // a tap on the drawer's padding
    await userEvent.keyboard("{Escape}");
    expect(menu).toHaveAttribute("aria-expanded", "false");
    await waitFor(() => expect(menu).toHaveFocus());
  });

  it("closes the account menu on Esc, not the drawer under it", async () => {
    renderAt("/new");
    await screen.findByRole("heading", { level: 1, name: "New chat" });
    const menu = screen.getByRole("button", { name: "Menu" });
    await userEvent.click(menu);
    await waitFor(() => expect(screen.getByRole("link", { name: "Chats" })).toHaveFocus());
    await userEvent.click(screen.getByRole("button", { name: "Account and theme" }));
    await screen.findByRole("menu");
    await userEvent.keyboard("{Escape}");
    await waitFor(() => expect(screen.queryByRole("menu")).not.toBeInTheDocument());
    expect(menu).toHaveAttribute("aria-expanded", "true");
  });

  it("keeps Tab in the open drawer", async () => {
    renderAt("/new");
    await screen.findByRole("heading", { level: 1, name: "New chat" });
    await userEvent.click(screen.getByRole("button", { name: "Menu" }));
    const drawer = screen.getByRole("dialog", { name: "Menu" });
    const first = within(drawer).getByRole("link", { name: "Chats" });
    const last = within(drawer).getByRole("button", { name: "Account and theme" });
    await waitFor(() => expect(first).toHaveFocus());

    await userEvent.tab({ shift: true });
    expect(last).toHaveFocus();
    await userEvent.tab();
    expect(first).toHaveFocus();

    (document.activeElement as HTMLElement).blur(); // a tap on the drawer's padding
    await userEvent.tab();
    expect(first).toHaveFocus();
    (document.activeElement as HTMLElement).blur();
    await userEvent.tab({ shift: true });
    expect(last).toHaveFocus();

    // The account menu (outside the drawer, in a portal) keeps its own Tab.
    await userEvent.click(last);
    const item = await screen.findByRole("menuitemradio", { name: "System" });
    await waitFor(() => expect(screen.getByRole("menu")).toContainElement(document.activeElement as HTMLElement));
    await userEvent.tab();
    expect(screen.getByRole("menu")).toContainElement(document.activeElement as HTMLElement);
    expect(item).toBeInTheDocument();
  });

  it("closes the drawer on a pick, and lists agents as a page with Back", async () => {
    renderAt("/new");
    await screen.findByRole("heading", { level: 1, name: "New chat" });
    await userEvent.click(screen.getByRole("button", { name: "Menu" }));
    await userEvent.click(await screen.findByRole("link", { name: "Agents" }));

    expect(await screen.findByRole("heading", { level: 1, name: "Agents" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Menu" })).toHaveAttribute("aria-expanded", "false");
    const list = screen.getByRole("navigation", { name: "Agents" });
    await userEvent.click(within(list).getByRole("link", { name: /researcher/ }));

    expect(await researcher()).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Back to agents" })).toHaveAttribute("href", "/agents");
  });
});
