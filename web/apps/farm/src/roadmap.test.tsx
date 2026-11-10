import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { Roadmap } from "./roadmap";
import { applyQueued, enqueue, flush, queued, type Roadmap as RoadmapData, type WorkEvent } from "./roadmap-data";
import type { Farm } from "./types";

const farm: Farm = {
  id: "farm_1",
  name: "Kadapa plot",
  boundary: { corners: [] },
  report: { status: "verified", checks: [], checked_at: "2026-07-20T06:00:00Z" },
  plan: { zones: [{ crop: "groundnut", ring: [], row_bearing: 0, sow: "2026-07-01" }] },
  created_at: "2026-07-01T06:00:00Z",
  updated_at: "2026-07-01T06:00:00Z",
};

// A small roadmap, shaped like the API's answer.
function roadmap(): RoadmapData {
  return {
    farm: "farm_1",
    today: "2026-07-20",
    zones: [
      {
        zone: 0,
        crop: "groundnut",
        crop_name: "Groundnut",
        planned_sow: "2026-07-01",
        harvest_from: "2026-10-08",
        harvest_by: "2026-10-28",
      },
    ],
    tasks: [
      {
        id: "z0-groundnut-2026-07-01-sow",
        source: "calendar",
        zone: 0,
        crop: "groundnut",
        kind: "sow",
        phase: "sow",
        title: "Sow Groundnut",
        date: "2026-07-01",
        state: "open",
        overdue: true,
      },
      {
        id: "z0-groundnut-2026-07-01-weed-1",
        source: "calendar",
        zone: 0,
        crop: "groundnut",
        kind: "weed",
        phase: "grow",
        title: "First weeding",
        detail: "Hoe while weeds are small.",
        date: "2026-07-25",
        state: "open",
      },
      {
        id: "z0-groundnut-2026-07-01-harvest-by",
        source: "calendar",
        zone: 0,
        crop: "groundnut",
        kind: "harvest",
        phase: "harvest",
        title: "Harvest by now",
        date: "2026-10-28",
        state: "open",
      },
    ],
    counts: { open: 3, done: 0, skipped: 0, not_needed: 0, overdue: 1 },
  };
}

function json(body: unknown, status = 200) {
  return Promise.resolve(
    new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } }),
  );
}

beforeEach(() => localStorage.clear());
afterEach(() => vi.unstubAllGlobals());

describe("applyQueued", () => {
  it("shows queued changes before the server has them", () => {
    const evs: WorkEvent[] = [
      { id: "a", task: "z0-groundnut-2026-07-01-weed-1", kind: "done", day: "2026-07-20" },
      { id: "b", kind: "added", title: "Repair the motor", day: "2026-07-21" },
    ];
    const r = applyQueued(roadmap(), evs);
    expect(r.tasks.find((t) => t.id === "z0-groundnut-2026-07-01-weed-1")?.state).toBe("done");
    expect(r.tasks.find((t) => t.id === "b")?.title).toBe("Repair the motor");
    expect(r.tasks.map((t) => t.date)).toEqual([...r.tasks.map((t) => t.date)].sort());
  });

  it("keeps the first close, as the server does", () => {
    const r = applyQueued(roadmap(), [
      { id: "a", task: "z0-groundnut-2026-07-01-weed-1", kind: "done", day: "2026-07-20", by: "Lakshmi" },
      { id: "b", task: "z0-groundnut-2026-07-01-weed-1", kind: "skipped", day: "2026-07-20", by: "Ravi" },
    ]);
    expect(r.tasks.find((t) => t.id === "z0-groundnut-2026-07-01-weed-1")?.by).toBe("Lakshmi");
  });
});

