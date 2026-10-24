// A fake Anthropic Messages API for the browser tests: agentkit serve talks
// to it through ANTHROPIC_BASE_URL, so the tests run real runs with no key
// and no cost. It answers POST /v1/messages with a stream in the API's wire
// shape (see cmd/agentkit/testdata/e2e/*/01.http), picked by the last user
// text:
//
//   "echo"   calls the demo toolset's echo tool, then says what it echoed
//   "xss"    echoes hostile HTML, then answers with more and a javascript: link
//   "stall"  starts an answer and never finishes it, until the request ends
//   "news"   shows an A2UI surface with ui_render in two calls (a loading
//            card, then the form), and answers its action ("search") by
//            changing the surface and saying what was asked; with "slowly"
//            in the conversation's first message, the second call and the
//            action's answer wait for GET /release (GET /gates: how many wait)
//   "unsafe" shows a surface with markup in its text, an image from another
//            origin (GET /hook.png, counted at GET /hooks) and a javascript: link
//   "background" with "echo": echoes, then holds the answer until GET /release
//   "handbook" searches the knowledge base (knowledge_search) for the
//            restocking fee, then answers with what the search returned
//   "delegate" asks agent:worker "What is 6*7?" (agent_message, wait), which
//            answers 42, and says what came back
//   "findtool" finds the echo tool with tool_search (the chat's Search
//            mode), calls it, and says what it echoed
//   other    "You said: …" with the number of messages so far
//
// Run with Node 24 (type stripping): node e2e/fake-model.ts <port>.
import { createServer, type ServerResponse } from "node:http";

type Block = { type: string; text?: string; name?: string; content?: Block[] };
type Message = { role: string; content: string | Block[] };

const port = Number(process.argv[2] ?? 18479);

const blocks = (m: Message): Block[] =>
  typeof m.content === "string" ? [{ type: "text", text: m.content }] : m.content;
const textOf = (m: Message) =>
  blocks(m)
    .filter((b) => b.type === "text")
    .map((b) => b.text ?? "")
    .join("");

function send(res: ServerResponse, type: string, data: object) {
  res.write(`event: ${type}\ndata: ${JSON.stringify({ type, ...data })}\n\n`);
}

function start(res: ServerResponse) {
  res.writeHead(200, { "Content-Type": "text/event-stream; charset=utf-8", "Request-Id": "req_fake" });
  send(res, "message_start", {
    message: {
      id: `msg_fake${Date.now()}`,
      type: "message",
      role: "assistant",
      model: "claude-haiku-fake",
      content: [],
      stop_reason: null,
      stop_sequence: null,
      usage: { input_tokens: 10, cache_creation_input_tokens: 0, cache_read_input_tokens: 0, output_tokens: 1 },
    },
  });
}

function finish(res: ServerResponse, stop: string, outputTokens: number) {
  send(res, "message_delta", {
    delta: { stop_reason: stop, stop_sequence: null },
    usage: {
      input_tokens: 10,
      cache_creation_input_tokens: 0,
      cache_read_input_tokens: 0,
      output_tokens: outputTokens,
    },
  });
  send(res, "message_stop", {});
  res.end();
}

function text(res: ServerResponse, index: number, s: string) {
  send(res, "content_block_start", { index, content_block: { type: "text", text: "" } });
  // Two deltas, so the page sees text stream in.
  const half = Math.ceil(s.length / 2);
  for (const part of [s.slice(0, half), s.slice(half)]) {
    if (part) send(res, "content_block_delta", { index, delta: { type: "text_delta", text: part } });
  }
  send(res, "content_block_stop", { index });
}

/** Stalled answers whose request ended (the run was cancelled): GET /stalls. */
let stallsEnded = 0;

/** Answers held until the test releases them (GET /release), and the image requests the page must never make. */
let gates = 0;
let released = 0;
let hooks = 0;

function gate(go: () => void) {
  const n = ++gates;
  const wait = () => (released >= n ? go() : setTimeout(wait, 20));
  wait();
}

function toolUse(res: ServerResponse, name: string, input: object) {
  send(res, "content_block_start", {
    index: 0,
    content_block: {
      type: "tool_use",
      id: `toolu_fake${Date.now()}${Math.random().toString(36).slice(2, 6)}`,
      name,
      input: {},
    },
  });
  send(res, "content_block_delta", {
    index: 0,
    delta: { type: "input_json_delta", partial_json: JSON.stringify(input) },
  });
  send(res, "content_block_stop", { index: 0 });
  finish(res, "tool_use", 40);
}

