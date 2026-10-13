import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Agent } from "@agentkit/agent-ui/api";
import { createMemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { tokenStorageKey } from "./api";
import { listStorageKey } from "./layout";
import { App } from "./app";
import { readiness } from "./pages/readiness";
import { routes } from "./routes";
import { sectionOf } from "./sections";

const agents = {
  v: 1,
  agents: [
    { name: "researcher", description: "Finds things", default: true, toolsets: [{ name: "web", status: "ready" }] },
  ],
};

/** A fake /v1 that wants the bearer token server.token ("good"), recording each request. */
function fakeServer(server = { token: "good" }) {
  const seen: Request[] = [];
  const fetch = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const req = new Request(new URL(String(input), "http://console.test"), init);
    seen.push(req);
    if (req.headers.get("Authorization") !== `Bearer ${server.token}`) {
      const body = { v: 1, error: { code: "unauthenticated", message: "token required", retryable: false } };
      return Response.json(body, { status: 401 });
    }
    if (new URL(req.url).pathname === "/v1/agents") return Response.json(agents);
    if (new URL(req.url).pathname === "/v1/toolsets") return Response.json({ v: 1, toolsets: [] });
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

  it("can be forgotten from the account menu", async () => {
    sessionStorage.setItem(tokenStorageKey, "good");
    const seen = fakeServer();
    renderAt("/agents");
    await researcher();

    await userEvent.click(screen.getByRole("button", { name: "Account and theme" }));
    await userEvent.click(await screen.findByRole("menuitem", { name: "Forget token" }));
    expect(sessionStorage.getItem(tokenStorageKey)).toBeNull();
    expect(await screen.findByLabelText("API token")).toBeInTheDocument();
    // Forgetting isn't a failure: the card shows no error.
    expect(screen.getByRole("dialog").textContent).not.toMatch(/rejected|accepted|Enter a token/);
    const before = seen.length;
    await new Promise((r) => setTimeout(r, 50));
    expect(seen).toHaveLength(before); // and makes no request without one
  });

  it("is not offered to forget when there is none", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => Response.json(agents)),
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
      vi.fn(async () => Response.json(agents)),
    );
    renderAt("/");
    expect(await screen.findByText("Finds things")).toBeInTheDocument();
    expect(screen.queryByLabelText("API token")).not.toBeInTheDocument();
  });
});

describe("a page", () => {
  it("that failed to load says why, and Retry loads it again", async () => {
    let calls = 0;
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => {
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
      vi.fn(async () => Response.json(agents)),
    );
    renderAt("/nowhere");
    expect(await screen.findByRole("heading", { level: 1, name: "Not found" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Back to chats" })).toHaveAttribute("href", "/");
  });
});

describe("the shell", () => {
  beforeEach(() => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL) =>
        String(input).endsWith("/v1/toolsets")
          ? Response.json({ v: 1, toolsets: [{ name: "web", type: "mcp", status: "ready", tool_count: 3 }] })
          : Response.json(agents),
      ),
    );
  });

  it("shows the first agent beside the list, and marks both", async () => {
    renderAt("/agents");
    expect(await researcher()).toBeInTheDocument();
    await waitFor(() => expect(document.title).toBe("researcher · agentkit"));
    const sections = screen.getByRole("navigation", { name: "Sections" });
    expect(within(sections).getByRole("link", { name: "Agents" })).toHaveAttribute("aria-current", "page");
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
      vi.fn(async () => Response.json(agents)),
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

  it("closes the drawer on a pick, and lists agents as a page with Back", async () => {
    renderAt("/new");
    await screen.findByRole("heading", { level: 1, name: "New chat" });
    await userEvent.click(screen.getByRole("button", { name: "Menu" }));
    await userEvent.click(screen.getByRole("link", { name: "Agents" }));

    expect(await screen.findByRole("heading", { level: 1, name: "Agents" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Menu" })).toHaveAttribute("aria-expanded", "false");
    const list = screen.getByRole("navigation", { name: "Agents" });
    await userEvent.click(within(list).getByRole("link", { name: /researcher/ }));

    expect(await researcher()).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Back to agents" })).toHaveAttribute("href", "/agents");
  });
});
