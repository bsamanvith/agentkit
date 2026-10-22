import { describe, expect, it } from "vitest";
import { applyEvent, startAnswer, threadFromHistory, type AssistantTurn, type Turn } from "../thread";
import {
  foldSurfaces,
  getAt,
  joinPath,
  parseActionText,
  safeUrl,
  setAt,
  SurfaceEdits,
  withEdits,
  type UIMessage,
} from "./model";
import { call, failingChecks, formatDate, interpolate, resolve, resolveContext } from "./resolve";

const catalogId = "https://a2ui.org/specification/v0_9/catalogs/basic/catalog.json";
const create = (id: string): UIMessage => ({ version: "v0.9", createSurface: { surfaceId: id, catalogId } });
const comps = (id: string, components: object[]): UIMessage => ({
  version: "v0.9",
  updateComponents: { surfaceId: id, components },
});
const data = (id: string, body: object): UIMessage => ({
  version: "v0.9",
  updateDataModel: { surfaceId: id, ...body },
});
const del = (id: string): UIMessage => ({ version: "v0.9", deleteSurface: { surfaceId: id } });

const answer = (key: string, messages: UIMessage[]): AssistantTurn => ({
  role: "assistant",
  key,
  parts: [],
  state: "done",
  ui: messages.map((message, i) => ({ key: `${key}:ui${i}`, message })),
});

describe("foldSurfaces", () => {
  it("creates, merges components, sets and removes data, and deletes", () => {
    const turns: Turn[] = [
      answer("a", [
        create("s"),
        comps("s", [
          { id: "root", component: "Text", text: "one" },
          { id: "x", component: "Text", text: "x" },
        ]),
        data("s", { value: { user: { name: "Ann", tmp: 1 } } }),
      ]),
      answer("b", [
        comps("s", [{ id: "root", component: "Text", text: "two" }]),
        data("s", { path: "/user/name", value: "Bea" }),
        data("s", { path: "/user/tmp" }),
        comps("nope", [{ id: "root", component: "Text", text: "dropped" }]),
      ]),
    ];
    const s = foldSurfaces(turns).get("a:ui0");
    expect(s?.components.get("root")?.text).toBe("two");
    expect(s?.components.get("x")?.text).toBe("x");
    expect(s?.data).toEqual({ user: { name: "Bea" } });
    expect(s?.revision).toBe(6);
    expect(foldSurfaces(turns).size).toBe(1);

    const gone = foldSurfaces([...turns, answer("c", [del("s"), comps("s", [{ id: "root", component: "Text" }])])]);
    expect(gone.get("a:ui0")?.deleted).toBe(true);
  });

  it("starts a surface created again afresh, in its new place", () => {
    const all = foldSurfaces([
      answer("a", [create("s"), comps("s", [{ id: "root", component: "Text", text: "old" }])]),
      answer("b", [create("s"), comps("s", [{ id: "x", component: "Text", text: "new" }])]),
    ]);
    expect(all.get("a:ui0")?.deleted).toBe(true);
    expect([...(all.get("b:ui0")?.components.keys() ?? [])]).toEqual(["x"]);
  });

  it("marks a surface sent by the person's action until the agent changes it", () => {
    const action = { surfaceId: "s", componentId: "go", name: "go", context: {} };
    const before: Turn[] = [
      answer("a", [create("s"), comps("s", [{ id: "root", component: "Text", text: "hi" }])]),
      { role: "user", key: "u", text: "go", action },
    ];
    expect(foldSurfaces(before).get("a:ui0")?.sent).toEqual(action);
    expect(foldSurfaces([...before, answer("b", [])]).get("a:ui0")?.sent).toEqual(action);
    const after = foldSurfaces([...before, answer("b", [data("s", { path: "/x", value: 1 })])]);
    expect(after.get("a:ui0")?.sent).toBeUndefined();
    // An action on a surface that doesn't exist (any more) marks nothing.
    const lone: Turn[] = [{ role: "user", key: "u", text: "go", action }];
    expect(foldSurfaces(lone).size).toBe(0);
  });
});

