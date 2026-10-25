// The agent builder and versions pages (T077) against a fake /v1 that keeps
// versions as T075's registry does: if_latest, 409 conflict, publish, delete
// a draft but never the published one. The pure parts (agent-def.ts) are
// tested on their own below.

import type { Agent, AgentVersion, Me } from "@agentkit/agent-ui/api";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createMemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { App } from "../app";
import { routes } from "../routes";
import {
  changedLines,
  check,
  definitionOf,
  definitionText,
  diffLines,
  draftOf,
  emptyDraft,
  landing,
  matches,
  splitModel,
} from "./agent-def";

const asAdmin: Me = {
  username: "ada",
  tenant: "default",
  roles: ["admin"],
  scopes: ["agent:run", "agent:read", "agent:admin", "admin:*"],
  credential: "session",
  csrf: "c1",
};

const researcher: Agent = {
  name: "researcher",
  description: "Searches the web.",
  default: true,
  model: { provider: "anthropic", id: "claude-opus-5-5", effort: "low" },
  toolsets: [{ name: "demo", status: "ready", tools: ["echo"] }],
  skills: [],
  memory: false,
  read_only: true,
  models: {
    default: "anthropic/claude-opus-5-5",
    allow: ["anthropic/claude-opus-5-5", "anthropic/claude-haiku-5-5"],
    choices: [],
  },
  tool_descriptions: { echo: "Echo only greetings." },
};

const tools: Record<string, unknown[]> = {
  demo: [
    { name: "echo", description: "Echoes text back.", input_schema: { type: "object" } },
    { name: "calculate", description: "Adds numbers.", input_schema: { type: "object" } },
  ],
  web: [{ name: "search", description: "Searches the web.", input_schema: { type: "object" } }],
};

type Fake = {
  posts: { path: string; body: Record<string, unknown> }[];
  versions: Map<string, { latest: number; published: number; versions: AgentVersion[] }>;
  /** The next save's answer instead of the registry's, once. */
  refuse?: Response;
};

/** A stored agent's version. */
function version(name: string, n: number, def: Record<string, unknown>, published = false): AgentVersion {
  return {
    name,
    version: n,
    published,
    definition: { name, ...def },
    author: "alex",
    created_at: "2026-10-24T09:00:00Z",
  };
}

const helperV1 = { description: "Helps.", model: { id: "claude-sonnet-5-5" }, toolsets: ["demo"], skills: ["./s"] };

