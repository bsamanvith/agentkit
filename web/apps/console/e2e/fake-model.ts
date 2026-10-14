// A fake Anthropic Messages API for the browser tests: agentkit serve talks
// to it through ANTHROPIC_BASE_URL, so the tests run real runs with no key
// and no cost. It answers POST /v1/messages with a stream in the API's wire
// shape (see cmd/agentkit/testdata/e2e/*/01.http), picked by the last user
// text:
//
//   "echo"   calls the demo toolset's echo tool, then says what it echoed
//   "xss"    echoes hostile HTML, then answers with more and a javascript: link
//   "stall"  starts an answer and never finishes it, until the request ends
//   other    "You said: …" with the number of messages so far
//
// Run with Node 24 (type stripping): node e2e/fake-model.ts <port>.
import { createServer, type ServerResponse } from "node:http";

type Block = { type: string; text?: string; content?: Block[] };
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

const server = createServer((req, res) => {
  if (req.method === "GET" && req.url === "/healthz") return res.end("ok");
  if (req.method === "GET" && req.url === "/stalls") return res.end(JSON.stringify({ ended: stallsEnded }));
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
    start(res);

    const xss = /\bxss\b/i.test(said);
    // The echo tool's result is back: say what it echoed.
    const result = last && blocks(last).find((b) => b.type === "tool_result");
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
