// The admin pages (T064) against a fake /v1: the agents, toolsets and
// connections pages, and what a `user` sees. The pure parts (the schema's
// field list, the connection rows, the scope match) are tested on their own
// below.

import type { Agent, CredentialInfo, Me, Toolset } from "@agentkit/agent-ui/api";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createMemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { hasScope, isAdmin } from "../access";
import { App } from "../app";
import { routes } from "../routes";
import { mayAdmin } from "../sections";
import { connectionRows, connectReturnKey, isLocalPath } from "./connections-model";
import { credentialWords } from "./credential";
import { mcpAddCommand, mcpURL, shellWord } from "./mcp";
import { fields, typeOf } from "./schema";

/** The page's markup with its attributes, and every field's current value. */
function pageMarkup(): string {
  const fields = Array.from(document.querySelectorAll("input, textarea"), (e) => (e as HTMLInputElement).value);
  return new XMLSerializer().serializeToString(document.body) + fields.join("\n");
}

const canary = "t064-unit-canary-key-77aa";

const agentsList: Agent[] = [
  {
    name: "researcher",
    description: "Searches the web.",
    default: true,
    model: { provider: "anthropic", id: "claude-opus-5-5", effort: "low" },
    toolsets: [
      { name: "demo", status: "ready", tools: ["echo"] },
      { name: "parallel", status: "skipped", tools: [] },
      { name: "mail", status: "skipped", tools: [] },
    ],
    skills: ["citations"],
    memory: false,
  },
];

const toolsets: Toolset[] = [
  {
    name: "demo",
    type: "demo",
    status: "ready",
    mcp_path: "/mcp/demo",
    tool_count: 1,
    credential: { status: "not_needed" },
  },
  {
    name: "parallel",
    type: "parallel",
    status: "skipped",
    reason: "parallel: the API key is missing: set the environment variable PARALLEL_API_KEY",
    mcp_path: "/mcp/parallel",
    tool_count: 0,
    credential: { status: "missing", env: "PARALLEL_API_KEY" },
  },
  {
    name: "mail",
    type: "a2a",
    status: "skipped",
    reason: "no card",
    tool_count: 0,
    credential: { status: "not_connected", slot: "mail", scope: "user" },
  },
];

const echo = {
  name: "echo",
  description: "Echoes text back.",
  kind: "read",
  risk: "low",
  classified: true,
  input_schema: {
    type: "object",
    properties: {
      text: { type: "string", description: "text to echo" },
      times: { type: "integer", minimum: 1, maximum: 3 },
    },
    required: ["text"],
  },
  output_schema: { type: "object", properties: { text: { type: "string" } }, required: ["text"] },
};

const asAdmin: Me = {
  username: "ada",
  tenant: "default",
  roles: ["admin"],
  scopes: ["agent:run", "agent:read", "agent:admin", "admin:*"],
  credential: "session",
  csrf: "c1",
};
const asUser: Me = { ...asAdmin, username: "uma", roles: ["user"], scopes: ["agent:run", "agent:read"] };

type Fake = { seen: Request[]; bodies: string[]; stored: CredentialInfo[] };

/**
 * A fake /v1 with a session for me. As on the server, the system prompt is
 * sent to admins only, and a user gets 403 on admin endpoints; what the
 * console sends is kept in bodies (so a test can see a key left only once).
 */
