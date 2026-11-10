import { readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";

// public/sw.js run against a fake service worker scope: what it shows for a
// push from the server (apps/farm/notify/webpush, Payload) and where a tap
// on the notification goes.

type Handler = (e: unknown) => void;

function worker(windows: { url: string }[] = []) {
  const handlers: Record<string, Handler> = {};
  const shown: { title: string; options: Record<string, unknown> }[] = [];
  const opened: string[] = [];
  const navigated: string[] = [];
  const self = {
    location: { origin: "https://farm.example" },
    addEventListener: (type: string, h: Handler) => (handlers[type] = h),
    skipWaiting: () => Promise.resolve(),
    registration: {
      showNotification: (title: string, options: Record<string, unknown>) => {
        shown.push({ title, options });
        return Promise.resolve();
      },
    },
    clients: {
      claim: () => Promise.resolve(),
      openWindow: (url: string) => {
        opened.push(url);
        return Promise.resolve(null);
      },
      matchAll: () =>
        Promise.resolve(
          windows.map((w) => ({
            url: w.url,
            navigate: (u: string) => {
              navigated.push(u);
              return Promise.resolve(null);
            },
            focus: () => Promise.resolve(),
          })),
        ),
    },
  };
  const src = readFileSync(join(import.meta.dirname, "../public/sw.js"), "utf8");
  new Function("self", src)(self);
  /** Fires an event and waits for what it passed to waitUntil. */
  const fire = async (type: string, e: Record<string, unknown>) => {
    let wait: Promise<unknown> = Promise.resolve();
    handlers[type]?.({ ...e, waitUntil: (p: Promise<unknown>) => (wait = p) });
    await wait;
  };
  return { fire, shown, opened, navigated };
}

const data = (v: unknown) => ({ json: () => (typeof v === "string" ? JSON.parse(v) : v), text: () => String(v) });

describe("service worker", () => {
  it("shows a pushed alert, one per message, and keeps an urgent one on screen", async () => {
    const w = worker();
    await w.fire("push", {
      data: data({
        id: "note_1:person_1",
        title: "Heavy rain Thursday",
        body: "Open the drains.",
        severity: "urgent",
        farm: "farm_1",
      }),
    });
    expect(w.shown).toEqual([
      {
        title: "Heavy rain Thursday",
        options: expect.objectContaining({
          body: "Open the drains.",
          tag: "note_1:person_1",
          requireInteraction: true,
          data: { link: "", farm: "farm_1" },
        }),
      },
    ]);
    await w.fire("push", { data: data({ id: "n2", title: "Weeding due", body: "", severity: "advice" }) });
    expect(w.shown[1]?.options.requireInteraction).toBe(false);
  });

  it("still shows a push that is not JSON", async () => {
    const w = worker();
    await w.fire("push", { data: data("rain tomorrow") });
    expect(w.shown[0]).toMatchObject({ title: "Farm alert", options: { body: "rain tomorrow" } });
  });

  it("opens the plot when tapped, in an open window of the app if there is one", async () => {
    const close = () => {};
    const tap = (d: Record<string, string>) => ({ notification: { close, data: d } });

    const fresh = worker();
    await fresh.fire("notificationclick", tap({ farm: "farm 1", link: "" }));
    expect(fresh.opened).toEqual(["/?farm=farm%201"]);

    const open = worker([{ url: "https://farm.example/" }]);
    await open.fire("notificationclick", tap({ farm: "farm_1", link: "https://farm.example/?farm=farm_1" }));
    expect(open.navigated).toEqual(["/?farm=farm_1"]);
    expect(open.opened).toEqual([]);

    // A link to another site never opens: the plot does.
    const other = worker();
    await other.fire("notificationclick", tap({ farm: "farm_2", link: "https://evil.example/x" }));
    expect(other.opened).toEqual(["/?farm=farm_2"]);
  });
});