describe("the thread's surfaces", () => {
  const input = { messages: [create("s"), comps("s", [{ id: "root", component: "Text", text: "hi" }])] };

  it("come from ui events, each createSurface placing its surface", () => {
    let a = startAnswer("a", 0);
    a = applyEvent(
      a,
      { v: 1, type: "tool_call", id: "1", seq: 1, run_id: "r", tool_use_id: "t", name: "ui_render", input },
      0,
    );
    for (const m of input.messages) {
      a = applyEvent(a, { v: 1, type: "ui", id: "2", seq: 2, run_id: "r", surface_id: "s", message: m }, 0);
    }
    a = applyEvent(a, { v: 1, type: "ui", id: "3", seq: 3, run_id: "r", message: "not one" }, 0);
    expect(a.ui?.map((e) => e.key)).toEqual(["a:ui0", "a:ui1"]);
    expect(a.parts.at(-1)).toEqual({ kind: "surface", key: "a:ui0", surfaceId: "s" });
  });

  it("come from a history's successful ui_render calls, and actions from their text", () => {
    const at = "2026-10-22T03:00:00Z";
    const actionText =
      '<ui_action>\n{"version":"v0.9","action":{"name":"search","surfaceId":"s","sourceComponentId":"go",' +
      '"timestamp":"2026-10-22T03:00:00Z","context":{"q":"\\u003cb\\u003e"}}}\n</ui_action>';
    const turns = threadFromHistory([
      { at, message: { role: "user", content: [{ type: "text", text: "Show me" }] } },
      {
        at,
        message: {
          role: "assistant",
          content: [{ type: "tool_use", id: "bad", name: "ui_render", input: { messages: [create("t")] } }],
        },
      },
      {
        at,
        message: {
          role: "user",
          content: [{ type: "tool_result", tool_use_id: "bad", content: "invalid", is_error: true }],
        },
      },
      { at, message: { role: "assistant", content: [{ type: "tool_use", id: "ok", name: "ui_render", input }] } },
      { at, message: { role: "user", content: [{ type: "tool_result", tool_use_id: "ok", content: "shown" }] } },
      { at, message: { role: "assistant", content: [{ type: "text", text: "Here." }] } },
      { at, message: { role: "user", content: [{ type: "text", text: actionText }] } },
    ]);
    const a = turns[1] as AssistantTurn;
    expect(a.ui?.map((e) => e.message)).toEqual(input.messages);
    expect(a.parts.map((p) => p.kind)).toEqual(["tools", "surface", "text"]);
    expect(turns[2]).toMatchObject({
      role: "user",
      text: "search",
      action: { surfaceId: "s", componentId: "go", name: "search", context: { q: "<b>" } },
    });
    expect(foldSurfaces(turns).get(`${a.key}:ui0`)?.sent?.name).toBe("search");
  });
});

describe("parseActionText", () => {
  it("reads only the server's action text", () => {
    const body = '{"version":"v0.9","action":{"name":"n","surfaceId":"s","sourceComponentId":"c","context":{}}}';
    expect(parseActionText(`<ui_action>\n${body}\n</ui_action>`)).toMatchObject({ name: "n", surfaceId: "s" });
    expect(parseActionText(body)).toBeUndefined();
    expect(parseActionText(`<ui_action>\n${body.replace("v0.9", "v1.0")}\n</ui_action>`)).toBeUndefined();
    expect(parseActionText("<ui_action>\nnot json\n</ui_action>")).toBeUndefined();
    expect(parseActionText('<ui_action>\n{"version":"v0.9","action":{"name":1}}\n</ui_action>')).toBeUndefined();
  });
});

describe("safeUrl", () => {
  // a2ui.SafeURL's cases.
  it.each([
    ["https://example.com/a?b#c", true],
    ["http://127.0.0.1:8080/x.png", true],
    ["HTTPS://EXAMPLE.COM", true],
    ["javascript:alert(1)", false],
    ["JaVaScRiPt:alert(1)", false],
    ["data:image/png;base64,AAAA", false],
    ["https:example.com", false],
    ["//example.com/x", false],
    ["/relative", false],
    ["https://user:pw@example.com/", false],
    ["https://example.com/\nx", false],
    ["ftp://example.com/", false],
    ["", false],
  ])("%j is %s", (u, want) => expect(safeUrl(u)).toBe(want));
  it("takes strings only", () => expect(safeUrl({ path: "/x" })).toBe(false));
});