function fakeServer(me: Me, opts: { vault?: boolean; system?: boolean; tools?: unknown[] } = {}): Fake {
  const fake: Fake = { seen: [], bodies: [], stored: [] };
  const admin = me.roles.includes("admin");
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const req = new Request(new URL(String(input), "http://console.test"), init);
      const url = new URL(req.url);
      const path = url.pathname;
      fake.seen.push(req);
      if (req.method !== "GET") fake.bodies.push(await req.clone().text());
      const error = (status: number, code: string) =>
        Response.json({ v: 1, error: { code, message: code } }, { status });
      if (path === "/v1/auth/me") return Response.json({ v: 1, ...me });
      if (path === "/v1/agents") return Response.json({ v: 1, agents: agentsList });
      if (path.startsWith("/v1/agents/")) {
        const a = agentsList.find((x) => x.name === decodeURIComponent(path.split("/")[3] ?? ""));
        if (!a) return error(404, "not_found");
        return Response.json({
          v: 1,
          ...a,
          ...(admin && opts.system !== false ? { system: "You are careful. Cite sources." } : {}),
        });
      }
      if (path === "/v1/toolsets") return Response.json({ v: 1, toolsets });
      if (path === "/v1/toolsets/demo/tools") {
        return Response.json({ v: 1, toolset: "demo", status: "ready", tools: opts.tools ?? [echo] });
      }
      if (path === "/v1/conversations") return Response.json({ v: 1, conversations: [] });
      if (path === "/v1/credentials" && req.method === "GET") {
        if (opts.vault === false) return error(404, "not_found");
        const mail = fake.stored.find((c) => c.slot === "mail");
        return Response.json({
          v: 1,
          credentials: [
            mail ?? { slot: "mail", scope: "user", status: "not_connected", oauth: false, toolsets: ["mail"] },
          ],
        });
      }
      if (path === "/v1/credentials/mail" && req.method === "PUT") {
        const info = {
          slot: "mail",
          scope: "user",
          status: "connected",
          kind: "api_key",
          oauth: false,
          toolsets: ["mail"],
          created_at: "2026-10-12T09:00:00Z",
          updated_at: "2026-10-12T09:00:00Z",
        };
        fake.stored = [info];
        return Response.json({ v: 1, ...info });
      }
      if (path === "/v1/credentials/mail" && req.method === "DELETE") {
        fake.stored = [];
        return new Response(null, { status: 204 });
      }
      return error(404, "not_found");
    }),
  );
  return fake;
}

function renderAt(path: string) {
  return render(<App router={createMemoryRouter(routes, { initialEntries: [path] })} />);
}

beforeEach(() => {
  sessionStorage.clear();
  localStorage.clear();
});
afterEach(() => vi.unstubAllGlobals());

// The admin pages are a lazy chunk: its first load, under a full run, can take over a second.
const h1 = (name: string) => screen.findByRole("heading", { level: 1, name }, { timeout: 5000 });