describe("flush", () => {
  it("keeps the queue when the network fails and empties it once sent", async () => {
    enqueue("farm_1", { id: "q1", task: "z0-groundnut-2026-07-01-weed-1", kind: "done", day: "2026-07-20" });
    vi.stubGlobal(
      "fetch",
      vi.fn(() => Promise.reject(new TypeError("Failed to fetch"))),
    );
    await expect(flush("farm_1")).rejects.toThrow();
    expect(queued("farm_1")).toHaveLength(1);
    const fetch = vi.fn(() => json({ added: 1, roadmap: roadmap() }));
    vi.stubGlobal("fetch", fetch);
    await flush("farm_1");
    expect(queued("farm_1")).toHaveLength(0);
    expect(JSON.parse(String((fetch.mock.calls[0] as unknown as [string, RequestInit])[1].body))).toEqual({
      events: [{ id: "q1", task: "z0-groundnut-2026-07-01-weed-1", kind: "done", day: "2026-07-20" }],
    });
  });

  it("drops only the change the server refuses and sends the rest", async () => {
    enqueue("farm_1", { id: "good", task: "z0-groundnut-2026-07-01-weed-1", kind: "done", day: "2026-07-20" });
    enqueue("farm_1", { id: "bad", task: "gone", kind: "done", day: "2026-07-20" });
    const bodies: { events: WorkEvent[] }[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn((_url: string, init?: RequestInit) => {
        const body = JSON.parse(String(init?.body)) as { events: WorkEvent[] };
        bodies.push(body);
        if (body.events.some((e) => e.id === "bad"))
          return json({ error: "task gone is not on the plot's roadmap", event_id: "bad", index: 1 }, 400);
        return json({ added: 1, roadmap: roadmap() });
      }),
    );
    await expect(flush("farm_1")).rejects.toThrow("not on the plot's roadmap");
    expect(queued("farm_1")).toHaveLength(0);
    expect(bodies.map((b) => b.events.map((e) => e.id))).toEqual([["good", "bad"], ["good"]]);
  });

  it("sends a long queue in chunks and keeps it when the server is busy", async () => {
    for (let i = 0; i < 450; i++) enqueue("farm_1", { id: `e${i}`, kind: "added", title: "t", day: "2026-07-20" });
    vi.stubGlobal(
      "fetch",
      vi.fn(() => json({ error: "slow down" }, 429)),
    );
    await expect(flush("farm_1")).rejects.toThrow("slow down");
    expect(queued("farm_1")).toHaveLength(450);
    const sizes: number[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn((_url: string, init?: RequestInit) => {
        sizes.push((JSON.parse(String(init?.body)) as { events: unknown[] }).events.length);
        return json({ added: 1, roadmap: roadmap() });
      }),
    );
    await flush("farm_1");
    expect(sizes).toEqual([200, 200, 50]);
    expect(queued("farm_1")).toHaveLength(0);
  });
});

describe("Roadmap", () => {
  it("lists the season, marks the sowing done with its day, and sends it", async () => {
    const posted: unknown[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn((url: string, init?: RequestInit) => {
        if (init?.method === "POST") {
          posted.push(JSON.parse(String(init.body)));
          const r = roadmap();
          Object.assign(r.tasks[0]!, { state: "done", day: "2026-07-03", overdue: false });
          Object.assign(r.zones[0]!, { sown: "2026-07-03" });
          return json({ added: 1, roadmap: r });
        }
        expect(url).toBe("/api/farms/farm_1/roadmap");
        return json(roadmap());
      }),
    );
    render(<Roadmap farm={farm} />);
    expect(await screen.findByText("Sow Groundnut")).toBeInTheDocument();
    expect(screen.getByText("First weeding")).toBeInTheDocument();
    expect(screen.queryByText("Harvest by now")).not.toBeInTheDocument(); // months away
    expect(screen.getByText(/Past its date/)).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: /Sown/ }));
    const day = screen.getByLabelText("Sown on");
    await userEvent.clear(day);
    await userEvent.type(day, "2026-07-03");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(posted).toHaveLength(1));
    const sent = (posted[0] as { events: WorkEvent[] }).events[0]!;
    expect(sent).toMatchObject({ task: "z0-groundnut-2026-07-01-sow", kind: "done", day: "2026-07-03" });
    expect(sent.id).toMatch(/^web-/);
    expect(await screen.findByText(/sown/)).toBeInTheDocument();
    expect(queued("farm_1")).toHaveLength(0);

    await userEvent.click(screen.getByRole("button", { name: "Whole season" }));
    expect(screen.getByText("Harvest by now")).toBeInTheDocument();
  });

  it("keeps a change on the phone when there is no connection", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn((_url: string, init?: RequestInit) =>
        init?.method === "POST" ? Promise.reject(new TypeError("offline")) : json(roadmap()),
      ),
    );
    render(<Roadmap farm={farm} />);
    await userEvent.click(await screen.findByRole("button", { name: /^Done/ }));
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(await screen.findByText(/1 change saved on this phone, will send when online/)).toBeInTheDocument();
    expect(queued("farm_1")).toHaveLength(1);
    expect(screen.getByText("First weeding")).toHaveClass("line-through");
  });
});