function fakeServer(): Fake {
  const fake: Fake = {
    posts: [],
    versions: new Map([
      [
        "helper",
        {
          latest: 2,
          published: 1,
          versions: [version("helper", 1, helperV1, true), version("helper", 2, { ...helperV1, system: "Be brief." })],
        },
      ],
    ]),
  };
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const req = new Request(new URL(String(input), "http://console.test"), init);
      const path = new URL(req.url).pathname;
      const error = (status: number, code: string, extra: object = {}) =>
        Response.json({ v: 1, error: { code, message: code, ...extra } }, { status });
      const parts = path.split("/").map(decodeURIComponent);
      const name = parts[3] ?? "";
      if (path === "/v1/auth/me") return Response.json({ v: 1, ...asAdmin });
      if (path === "/v1/agents") {
        const stored = [...fake.versions]
          .filter(([, l]) => l.published > 0)
          .map(([n, l]) => ({
            name: n,
            description: "Helps.",
            default: false,
            model: { id: "claude-sonnet-5-5" },
            toolsets: [{ name: "demo", status: "ready", tools: ["echo"] }],
            skills: [],
            memory: false,
            version: l.published,
          }));
        return Response.json({ v: 1, agents: [researcher, ...stored] });
      }
      if (path === "/v1/models") {
        return Response.json({
          v: 1,
          default: "anthropic",
          providers: [
            {
              name: "anthropic",
              models: ["anthropic/claude-opus-5-5", "anthropic/claude-sonnet-5-5", "anthropic/claude-haiku-5-5"],
            },
          ],
        });
      }
      if (path === "/v1/toolsets") {
        return Response.json({
          v: 1,
          toolsets: [
            { name: "demo", type: "demo", status: "ready", tool_count: 2 },
            { name: "web", type: "parallel", status: "ready", tool_count: 1 },
          ],
        });
      }
      if (parts[2] === "toolsets" && parts[4] === "tools") {
        return Response.json({ v: 1, toolset: name, status: "ready", tools: tools[name] ?? [] });
      }
      if (path === "/v1/conversations") return Response.json({ v: 1, conversations: [] });
      if (parts[2] === "agents" && parts.length === 4) {
        return name === "researcher"
          ? Response.json({ v: 1, ...researcher, system: "Cite sources." })
          : error(404, "not_found");
      }
      if (parts[2] === "agents" && parts[4] === "versions") {
        if (name === "researcher") {
          return req.method === "GET"
            ? Response.json({ v: 1, name, read_only: true, published: 0, latest: 0, versions: [] })
            : error(409, "read_only");
        }
        const l = fake.versions.get(name);
        if (req.method === "GET" && parts.length === 5) {
          return l ? Response.json({ v: 1, name, ...l }) : error(404, "not_found");
        }
        if (req.method === "DELETE") {
          const n = Number(parts[5]);
          if (!l) return error(404, "not_found");
          if (n === l.published) return error(409, "version_published");
          l.versions = l.versions.filter((v) => v.version !== n);
          return new Response(null, { status: 204 });
        }
        const body = (await req.json()) as {
          definition: Record<string, unknown>;
          if_latest?: number;
          publish?: boolean;
        };
        fake.posts.push({ path, body });
        if (fake.refuse) {
          const r = fake.refuse;
          fake.refuse = undefined;
          return r;
        }
        if ((body.if_latest ?? 0) !== (l?.latest ?? 0)) return error(409, "conflict");
        const next = l ?? { latest: 0, published: 0, versions: [] };
        next.latest++;
        const v = {
          ...version(name, next.latest, {}, body.publish === true),
          definition: body.definition,
          author: "ada",
        };
        next.versions.push(v);
        if (body.publish) next.published = v.version;
        fake.versions.set(name, next);
        return Response.json({ v: 1, ...v }, { status: 201 });
      }
      if (parts[2] === "agents" && parts[4] === "publish") {
        const body = (await req.json()) as { version: number };
        fake.posts.push({ path, body });
        const l = fake.versions.get(name);
        if (!l) return error(404, "not_found");
        l.published = body.version;
        return Response.json({ v: 1, name, ...l });
      }
      return error(404, "not_found");
    }),
  );
  return fake;
}

function renderAt(path: string) {
  const router = createMemoryRouter(routes, { initialEntries: [path] });
  render(<App router={router} />);
  return router;
}

beforeEach(() => {
  sessionStorage.clear();
  localStorage.clear();
});
afterEach(() => vi.unstubAllGlobals());

const h1 = (name: string | RegExp) => screen.findByRole("heading", { level: 1, name });
const field = (name: string | RegExp) => screen.getByRole("textbox", { name });
/** A text field with suggestions (a datalist). */
const combo = (name: string | RegExp) => screen.getByRole("combobox", { name });
/** The builder, once its form is there (the loading page has the same h1). */
const form = (name: string) => screen.findByRole("form", { name });