describe("the agents page", () => {
  it("lists the agents and shows the detail, with the system prompt for an admin", async () => {
    fakeServer(asAdmin);
    renderAt("/agents/researcher");
    expect(await h1("researcher")).toBeInTheDocument();
    expect(
      within(screen.getByRole("navigation", { name: "Agents" })).getByRole("link", { name: /researcher/ }),
    ).toBeInTheDocument();
    expect(screen.getByText("anthropic · claude-opus-5-5 · effort low")).toBeInTheDocument();
    expect(screen.getByText("citations")).toBeInTheDocument();
    // Each toolset links to its page, with its status for screen readers.
    const ts = screen.getByRole("list", { name: "Toolsets" });
    expect(within(ts).getByRole("link", { name: /parallel, skipped/ })).toHaveAttribute("href", "/toolsets/parallel");
    expect(screen.getByText(/2 of its 3 toolsets aren't ready: parallel, mail/)).toBeInTheDocument();
    // The system prompt: collapsed, there for an admin.
    const summary = await screen.findByText("System prompt (admins only)");
    expect(screen.getByText("You are careful. Cite sources.")).not.toBeVisible();
    await userEvent.click(summary);
    expect(screen.getByText("You are careful. Cite sources.")).toBeVisible();
  });

  it("opens a new chat with the agent from Chat with this agent", async () => {
    fakeServer(asAdmin);
    const { container } = renderAt("/agents/researcher");
    await h1("researcher");
    await userEvent.click(screen.getByRole("link", { name: "Chat with researcher" }));
    expect(await screen.findByRole("button", { name: "Agent: researcher" })).toBeInTheDocument();
    expect(container.ownerDocument.title).toBe("New chat · agentkit");
  });

  it("shows no system prompt when the API sends none (a user's view, or an older server)", async () => {
    // The page shows what the API sends, whatever /me says.
    fakeServer(asAdmin, { system: false });
    renderAt("/agents/researcher");
    expect(await h1("researcher")).toBeInTheDocument();
    await waitFor(() => expect(screen.queryByLabelText("Loading")).not.toBeInTheDocument());
    expect(screen.queryByText("System prompt (admins only)")).not.toBeInTheDocument();
  });
});

describe("the toolsets page", () => {
  it("shows a skipped optional toolset with its reason, and how to set its key", async () => {
    fakeServer(asAdmin);
    renderAt("/toolsets/parallel");
    expect(await h1("parallel")).toBeInTheDocument();
    expect(
      screen.getByText("Skipped: parallel: the API key is missing: set the environment variable PARALLEL_API_KEY"),
    ).toBeInTheDocument();
    expect(screen.getByText(/Set PARALLEL_API_KEY in the server's environment/)).toBeInTheDocument();
    expect(screen.getByText("The MCP URL appears once the toolset is ready.")).toBeInTheDocument();
    expect(screen.queryByLabelText("Command")).not.toBeInTheDocument();
  });

  it("gives the MCP URL and a claude mcp add command with a placeholder, never the token", async () => {
    sessionStorage.setItem("agentkit.token", canary); // a token the console holds
    fakeServer({ ...asAdmin, credential: "api_key", roles: [], scopes: ["*"] });
    const write = vi.fn(async () => {});
    Object.defineProperty(navigator, "clipboard", { value: { writeText: write }, configurable: true });
    renderAt("/toolsets/demo");
    expect(await h1("demo")).toBeInTheDocument();
    const url = `${window.location.origin}/mcp/demo`;
    expect(screen.getByLabelText("MCP URL")).toHaveTextContent(url);
    const command = screen.getByLabelText("Command");
    expect(command).toHaveTextContent(
      `claude mcp add --transport http demo ${url} --header "Authorization: Bearer $AGENTKIT_API_TOKEN"`,
    );
    await userEvent.click(screen.getByRole("button", { name: "Copy command" }));
    expect(write).toHaveBeenCalledWith(mcpAddCommand("demo", url));
    expect(await screen.findByRole("button", { name: "Copied" })).toBeInTheDocument();
    expect(pageMarkup()).not.toContain(canary);
    expect(JSON.stringify(write.mock.calls)).not.toContain(canary);
  });

  it("renders each tool's schema as a readable field list, with the raw schema on demand", async () => {
    fakeServer(asAdmin);
    renderAt("/toolsets/demo");
    await h1("demo");
    const tool = await screen.findByText("echo");
    await userEvent.click(tool);
    const input = screen.getByRole("table", { name: "Input" });
    const rows = within(input).getAllByRole("row").slice(1);
    expect(rows.map((r) => r.textContent)).toEqual(["textrequiredstringtext to echo", "timesinteger1 to 3."]);
    expect(screen.getByRole("table", { name: "Output" })).toHaveTextContent("text");
    expect(screen.queryByLabelText("echo raw schema")).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Show raw schema" }));
    expect(screen.getByLabelText("echo raw schema")).toHaveTextContent('"description": "text to echo"');
    expect(screen.getByRole("button", { name: "Hide raw schema" })).toHaveAttribute("aria-expanded", "true");
  });

  // An MCP server's schema is untrusted: its words are text, never markup or a link.
  it("shows a tool's untrusted description, title and schema text as text", async () => {
    const markup = `<img src=x onerror="window.pwned=1"><a href="javascript:window.pwned=1">click</a>`;
    fakeServer(asAdmin, {
      tools: [
        {
          ...echo,
          description: `Echoes.\n${markup}`,
          input_schema: {
            type: "object",
            title: markup,
            properties: {
              [`x${markup}`]: { type: "string", title: markup, description: markup, default: markup, format: markup },
              link: { type: "string", enum: ["javascript:alert(1)", markup] },
            },
          },
        },
      ],
    });
    renderAt("/toolsets/demo");
    await h1("demo");
    await userEvent.click(await screen.findByText("echo"));
    await userEvent.click(screen.getByRole("button", { name: "Show raw schema" }));
    const input = screen.getByRole("table", { name: "Input" });
    expect(input).toHaveTextContent(markup);
    expect(screen.getByLabelText("echo raw schema")).toHaveTextContent("javascript:window.pwned=1");
    expect(document.querySelector("main img, main [onerror]")).toBeNull();
    const hrefs = Array.from(document.querySelectorAll("a[href]"), (a) => a.getAttribute("href") ?? "");
    expect(hrefs.filter((h) => !h.startsWith("/") && !h.startsWith("#"))).toEqual([]);
    expect((window as { pwned?: number }).pwned).toBeUndefined();
  });
});

describe("the connections page", () => {
  it("shows statuses and variable names, sets a key once, and never shows it", async () => {
    const fake = fakeServer(asUser);
    renderAt("/connections");
    expect(await h1("Connections")).toBeInTheDocument();
    const mail = await screen.findByRole("row", { name: /^mail/ });
    expect(within(mail).getByText("Not connected")).toBeInTheDocument();
    const parallel = screen.getByRole("row", { name: /^parallel/ });
    expect(within(parallel).getByText("Server key not set")).toBeInTheDocument();
    expect(within(parallel).getByText(/Set PARALLEL_API_KEY/)).toBeInTheDocument();
    expect(within(parallel).queryByRole("button")).not.toBeInTheDocument(); // server keys aren't managed here
    expect(screen.queryByRole("row", { name: /^demo/ })).not.toBeInTheDocument(); // needs none

    await userEvent.click(within(mail).getByRole("button", { name: "Connect mail" }));
    const dialog = screen.getByRole("dialog", { name: "Connect mail" });
    const field = within(dialog).getByLabelText("API key");
    expect(field).toHaveAttribute("type", "password");
    expect(field).toHaveAttribute("autocomplete", "off");
    await userEvent.type(field, canary);
    await userEvent.click(within(dialog).getByRole("button", { name: "Save" }));

    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    const after = await screen.findByRole("row", { name: /^mail/ });
    await waitFor(() => expect(within(after).getByText("Connected")).toBeInTheDocument());
    expect(within(after).getByText("Your key, added 12 Oct 2026")).toBeInTheDocument();
    expect(fake.bodies).toEqual([JSON.stringify({ scope: "user", kind: "api_key", api_key: canary })]);
    expect(pageMarkup()).not.toContain(canary);
    const stored = [sessionStorage, localStorage].flatMap((s) =>
      Array.from({ length: s.length }, (_, i) => s.getItem(s.key(i) ?? "")),
    );
    expect(JSON.stringify(stored)).not.toContain(canary);
  });

  it("disconnects after asking", async () => {
    const fake = fakeServer(asUser);
    fake.stored = [
      { slot: "mail", scope: "user", status: "connected", kind: "api_key", oauth: false, toolsets: ["mail"] },
    ];
    renderAt("/connections");
    const mail = await screen.findByRole("row", { name: /^mail/ });
    await userEvent.click(within(mail).getByRole("button", { name: "Disconnect mail" }));
    const dialog = screen.getByRole("dialog", { name: "Disconnect mail?" });
    expect(dialog).toHaveTextContent("mail stops working for you until you connect again. Used by researcher.");
    await userEvent.click(within(dialog).getByRole("button", { name: "Disconnect" }));
    await waitFor(() => expect(within(screen.getByRole("row", { name: /^mail/ })).getByText("Not connected")));
    expect(fake.seen.some((r) => r.method === "DELETE" && r.url.endsWith("/v1/credentials/mail?scope=user"))).toBe(
      true,
    );
  });

  it("says when the server keeps no credentials, and still shows server keys", async () => {
    fakeServer(asUser, { vault: false });
    renderAt("/connections");
    expect(await screen.findByText(/keys and accounts can't be connected here/)).toBeInTheDocument();
    expect(screen.getByRole("row", { name: /^parallel/ })).toHaveTextContent("Server key not set");
    expect(screen.getByRole("row", { name: /^mail/ })).toHaveTextContent("Not available");
  });
});

describe("/connections/done in the same tab", () => {
  it("goes back to the saved route with the result, once", async () => {
    fakeServer(asUser);
    sessionStorage.setItem(connectReturnKey, "/connections");
    const router = createMemoryRouter(routes, { initialEntries: ["/connections/done?slot=mail&result=connected"] });
    render(<App router={router} />);
    expect(await h1("Connections")).toBeInTheDocument();
    expect(router.state.location.pathname).toBe("/connections");
    await waitFor(() => expect(screen.getByText("mail connected.")).toBeInTheDocument());
    expect(sessionStorage.getItem(connectReturnKey)).toBeNull();
  });

  // The saved route is the console's own; one that names another host goes to Connections.
  it.each(["//evil.example/x", "/\\evil.example/x", "https://evil.example/x", "javascript:alert(1)"])(
    "never leaves the console for a saved %s",
    async (saved) => {
      fakeServer(asUser);
      sessionStorage.setItem(connectReturnKey, saved);
      const router = createMemoryRouter(routes, { initialEntries: ["/connections/done?slot=mail&result=denied"] });
      render(<App router={router} />);
      expect(await h1("Connections")).toBeInTheDocument();
      expect(router.state.location.pathname).toBe("/connections");
      expect(
        await screen.findByText("You didn't allow access. Nothing was saved.", { selector: "[role=alert]" }),
      ).toBeInTheDocument();
    },
  );
});

describe("isLocalPath", () => {
  it.each([
    ["/connections", true],
    ["/agents/researcher?x=1", true],
    ["//evil.example", false],
    ["/\\evil.example", false],
    ["https://evil.example/", false],
    ["connections", false],
    ["", false],
  ])("%s is %s", (path, want) => expect(isLocalPath(path)).toBe(want));
});

describe("mcpAddCommand", () => {
  it("keeps a plain name and URL as they are, and quotes anything a shell would read", () => {
    const url = "https://agents.example.com/mcp/demo";
    expect(mcpAddCommand("demo", url)).toBe(
      `claude mcp add --transport http demo ${url} --header "Authorization: Bearer $AGENTKIT_API_TOKEN"`,
    );
    expect(mcpAddCommand("a;rm -rf ~", "https://x.example/$(id)")).toBe(
      `claude mcp add --transport http 'a;rm -rf ~' 'https://x.example/$(id)' --header "Authorization: Bearer $AGENTKIT_API_TOKEN"`,
    );
    expect(shellWord("it's")).toBe(`'it'\\''s'`);
  });
});

describe("roles", () => {
  it("hides the admin navigation from a user, and shows the 403 page on an admin link", async () => {
    fakeServer(asUser);
    renderAt("/agents/researcher");
    expect(await h1("Admins only")).toBeInTheDocument();
    const sections = screen.getByRole("navigation", { name: "Sections" });
    expect(within(sections).queryByRole("link", { name: "Agents" })).not.toBeInTheDocument();
    expect(within(sections).queryByRole("link", { name: "Toolsets" })).not.toBeInTheDocument();
    expect(within(sections).getByRole("link", { name: "Connections" })).toBeInTheDocument();
    expect(screen.queryByText("System prompt (admins only)")).not.toBeInTheDocument();
  });

  it("shows the admin navigation to an admin", async () => {
    fakeServer(asAdmin);
    renderAt("/connections");
    await h1("Connections");
    const sections = screen.getByRole("navigation", { name: "Sections" });
    await waitFor(() => expect(within(sections).getByRole("link", { name: "Agents" })).toBeInTheDocument());
    expect(within(sections).getByRole("link", { name: "Toolsets" })).toBeInTheDocument();
  });

  it("keeps the admin navigation hidden until /me says the caller is an admin", async () => {
    fakeServer(asAdmin);
    const real = globalThis.fetch;
    let answer: () => void = () => {};
    const held = new Promise<void>((resolve) => (answer = resolve));
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        if (String(input).includes("/v1/auth/me")) await held;
        return real(input, init);
      }),
    );
    renderAt("/connections");
    await h1("Connections");
    const sections = screen.getByRole("navigation", { name: "Sections" });
    expect(within(sections).queryByRole("link", { name: "Agents" })).not.toBeInTheDocument();
    expect(within(sections).queryByRole("link", { name: "Toolsets" })).not.toBeInTheDocument();
    answer();
    expect(await within(sections).findByRole("link", { name: "Agents" })).toBeInTheDocument();
  });

  it("shows the 403 page when the API refuses an admin list", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => Response.json({ v: 1, error: { code: "forbidden", message: "no" } }, { status: 403 })),
    );
    renderAt("/toolsets");
    expect(await h1("Admins only")).toBeInTheDocument();
  });
});