const catalogId = "https://a2ui.org/specification/v0_9/catalogs/basic/catalog.json";
const ui = (body: object) => ({ version: "v0.9", ...body });

/** The news form's components, with this title. */
function newsForm(title: string) {
  return ui({
    updateComponents: {
      surfaceId: "news",
      components: [
        { id: "root", component: "Card", child: "col" },
        { id: "col", component: "Column", children: ["title", "topic", "sources", "recent", "search"] },
        { id: "title", component: "Text", variant: "h3", text: title },
        { id: "topic", component: "TextField", label: "Topic", value: { path: "/topic" } },
        {
          id: "sources",
          component: "ChoicePicker",
          label: "Sources",
          variant: "multipleSelection",
          displayStyle: "chips",
          options: [
            { label: "News sites", value: "news" },
            { label: "Blogs", value: "blogs" },
            { label: "Forums", value: "forums" },
          ],
          value: { path: "/sources" },
        },
        { id: "recent", component: "CheckBox", label: "Past week only", value: { path: "/recent" } },
        {
          id: "search",
          component: "Button",
          variant: "primary",
          child: "search_label",
          action: {
            event: {
              name: "search",
              context: { topic: { path: "/topic" }, sources: { path: "/sources" }, recent: { path: "/recent" } },
            },
          },
        },
        { id: "search_label", component: "Text", text: "Search" },
      ],
    },
  });
}

type Action = { name: string; surfaceId: string; context: Record<string, unknown> };

/** The action of a ui_action message (a2ui.ActionText), if it is one. */
function actionOf(text: string): Action | undefined {
  const m = /^<ui_action>\n([\s\S]*)\n<\/ui_action>$/.exec(text);
  if (!m?.[1]) return undefined;
  return (JSON.parse(m[1]) as { action: Action }).action;
}

/** The answers that show UI; false when the conversation isn't one of them. */
function answerUI(res: ServerResponse, messages: Message[]): boolean {
  const texts = messages.filter((m) => m.role === "user" && textOf(m) !== "");
  const first = texts[0] ? textOf(texts[0]) : "";
  const said = texts.at(-1) ? textOf(texts.at(-1) as Message) : "";
  const since = messages.slice(messages.lastIndexOf(texts.at(-1) as Message) + 1);
  const calls = since.filter((m) => m.role === "assistant" && blocks(m).some((b) => b.type === "tool_use")).length;
  const slow = /\bslowly\b/i.test(first);
  const action = actionOf(said);
  const later = (f: () => void) => (slow ? gate(f) : f());

  if (action?.surfaceId === "news") {
    const c = action.context as { topic?: string; sources?: string[]; recent?: boolean };
    if (calls === 0) {
      later(() => {
        start(res);
        toolUse(res, "ui_render", { messages: [newsForm(`Searching for “${c.topic ?? ""}”`)] });
      });
    } else {
      start(res);
      text(
        res,
        0,
        `Searching ${(c.sources ?? []).join(" and ") || "everything"} for “${c.topic ?? ""}”${c.recent ? " from the past week" : ""}.`,
      );
      finish(res, "end_turn", 20);
    }
    return true;
  }
  if (/\bnews\b/i.test(first) && !action) {
    if (calls === 0) {
      start(res);
      toolUse(res, "ui_render", {
        messages: [
          ui({ createSurface: { surfaceId: "news", catalogId } }),
          ui({
            updateComponents: {
              surfaceId: "news",
              components: [
                { id: "root", component: "Card", child: "col" },
                { id: "col", component: "Column", children: ["title", "loading"] },
                { id: "title", component: "Text", variant: "h3", text: "Narrow the news search" },
                { id: "loading", component: "Text", variant: "caption", text: "Loading the options…" },
              ],
            },
          }),
        ],
      });
    } else if (calls === 1) {
      later(() => {
        start(res);
        toolUse(res, "ui_render", {
          messages: [
            newsForm("Narrow the news search"),
            ui({ updateDataModel: { surfaceId: "news", value: { topic: "", sources: ["news"], recent: false } } }),
          ],
        });
      });
    } else {
      start(res);
      text(res, 0, "Pick a topic and the sources, then Search.");
      finish(res, "end_turn", 12);
    }
    return true;
  }
  if (/\bunsafe\b/i.test(first)) {
    start(res);
    if (calls > 0) {
      text(res, 0, "Here it is.");
      finish(res, "end_turn", 5);
      return true;
    }
    toolUse(res, "ui_render", {
      messages: [
        ui({ createSurface: { surfaceId: "unsafe", catalogId } }),
        ui({
          updateComponents: {
            surfaceId: "unsafe",
            components: [
              { id: "root", component: "Column", children: ["hostile", "pixel", "bad_link", "good_link"] },
              {
                id: "hostile",
                component: "Text",
                text: '<img src=x onerror="window.__xss=7"><script>window.__xss=8</script> **not bold**',
              },
              { id: "pixel", component: "Image", url: { path: "/img" }, description: "A tracking pixel" },
              {
                id: "bad_link",
                component: "Button",
                child: "bad_label",
                action: { functionCall: { call: "openUrl", args: { url: { path: "/link" } } } },
              },
              { id: "bad_label", component: "Text", text: "Open the page" },
              {
                id: "good_link",
                component: "Button",
                child: "good_label",
                action: { functionCall: { call: "openUrl", args: { url: "https://example.com/docs" } } },
              },
              { id: "good_label", component: "Text", text: "Read the docs" },
            ],
          },
        }),
        ui({
          updateDataModel: {
            surfaceId: "unsafe",
            value: { img: `http://127.0.0.1:${port}/hook.png`, link: "javascript:window.__xss=9" },
          },
        }),
      ],
    });
    return true;
  }
  return false;
}

