// The reference AG-UI client against pkg/agui (T069): node run.mjs URL KEY.
//
// It runs, with @ag-ui/client's HttpAgent (which validates every event
// against the protocol's schemas and its verifier) and AG-UI's A2UI
// middleware in front, as CopilotKit's runtime has it:
//
//   surface    "show news" to the agent "painter": a tool call, a ui_render
//              call and its surface, then the answer;
//   action     an action on that surface, on the same thread;
//   interrupt  "note" to the agent "asker": a call held for approval, which
//              ends the run on an interrupt; then the resume that approves
//              it, on the same thread.
//
// It prints one JSON object: every run's events (as the client passed them
// on), outcome, result and error, the agent's messages after it, and every
// warning the client printed (it warns about what it strips).

import { HttpAgent } from "@ag-ui/client";
import { A2UIMiddleware } from "@ag-ui/a2ui-middleware";

const [url, key] = process.argv.slice(2);
const warnings = [];
for (const level of ["warn", "error"]) {
  console[level] = (...args) => warnings.push(args.map(String).join(" "));
}

const runs = [];

async function run(name, agent, params) {
  const rec = { name, events: [], outcome: null, interrupts: [], result: null, error: null };
  try {
    const res = await agent.runAgent(params, {
      onEvent: ({ event }) => {
        rec.events.push(event);
      },
      onRunFinishedEvent: (p) => {
        rec.outcome = p.outcome;
        if (p.outcome === "interrupt") rec.interrupts = p.interrupts;
      },
    });
    rec.result = res.result ?? null;
  } catch (err) {
    rec.error = String(err && err.message ? err.message : err);
  }
  rec.messages = structuredClone(agent.messages);
  runs.push(rec);
  return rec;
}

const headers = { Authorization: `Bearer ${key}` };

const painter = new HttpAgent({ url: `${url}/agui/painter`, headers, threadId: "reference-thread-1" });
painter.use(new A2UIMiddleware());
painter.addMessage({ id: "ref-u1", role: "user", content: "show news" });
await run("surface", painter, {});
await run("action", painter, {
  forwardedProps: {
    a2uiAction: { userAction: { name: "search", surfaceId: "news", sourceComponentId: "go", context: { topic: ["go"] } } },
  },
});

const asker = new HttpAgent({ url: `${url}/agui/asker`, headers, threadId: "reference-thread-2" });
asker.addMessage({ id: "ref-u2", role: "user", content: "note" });
const held = await run("interrupt", asker, {});
const resume = held.interrupts.map((i) => ({ interruptId: i.id, status: "resolved", payload: { approved: true } }));
await run("resume", asker, { resume });

process.stdout.write(JSON.stringify({ runs, warnings }));