describe("the agent builder", () => {
  it("saves a new agent as v1 with exactly the definition built", async () => {
    const fake = fakeServer();
    const user = userEvent.setup();
    const router = renderAt("/agents/new");
    await form("New agent");
    await user.type(field("Name"), "scout");
    await user.type(field("Description"), "Finds things.");
    await user.type(combo("Default model"), "anthropic/claude-sonnet-5-5");
    // Two more models by name, and a pattern with its live matches.
    await user.type(combo("Also allowed"), "anthropic/claude-opus-5-5{Enter}");
    await user.type(combo("Also allowed"), "claude-haiku-5-5");
    await user.click(screen.getByRole("button", { name: "Add to also allowed" }));
    expect(within(screen.getByRole("list", { name: "Also allowed" })).getAllByRole("listitem")).toHaveLength(2);
    await user.click(screen.getByRole("button", { name: "Add a pattern" }));
    await user.type(field("Pattern 1"), "anthropic/claude-.*-5-5");
    expect(screen.getByText(/^Matches: anthropic\/claude-opus-5-5, anthropic\/claude-sonnet-5-5/)).toBeInTheDocument();
    await user.type(field("System prompt"), "Be useful.");
    // Two toolsets, and one tool's description overridden.
    await user.click(await screen.findByRole("checkbox", { name: /^demo/ }));
    await user.click(screen.getByRole("checkbox", { name: /^web/ }));
    await user.click(await screen.findByRole("button", { name: "Override the description of echo" }));
    const echo = field("The description echo gets");
    expect(echo).toHaveValue("Echoes text back.");
    await user.clear(echo);
    await user.type(echo, "Echo greetings only.");
    await user.type(field("Turns per run"), "12");
    await user.click(screen.getByRole("button", { name: "Save draft" }));
    await waitFor(() => expect(router.state.location.pathname).toBe("/agents/scout/edit"));
    expect(fake.posts).toEqual([
      {
        path: "/v1/agents/scout/versions",
        body: {
          definition: {
            name: "scout",
            description: "Finds things.",
            model: { provider: "anthropic", id: "claude-sonnet-5-5" },
            models: {
              allow: ["anthropic/claude-opus-5-5", "claude-haiku-5-5"],
              allow_patterns: ["anthropic/claude-.*-5-5"],
            },
            system: "Be useful.",
            toolsets: ["demo", "web"],
            tool_descriptions: { echo: "Echo greetings only." },
            max_turns: 12,
          },
          if_latest: 0,
          publish: false,
        },
      },
    ]);
    // The builder opens the saved agent: v1, a draft.
    await form("Edit scout");
    expect(screen.getByText("Not published · draft v1")).toBeInTheDocument();
  });

  it("checks the form before saving, and lands the server's field errors on their fields", async () => {
    const fake = fakeServer();
    const user = userEvent.setup();
    renderAt("/agents/new");
    await form("New agent");
    await user.type(field("Name"), "bad name");
    await user.click(screen.getByRole("button", { name: "Save draft" }));
    const summary = await screen.findByRole("alert", { name: /2 fields need a fix/ });
    await waitFor(() => expect(summary).toHaveFocus());
    expect(fake.posts).toHaveLength(0);
    expect(field("Name")).toHaveAccessibleDescription(/1 to 64 letters/);
    // The server's errors: on their fields, and linked from the summary.
    await user.clear(field("Name"));
    await user.type(field("Name"), "scout");
    await user.type(combo("Default model"), "claude-sonnet-5-5");
    await user.type(combo("Also allowed"), "gemini/flash{Enter}");
    fake.refuse = Response.json(
      {
        v: 1,
        error: {
          code: "invalid_definition",
          message: "invalid agent definition",
          fields: [
            { field: "models.allow[0]", message: "provider gemini is not one this server builds" },
            { field: "model.id", message: "unknown model" },
            { message: "something about the whole" },
          ],
        },
      },
      { status: 400 },
    );
    await user.click(screen.getByRole("button", { name: "Save draft" }));
    const again = await screen.findByRole("alert", { name: /3 fields need a fix/ });
    expect(combo("Also allowed")).toHaveAccessibleDescription(/provider gemini is not one/);
    expect(combo("Default model")).toHaveAccessibleDescription(/unknown model/);
    expect(within(again).getByText(/something about the whole/)).toBeInTheDocument();
    await user.click(within(again).getByRole("link", { name: /models\.allow\[0\]/ }));
    expect(combo("Also allowed")).toHaveFocus();
    // The section nav marks the section.
    const nav = screen.getByRole("navigation", { name: "On this page" });
    expect(within(nav).getByRole("link", { name: "Models, needs a fix" })).toBeInTheDocument();
    expect(within(nav).getByRole("link", { name: "Tools" })).toBeInTheDocument();
  });

  it("doesn't overwrite a version saved meanwhile: it offers theirs or saving as the next", async () => {
    const fake = fakeServer();
    const user = userEvent.setup();
    renderAt("/agents/helper/edit");
    await form("Edit helper");
    expect(screen.getByText("Published v1 · draft v2")).toBeInTheDocument();
    expect(field("System prompt")).toHaveValue("Be brief.");
    // Someone saves v3 while we edit.
    const l = fake.versions.get("helper")!;
    l.latest = 3;
    l.versions.push(version("helper", 3, { ...helperV1, system: "Be very brief." }));
    await user.type(field("Description"), " A lot.");
    await user.click(screen.getByRole("button", { name: "Save draft" }));
    const dialog = await screen.findByRole("dialog", { name: "Someone saved a version meanwhile" });
    expect(
      within(dialog).getByText("alex saved v3 while you were editing. Your draft isn't saved."),
    ).toBeInTheDocument();
    expect(l.versions).toHaveLength(3);
    await user.click(within(dialog).getByText("What changed in v3"));
    expect(within(dialog).getByRole("group", { name: "v2 to v3" })).toHaveTextContent(
      /Removed: system: "Be brief\.".*Added: system: "Be very brief\."/,
    );
    await user.click(within(dialog).getByRole("button", { name: "Save mine as v4" }));
    await waitFor(() => expect(l.versions).toHaveLength(4));
    expect(fake.posts.map((p) => p.body.if_latest)).toEqual([2, 3]);
    expect(l.versions[3]!.definition.description).toBe("Helps. A lot.");
    expect(l.versions[3]!.definition.skills).toEqual(["./s"]); // kept, though the form doesn't edit it

    // Discarding loads theirs instead.
    l.latest = 5;
    l.versions.push(version("helper", 5, { ...helperV1, description: "Theirs." }));
    await user.type(field("Description"), "!");
    await user.click(screen.getByRole("button", { name: "Save draft" }));
    const second = await screen.findByRole("dialog", { name: "Someone saved a version meanwhile" });
    await user.click(within(second).getByRole("button", { name: "Discard mine, load v5" }));
    expect(field("Description")).toHaveValue("Theirs.");
    expect(l.versions).toHaveLength(5);
  });

  it("opens a config agent read-only, and Duplicate starts a new agent from it", async () => {
    const fake = fakeServer();
    const user = userEvent.setup();
    const router = renderAt("/agents/researcher/edit");
    await form("researcher");
    expect(screen.getByText(/comes from the server config file, so it can't be changed here/)).toBeInTheDocument();
    expect(field("System prompt")).toBeDisabled();
    expect(screen.queryByRole("button", { name: "Save draft" })).not.toBeInTheDocument();
    await user.click(screen.getByRole("link", { name: "Duplicate to edit" }));
    await form("New agent");
    expect(router.state.location.search).toBe("?from=researcher");
    expect(field("Name")).toHaveValue("researcher-copy");
    await user.click(screen.getByRole("button", { name: "Save draft" }));
    await waitFor(() => expect(fake.posts).toHaveLength(1));
    expect(fake.posts[0]!.body.definition).toEqual({
      name: "researcher-copy",
      description: "Searches the web.",
      model: { provider: "anthropic", id: "claude-opus-5-5", effort: "low" },
      models: { allow: ["anthropic/claude-haiku-5-5"] },
      system: "Cite sources.",
      toolsets: ["demo"],
      tool_descriptions: { echo: "Echo only greetings." },
    });
  });

  it("publishes after confirming, saving the changes first", async () => {
    const fake = fakeServer();
    const user = userEvent.setup();
    renderAt("/agents/helper/edit");
    await form("Edit helper");
    // No changes: Publish publishes the newest draft, v2.
    await user.click(screen.getByRole("button", { name: "Publish…" }));
    let dialog = await screen.findByRole("dialog", { name: "Publish v2 of helper?" });
    expect(dialog).toHaveTextContent("1 line differs from v1, which is published now.");
    await user.click(within(dialog).getByRole("button", { name: "Publish v2" }));
    await screen.findByText("Published v2.");
    expect(fake.posts.at(-1)).toEqual({ path: "/v1/agents/helper/publish", body: { version: 2 } });
    expect(screen.getByRole("button", { name: "Publish…" })).toBeDisabled();
    // With changes: saved as v3 and published at once.
    await user.type(field("Turns per run"), "5");
    await user.click(screen.getByRole("button", { name: "Publish…" }));
    dialog = await screen.findByRole("dialog", { name: "Publish v3 of helper?" });
    await user.click(within(dialog).getByRole("button", { name: "Publish v3" }));
    await screen.findByText("Published v3.");
    expect(fake.posts.at(-1)?.body).toMatchObject({ if_latest: 2, publish: true });
    expect(fake.versions.get("helper")!.published).toBe(3);
  });

  it("asks before leaving with unsaved changes", async () => {
    fakeServer();
    const user = userEvent.setup();
    const router = renderAt("/agents/helper/edit");
    await form("Edit helper");
    await user.click(screen.getByRole("link", { name: "Versions" }));
    await h1("helper: versions"); // nothing changed: no question
    await user.click(await screen.findByRole("link", { name: "Edit" }));
    await form("Edit helper");
    await user.type(field("Description"), "?");
    expect(screen.getByText(/Unsaved changes/)).toBeInTheDocument();
    await user.click(screen.getByRole("link", { name: "Versions" }));
    const dialog = await screen.findByRole("dialog", { name: "Leave without saving?" });
    await user.click(within(dialog).getByRole("button", { name: "Stay" }));
    expect(router.state.location.pathname).toBe("/agents/helper/edit");
    expect(field("Description")).toHaveValue("Helps.?");
    await user.click(screen.getByRole("link", { name: "Versions" }));
    await user.click(within(await screen.findByRole("dialog")).getByRole("button", { name: "Leave" }));
    await h1("helper: versions");
  });

  it("shows a variant's fields and saves them", async () => {
    const fake = fakeServer();
    const user = userEvent.setup();
    renderAt("/agents/helper/edit");
    await form("Edit helper");
    await user.click(screen.getByRole("button", { name: "Add a variant" }));
    await user.type(combo("On"), "re:gemini/.*");
    await user.click(screen.getByRole("radio", { name: "Instead of the agent's" }));
    await user.type(field("Variant 1 instructions"), "Answer in short lines.");
    await user.selectOptions(screen.getAllByRole("combobox", { name: "Effort" })[1]!, "high");
    await user.selectOptions(await screen.findByRole("combobox", { name: "Tool to describe" }), "calculate");
    await user.click(screen.getByRole("button", { name: "Override" }));
    await user.type(screen.getByRole("textbox", { name: "calculate" }), "Add on Gemini.");
    await user.click(screen.getByRole("button", { name: "Save draft" }));
    await waitFor(() => expect(fake.posts).toHaveLength(1));
    expect(fake.posts[0]!.body.definition).toMatchObject({
      models: {
        variants: [
          {
            match: "re:gemini/.*",
            system: "Answer in short lines.",
            effort: "high",
            tool_descriptions: { calculate: "Add on Gemini." },
          },
        ],
      },
    });
  });
});

describe("the versions page", () => {
  it("publishes a draft, rolls back, and deletes a draft but never the published one", async () => {
    const fake = fakeServer();
    const user = userEvent.setup();
    renderAt("/agents/helper/versions");
    const nav = await screen.findByRole("navigation", { name: "Versions" });
    expect(
      within(nav)
        .getAllByRole("link")
        .map((l) => l.textContent),
    ).toEqual([expect.stringMatching(/^v2Draft/), expect.stringMatching(/^v1Published/)]);
    // v2 compared with the published v1.
    expect(screen.getByRole("group", { name: "Changes from v1 to v2" })).toHaveTextContent(
      'Added: system: "Be brief."',
    );
    await user.click(screen.getByRole("button", { name: "Publish v2…" }));
    await user.click(within(await screen.findByRole("dialog")).getByRole("button", { name: "Publish v2" }));
    await waitFor(() => expect(fake.versions.get("helper")!.published).toBe(2));
    expect(screen.queryByRole("button", { name: "Delete draft" })).not.toBeInTheDocument();
    // v1 is older now: roll back to it.
    await user.click(within(nav).getByRole("link", { name: /^v1/ }));
    await user.click(await screen.findByRole("button", { name: "Roll back to v1…" }));
    await user.click(within(await screen.findByRole("dialog")).getByRole("button", { name: "Roll back to v1" }));
    await waitFor(() => expect(fake.versions.get("helper")!.published).toBe(1));
    expect(fake.posts.map((p) => p.path)).toEqual(["/v1/agents/helper/publish", "/v1/agents/helper/publish"]);
    // v2 is a draft again: delete it.
    await user.click(within(nav).getByRole("link", { name: /^v2/ }));
    await user.click(await screen.findByRole("button", { name: "Delete draft" }));
    await user.click(within(await screen.findByRole("dialog")).getByRole("button", { name: "Delete v2" }));
    await waitFor(() => expect(within(nav).getAllByRole("link")).toHaveLength(1));
    expect(fake.versions.get("helper")!.versions.map((v) => v.version)).toEqual([1]);
  });

  it("has Edit, Versions and Duplicate on the agent pages, and New agent in the list", async () => {
    fakeServer();
    const user = userEvent.setup();
    renderAt("/agents/helper");
    await h1("helper");
    expect(screen.getByRole("link", { name: "Edit" })).toHaveAttribute("href", "/agents/helper/edit");
    expect(screen.getByRole("link", { name: "Versions" })).toHaveAttribute("href", "/agents/helper/versions");
    expect(screen.getByRole("link", { name: "New agent" })).toHaveAttribute("href", "/agents/new");
    await user.click(screen.getByRole("link", { name: /^researcher/ }));
    await h1("researcher");
    expect(screen.queryByRole("link", { name: "Edit" })).not.toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Duplicate" })).toHaveAttribute("href", "/agents/new?from=researcher");
  });
});

describe("agent definitions", () => {
  it("reads a definition and writes it back as it was, unknown fields included", () => {
    const def = {
      name: "a",
      description: "d",
      model: { provider: "anthropic", id: "m", effort: "high", tool_search: "emulated" },
      models: {
        allow: ["anthropic/x"],
        allow_patterns: ["anthropic/.*"],
        variants: [{ match: "anthropic/x", system_append: "More.", max_tokens: 100, future: 1 }],
      },
      system: "s",
      toolsets: ["demo"],
      runtime_toolsets: ["rt"],
      tool_descriptions: { echo: "e" },
      interaction: "supervised",
      max_turns: 3,
      max_total_tokens: 1000,
      skills: ["./s"],
      tool_policy: "read_only",
      prefix_tool_names: true,
    };
    expect(definitionOf(draftOf(def))).toEqual(def);
    expect(definitionOf(emptyDraft())).toEqual({});
  });

  it("splits model names and finds where field errors land", () => {
    expect(splitModel("gemini/flash")).toEqual({ provider: "gemini", id: "flash" });
    expect(splitModel("claude-x")).toEqual({ id: "claude-x" });
    const fields = new Set(["model", "models.allow", "models.variants[0].match"]);
    expect(landing("model.id", fields)).toBe("model");
    expect(landing("models.allow[3]", fields)).toBe("models.allow");
    expect(landing("models.variants[0].match", fields)).toBe("models.variants[0].match");
    expect(landing("models.variants[1].match", fields)).toBe("");
    expect(landing("", fields)).toBe("");
  });

  it("matches patterns whole, and checks drafts", () => {
    const known = ["anthropic/claude-opus-5-5", "anthropic/claude-sonnet-5-5"];
    expect(matches("anthropic/claude-opus.*", known)).toEqual(["anthropic/claude-opus-5-5"]);
    expect(matches("claude-opus.*", known)).toEqual([]); // anchored at both ends
    expect(matches("(", known)).toEqual([]);
    const d = { ...emptyDraft(), name: "ok", model: "m", patterns: ["("], maxTurns: "0" };
    expect([...check(d, true).keys()]).toEqual(["models.allow_patterns[0]", "max_turns"]);
    expect(check({ ...d, patterns: [], maxTurns: "" }, true).size).toBe(0);
    expect(check({ ...d, name: "no spaces", patterns: [], maxTurns: "" }, false).size).toBe(0); // a name is checked when new
  });

  it("diffs definitions line by line, a sentence per line", () => {
    const a = { system: "One. Two. Three is long enough to make this prompt longer than eighty characters." };
    const b = { system: "One. Two changed. Three is long enough to make this prompt longer than eighty characters." };
    expect(definitionText(a)).toEqual([
      "system: |",
      "  One.",
      "  Two.",
      "  Three is long enough to make this prompt longer than eighty characters.",
    ]);
    expect(diffLines(definitionText(a), definitionText(b)).filter((l) => l.op !== "same")).toEqual([
      { op: "del", text: "  Two." },
      { op: "add", text: "  Two changed." },
    ]);
    expect(changedLines(a, a)).toBe(0);
    expect(changedLines({}, { toolsets: ["x", "y"] })).toBe(3);
  });
});