describe("hasScope and isAdmin", () => {
  it.each([
    [["*"], "agent:admin", true],
    [["agent:*"], "agent:admin", true],
    [["admin:*"], "agent:admin", false],
    [["agent:read", "agent:run"], "agent:admin", false],
    [["agent:?dmin"], "agent:admin", true],
    [["agent:[a-b]dmin"], "agent:admin", true],
    [["agent:[^a]dmin"], "agent:admin", false],
    [["agent:[admin"], "agent:admin", false],
    [["agent.admin"], "agentXadmin", false],
  ])("%j has %s: %s", (scopes, scope, want) => {
    expect(hasScope({ ...asUser, scopes }, scope)).toBe(want);
  });

  it.each([
    [asAdmin, true],
    [asUser, false],
    [{ ...asUser, credential: "api_key", roles: [], scopes: ["*"] }, true],
    [{ tenant: "default", roles: [], scopes: ["*"] }, true], // no credentials: everything
    [{ ...asUser, roles: ["admin"], scopes: [] }, true],
  ])("%j: %s", (me, want) => {
    expect(isAdmin(me)).toBe(want);
  });

  // The navigation's test (sections.ts, the Tasks view's Triggers tab too): isAdmin, and unknown is no.
  it.each([
    [null, false],
    [asAdmin, true],
    [asUser, false],
    [{ ...asUser, credential: "api_key", roles: [], scopes: ["agent:*"] }, true], // a pattern, as Go matches it
  ])("mayAdmin(%j): %s", (me, want) => {
    expect(mayAdmin(me)).toBe(want);
  });
});