const server = createServer((req, res) => {
  if (req.method === "GET" && req.url === "/healthz") return res.end("ok");
  if (req.method === "GET" && req.url === "/stalls") return res.end(JSON.stringify({ ended: stallsEnded }));
  if (req.method === "GET" && req.url === "/gates") return res.end(JSON.stringify({ waiting: gates - released }));
  if (req.method === "GET" && req.url === "/release") {
    released++;
    return res.end("ok");
  }
  if (req.method === "GET" && req.url === "/hooks") return res.end(JSON.stringify({ hooks }));
  if (req.method === "GET" && req.url?.startsWith("/hook.png")) {
    hooks++;
    res.writeHead(404);
    return res.end();
  }
  if (req.method !== "POST" || !req.url?.startsWith("/v1/messages")) {
    res.writeHead(404, { "Content-Type": "application/json" });
    return res.end(JSON.stringify({ type: "error", error: { type: "not_found_error", message: "fake model" } }));
  }
  let body = "";
  req.on("data", (c: Buffer) => (body += c.toString()));
  req.on("end", () => {
    const { messages, tools = [] } = JSON.parse(body) as { messages: Message[]; tools?: { name: string }[] };
    const last = messages.at(-1);
    const users = messages.filter((m) => m.role === "user" && textOf(m) !== "");
    const said = [...users].reverse().map(textOf)[0] ?? "";
    if (answerUI(res, messages)) return;
    start(res);

    const xss = /\bxss\b/i.test(said);
    // The echo tool's result is back: say what it echoed.
    const result = last && blocks(last).find((b) => b.type === "tool_result");
    const resultText = () =>
      typeof result?.content === "string" ? result.content : (result?.content ?? []).map((b) => b.text ?? "").join("");
    // Tasks (tasks.spec.ts): a "background" task echoes, then holds
    // its answer until GET /release, so it is still running while the page
    // comes and goes.
    if (result && /\bbackground\b/i.test(said)) {
      gate(() => {
        text(res, 0, `Background work done: ${resultText()}.`);
        finish(res, "end_turn", 12);
      });
      return;
    }
    // "handbook" (test/e2e's TestKnowledge) searches the knowledge base
    // and answers with what it found.
    if (/\bhandbook\b/i.test(said)) {
      if (result) {
        text(res, 0, `From the handbook: ${resultText()}`);
        return finish(res, "end_turn", 20);
      }
      const name = tools.find((t) => t.name.endsWith("knowledge_search"))?.name ?? "knowledge_search";
      send(res, "content_block_start", {
        index: 0,
        content_block: { type: "tool_use", id: `toolu_fake${Date.now()}`, name, input: {} },
      });
      send(res, "content_block_delta", {
        index: 0,
        delta: {
          type: "input_json_delta",
          partial_json: JSON.stringify({ query: "restocking fee for an opened e-bike" }),
        },
      });
      send(res, "content_block_stop", { index: 0 });
      return finish(res, "tool_use", 20);
    }
    // "findtool" (T114, T115): tool_search for the echo tool, then echo,
    // then say what it echoed.
    if (/\bfindtool\b/i.test(said)) {
      const called = (pred: (name: string) => boolean) =>
        messages.some(
          (m) => m.role === "assistant" && blocks(m).some((b) => b.type === "tool_use" && pred(b.name ?? "")),
        );
      if (result && called((n) => n.endsWith("echo"))) {
        text(res, 0, `Found the echo tool, which said ${resultText()}.`);
        return finish(res, "end_turn", 12);
      }
      const searched = called((n) => n === "tool_search");
      const name = searched ? (tools.find((t) => t.name.endsWith("echo"))?.name ?? "echo") : "tool_search";
      send(res, "content_block_start", {
        index: 0,
        content_block: { type: "tool_use", id: `toolu_fake${Date.now()}`, name, input: {} },
      });
      send(res, "content_block_delta", {
        index: 0,
        delta: {
          type: "input_json_delta",
          partial_json: JSON.stringify(searched ? { text: "mango" } : { query: "echo" }),
        },
      });
      send(res, "content_block_stop", { index: 0 });
      return finish(res, "tool_use", 20);
    }
    // "delegate" asks the worker agent and waits for its reply; the worker
    // answers "6*7" with 42.
    if (/\bdelegate\b/i.test(said)) {
      if (result) {
        text(res, 0, `The worker replied: ${resultText()}`);
        return finish(res, "end_turn", 12);
      }
      const name = tools.find((t) => t.name.endsWith("agent_message"))?.name ?? "agent_message";
      send(res, "content_block_start", {
        index: 0,
        content_block: { type: "tool_use", id: `toolu_fake${Date.now()}`, name, input: {} },
      });
      send(res, "content_block_delta", {
        index: 0,
        delta: {
          type: "input_json_delta",
          partial_json: JSON.stringify({ to: "agent:worker", text: "What is 6*7?", wait: true }),
        },
      });
      send(res, "content_block_stop", { index: 0 });
      return finish(res, "tool_use", 20);
    }
    if (said.includes("What is 6*7?")) {
      text(res, 0, "42");
      return finish(res, "end_turn", 2);
    }
    if (result && xss) {
      text(
        res,
        0,
        'Hostile: <img src=x onerror="window.__xss=1"> <script>window.__xss=2</script> ' +
          "[a link](javascript:window.__xss=3) and **bold**.",
      );
      return finish(res, "end_turn", 30);
    }
    if (result) {
      text(res, 0, `The echo tool said ${(result.content ?? []).map((b) => b.text ?? "").join("")}.`);
      return finish(res, "end_turn", 12);
    }
    if (xss || /\becho\b/i.test(said)) {
      // With "xss", the tool's input and result are hostile too.
      const input = xss ? '<img src=x onerror="window.__xss=4"><script>window.__xss=5</script>' : "pineapple";
      const echo = tools.find((t) => t.name.endsWith("__echo"))?.name ?? "echo";
      send(res, "content_block_start", {
        index: 0,
        content_block: { type: "tool_use", id: `toolu_fake${Date.now()}`, name: echo, input: {} },
      });
      send(res, "content_block_delta", {
        index: 0,
        delta: { type: "input_json_delta", partial_json: JSON.stringify({ text: input }) },
      });
      send(res, "content_block_stop", { index: 0 });
      return finish(res, "tool_use", 20);
    }
    if (/\bstall\b/i.test(said)) {
      send(res, "content_block_start", { index: 0, content_block: { type: "text", text: "" } });
      send(res, "content_block_delta", { index: 0, delta: { type: "text_delta", text: "Working on it" } });
      res.on("close", () => stallsEnded++);
      return; // never ends: the run is stopped by the page
    }
    text(res, 0, `You said: ${said}. That's ${users.length} message${users.length === 1 ? "" : "s"} from you.`);
    finish(res, "end_turn", 15);
  });
});

server.listen(port, "127.0.0.1");
