import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Agent } from "@agentkit/agent-ui/api";
import { createMemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { tokenStorageKey } from "./api";
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

/** A fake /v1 that wants the bearer token "good", recording each request. */
function fakeServer() {
  const seen: Request[] = [];
  const fetch = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const req = new Request(new URL(String(input), "http://console.test"), init);
    seen.push(req);
    if (req.headers.get("Authorization") !== "Bearer good") {
      const body = { v: 1, error: { code: "unauthenticated", message: "token required", retryable: false } };
      return Response.json(body, { status: 401 });
    }
    if (new URL(req.url).pathname === "/v1/agents") return Response.json(agents);
    return Response.json({ v: 1, error: { code: "not_found", message: "no", retryable: false } }, { status: 404 });
  });
  vi.stubGlobal("fetch", fetch);
  return seen;
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
    expect(await screen.findByText("researcher")).toBeInTheDocument();
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

describe("the shell", () => {
  it("names the page and marks its section", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => Response.json(agents)),
    );
    renderAt("/agents");
    expect(await screen.findByRole("heading", { level: 1, name: "Agents" })).toBeInTheDocument();
    await waitFor(() => expect(document.title).toBe("Agents · agentkit"));
    expect(screen.getByRole("link", { name: "Agents" })).toHaveAttribute("aria-current", "page");
    expect(screen.getByRole("link", { name: "Skip to content" })).toHaveAttribute("href", "#content");
  });

  it("shows a pinned theme from localStorage on <html>", async () => {
    localStorage.setItem("agentkit.theme", "dark");
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => Response.json(agents)),
    );
    renderAt("/");
    await waitFor(() => expect(document.documentElement).toHaveAttribute("data-theme", "dark"));
  });
});