describe("fields", () => {
  it("lists properties in order, nested ones under their parent, with types and help", () => {
    const got = fields({
      type: "object",
      properties: {
        query: { type: "string", description: "what to search", minLength: 1 },
        mode: { enum: ["fast", "deep"], default: "fast" },
        urls: { type: ["null", "array"], items: { type: "string" }, maxItems: 10 },
        filters: {
          type: "object",
          properties: { after: { type: "string", format: "date" }, domains: { $ref: "#/$defs/list" } },
          required: ["after"],
        },
        results: { type: "array", items: { type: "object", properties: { url: { type: "string" } } } },
        either: { anyOf: [{ type: "string" }, { type: "integer" }] },
      },
      required: ["query"],
      $defs: { list: { type: "array", items: { type: "string" }, description: "hosts" } },
    });
    expect(got.map((f) => [f.path, f.depth, f.type, f.required, f.help])).toEqual([
      ["query", 0, "string", true, "what to search At least 1 characters."],
      ["mode", 0, "one of: fast, deep", false, "Default: fast."],
      ["urls", 0, "array of string or null", false, "At most 10 items."],
      ["filters", 0, "object", false, ""],
      ["filters.after", 1, "string (date)", true, ""],
      ["filters.domains", 1, "array of string", false, "hosts"],
      ["results", 0, "array of object", false, ""],
      ["results[].url", 1, "string", false, ""],
      ["either", 0, "string or integer", false, ""],
    ]);
  });

  it("has none for an empty input, or a schema that isn't an object", () => {
    expect(fields({ type: "object", additionalProperties: false })).toEqual([]);
    expect(fields(null)).toEqual([]);
    expect(fields("string")).toEqual([]);
  });

  it("stops at a reference to itself", () => {
    const tree = { $defs: { node: { type: "object", properties: { child: { $ref: "#/$defs/node" } } } } };
    const got = fields({ ...tree, $ref: "#/$defs/node" });
    expect(got.length).toBeLessThan(10);
    expect(got[0]?.path).toBe("child");
    expect(typeOf({ $ref: "#/$defs/missing" })).toBe("missing");
  });
});