describe("data", () => {
  it("reads and writes JSON pointers without changing the input", () => {
    const d = { a: { "b/c": [1, { "~x": 2 }] } };
    expect(getAt(d, "/a/b~1c/1/~0x")).toBe(2);
    expect(getAt(d, "")).toBe(d);
    expect(getAt(d, "/a/missing/deeper")).toBeUndefined();
    const set = setAt(d, "/a/new/x", 1);
    expect(set).toEqual({ a: { "b/c": [1, { "~x": 2 }], new: { x: 1 } } });
    expect(d).toEqual({ a: { "b/c": [1, { "~x": 2 }] } });
    expect(setAt([1, 2, 3], "/1", undefined)).toEqual([1, 3]);
    expect(joinPath("/items/2", "name")).toBe("/items/2/name");
    expect(joinPath("/items/2", "/abs")).toBe("/abs");
    expect(joinPath("", "name")).toBe("/name");
  });

  it("keeps the person's edits for the surface's revision only", () => {
    const edits = new SurfaceEdits();
    let calls = 0;
    edits.subscribe(() => calls++);
    edits.set("c", { id: "s", revision: 3 }, "/q", "go");
    expect(edits.get("c", { id: "s", revision: 3 }).get("/q")).toBe("go");
    expect(edits.get("c", { id: "s", revision: 4 }).size).toBe(0);
    expect(edits.get("other", { id: "s", revision: 3 }).size).toBe(0);
    expect(edits.get("c", { id: "s", revision: 3 })).toBe(edits.get("c", { id: "s", revision: 3 }));
    expect(calls).toBe(1);
    expect(withEdits({ q: "", n: 1 }, edits.get("c", { id: "s", revision: 3 }))).toEqual({ q: "go", n: 1 });
  });
});

describe("dynamic values", () => {
  const scope = {
    data: { user: { name: "Ann", n: 3 }, items: [{ t: "a" }, { t: "b" }], when: "2026-02-02T15:17:00" },
    base: "",
  };

  it("resolves literals, bindings, relative paths and calls", () => {
    expect(resolve("x", scope)).toBe("x");
    expect(resolve({ path: "/user/name" }, scope)).toBe("Ann");
    expect(resolve({ path: "t" }, { ...scope, base: "/items/1" })).toBe("b");
    expect(resolve({ call: "not", args: { value: false } }, scope)).toBe(true);
    expect(resolveContext({ who: { path: "/user/name" }, gone: { path: "/nope" }, lit: 2 }, scope)).toEqual({
      who: "Ann",
      gone: null,
      lit: 2,
    });
  });

  it("computes the catalog's functions", () => {
    expect(interpolate("Hi ${/user/name}, \\${not}", scope)).toBe("Hi Ann, ${not}");
    expect(call("formatString", { value: "${/user/n} items" }, scope)).toBe("3 items");
    expect(call("pluralize", { value: { path: "/user/n" }, one: "item", other: "items" }, scope)).toBe("items");
    expect(call("pluralize", { value: 1, one: "item", other: "items" }, scope)).toBe("item");
    expect(call("formatNumber", { value: 1234.5, decimals: 2, grouping: false }, scope)).toMatch(/^1234[.,]50$/);
    expect(call("required", { value: "" }, scope)).toBe(false);
    expect(call("regex", { value: "1234567890", pattern: "^\\d{10}$" }, scope)).toBe(true);
    expect(call("regex", { value: "x", pattern: "(" }, scope)).toBe(false);
    expect(call("length", { value: "abc", min: 4 }, scope)).toBe(false);
    expect(call("numeric", { value: "5", min: 1, max: 9 }, scope)).toBe(true);
    expect(call("email", { value: "a@b.co" }, scope)).toBe(true);
    expect(call("and", { values: [true, { call: "not", args: { value: true } }] }, scope)).toBe(false);
    expect(call("or", { values: [false, true] }, scope)).toBe(true);
    expect(call("openUrl", { url: "https://x.y" }, scope)).toBeUndefined();
    expect(formatDate("2026-02-02T15:17:00", "E MMM d, yyyy h:mm a")).toBe("Mon Feb 2, 2026 3:17 PM");
    expect(formatDate("not a date", "yyyy")).toBe("not a date");
  });

  it("lists the messages of failing checks, in both of the spec's forms", () => {
    const checks = [
      { condition: { call: "required", args: { value: { path: "/user/name" } } }, message: "Name is required." },
      { call: "email", args: { value: { path: "/user/name" } }, message: "Not an email." },
    ];
    expect(failingChecks(checks, scope)).toEqual(["Not an email."]);
  });
});