describe("credentialWords", () => {
  it.each([
    [{ status: "not_needed" }, null],
    [{ status: "configured", env: "EXA_API_KEY" }, "ok Using the server key Server key EXA_API_KEY"],
    [{ status: "missing", env: "EXA_API_KEY" }, "warn Server key not set Server key EXA_API_KEY"],
    [{ status: "keyless", env: "TAVILY_API_KEY" }, "neutral Keyless Server key TAVILY_API_KEY (optional)"],
    [{ status: "connected", slot: "mail", scope: "user" }, "ok Connected Slot mail, each user connects their own"],
    [{ status: "missing", slot: "crm" }, "warn Not available Slot crm, shared by the tenant, set by an admin"],
  ])("%j", (c, want) => {
    const w = credentialWords(c);
    expect(w && `${w.tone} ${w.word} ${w.source}`).toBe(want);
  });
});

describe("connectionRows", () => {
  const info = (c: Partial<CredentialInfo>): CredentialInfo => ({
    slot: "mail",
    scope: "user",
    status: "not_connected",
    oauth: false,
    toolsets: ["mail"],
    ...c,
  });

  it("puts what needs you first, and leaves out what no agent uses or needs nothing", () => {
    const rows = connectionRows(
      [info({ slot: "crm", status: "connected", kind: "api_key", toolsets: ["crm"] }), info({})],
      [
        ...toolsets,
        { name: "unused", type: "exa", status: "ready", tool_count: 1, credential: { status: "missing", env: "X" } },
      ],
      [{ ...agentsList[0]!, toolsets: [...agentsList[0]!.toolsets, { name: "crm", status: "ready", tools: [] }] }],
      false,
    );
    expect(rows.map((r) => [r.name, r.word, r.actions.join("+")])).toEqual([
      ["mail", "Not connected", "connect"],
      ["crm", "Connected", "replace+disconnect"],
      ["parallel", "Server key not set", ""],
    ]);
  });

  it("lets only those with credentials:admin manage the tenant's", () => {
    const tenant = info({ scope: "tenant" });
    const used = [agentsList[0]!];
    expect(connectionRows([tenant], [], used, false)[0]).toMatchObject({
      actions: [],
      detail: "An admin sets it for everyone.",
    });
    expect(connectionRows([tenant], [], used, true)[0]).toMatchObject({ actions: ["connect"] });
  });

  it("offers an account to reconnect, and a key to replace", () => {
    const used = [agentsList[0]!];
    const acct = info({ status: "connected", kind: "oauth2", oauth: true });
    expect(connectionRows([acct], [], used, false)[0]).toMatchObject({
      kind: "Account",
      actions: ["reconnect", "disconnect"],
    });
    const expired = info({ status: "needs_reconnect", oauth: true });
    expect(connectionRows([expired], [], used, false)[0]).toMatchObject({ needsYou: true, word: "Needs reconnect" });
  });
});

describe("mcpURL", () => {
  it("resolves the path against the server, and is empty without one", () => {
    expect(mcpURL(toolsets[0]!, "https://agents.example.com")).toBe("https://agents.example.com/mcp/demo");
    expect(mcpURL(toolsets[2]!, "https://agents.example.com")).toBe("");
  });
});
