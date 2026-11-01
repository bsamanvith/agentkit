import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { GroupForm, GroupPage } from "./groups";
import fixture from "./testdata/kothapeta.json";
import laid from "./testdata/group.json";
import type { Distribution, DistributionResult, Farm, Group } from "./types";

const result = laid as unknown as DistributionResult;

const farm: Farm = {
  id: "farm_1",
  name: "Kothapeta paddy",
  boundary: { corners: fixture.corners },
  report: { status: "verified", checks: [], checked_at: "2026-11-01T06:00:00Z" },
  created_at: "2026-11-01T06:00:00Z",
  updated_at: "2026-11-01T06:00:00Z",
};

const crops = [
  { id: "mango", name: "Mango", perennial: true },
  { id: "tomato", name: "Tomato" },
  { id: "onion", name: "Onion" },
  { id: "green_gram", name: "Green gram" },
];

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

afterEach(() => vi.unstubAllGlobals());

function stub() {
  const calls: { method: string; url: string; body?: unknown }[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn((url: string, init?: RequestInit) => {
      const body = init?.body ? JSON.parse(init.body as string) : undefined;
      calls.push({ method: init?.method ?? "GET", url, body });
      if (url === "/api/crops") return Promise.resolve(json({ crops }));
      if (url.endsWith("/distribution/preview"))
        return Promise.resolve(
          json({ ...result, distribution: { ...result.distribution, ...(body as Distribution) } }),
        );
      if (url.endsWith("/distribution") && init?.method === "PUT")
        return Promise.resolve(json({ group: { ...group, distribution: result.distribution }, result }));
      return Promise.resolve(json({ error: "unexpected " + url }, 500));
    }),
  );
  return calls;
}

const group: Group = {
  id: "group_1",
  name: "Home farm",
  plots: ["farm_1"],
  created_at: "2026-11-01T06:00:00Z",
  updated_at: "2026-11-01T06:00:00Z",
};

describe("GroupPage", () => {
  it("starts from a template, lays it out live, shows the effects and numbers, and saves", async () => {
    const calls = stub();
    const user = userEvent.setup();
    const onGroup = vi.fn();
    render(
      <GroupPage
        group={group}
        farms={[farm]}
        planner
        onGroup={onGroup}
        onDeleted={vi.fn()}
        onEdit={vi.fn()}
        onOpenPlot={vi.fn()}
      />,
    );
    const border = await screen.findByRole("button", { name: /tree border, crops inside/i });
    await waitFor(() => expect(border).toBeEnabled());
    await user.click(border);
    // Laid out: the pieces in the picture, trees as crowns.
    await waitFor(
      () => expect(screen.getByTestId("group-picture").querySelectorAll("[data-plant=veg]").length).toBeGreaterThan(0),
      { timeout: 4000 },
    );
    const picture = screen.getByTestId("group-picture");
    expect(picture.querySelectorAll("circle.lv-pop").length).toBe(33);
    // The effects, warnings first, and each group's numbers.
    const effects = screen.getByRole("region", { name: /what this arrangement does/i });
    const items = within(effects).getAllByRole("listitem");
    expect(items[0]).toHaveAttribute("data-severity", "warning");
    expect(within(effects).getAllByText(/shade of Border trees/i).length).toBe(2);
    expect(screen.getByText("Worth a look")).toBeInTheDocument();
    const numbers = screen.getByRole("region", { name: "Numbers" });
    expect(within(numbers).getByText("5,510")).toBeInTheDocument();
    // Change the arrangement: laid out again with it.
    await user.click(screen.getByRole("radio", { name: "Alternating" }));
    await waitFor(() =>
      expect(calls.filter((c) => c.url.endsWith("/preview")).at(-1)?.body).toMatchObject({ arrange: "alternate" }),
    );
    await user.click(screen.getByRole("button", { name: /^save$/i }));
    await waitFor(() => expect(onGroup).toHaveBeenCalled());
    const put = calls.find((c) => c.method === "PUT")!;
    expect(put.url).toBe("/api/groups/group_1/distribution");
    expect((put.body as Distribution).plants.map((p) => p.id)).toEqual(["border", "veg", "pulses"]);
  });

  it("asks for plots when the group has none", () => {
    stub();
    render(
      <GroupPage
        group={{ ...group, plots: [] }}
        farms={[farm]}
        planner
        onGroup={vi.fn()}
        onDeleted={vi.fn()}
        onEdit={vi.fn()}
        onOpenPlot={vi.fn()}
      />,
    );
    expect(screen.getByRole("button", { name: /add plots/i })).toBeInTheDocument();
  });
});

describe("GroupForm", () => {
  it("names the group, picks plots, and keeps plots of other groups out", async () => {
    const calls: { url: string; body: unknown }[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn((url: string, init?: RequestInit) => {
        calls.push({ url, body: JSON.parse(init!.body as string) });
        return Promise.resolve(json({ ...group, name: "River fields", plots: ["farm_1"] }, 201));
      }),
    );
    const other: Farm = { ...farm, id: "farm_2", name: "Mango grove" };
    const user = userEvent.setup();
    const onSaved = vi.fn();
    render(
      <GroupForm
        farms={[farm, other]}
        groups={[{ ...group, id: "g0", name: "Old farm", plots: ["farm_2"] }]}
        onSaved={onSaved}
        onCancel={vi.fn()}
      />,
    );
    await user.type(screen.getByLabelText(/group name/i), "River fields");
    const boxes = screen.getAllByRole("checkbox");
    expect(boxes[1]).toBeDisabled();
    expect(screen.getByText(/in Old farm/)).toBeInTheDocument();
    await user.click(boxes[0]!);
    await user.click(screen.getByRole("button", { name: /create group/i }));
    await waitFor(() => expect(onSaved).toHaveBeenCalled());
    expect(calls[0]).toEqual({ url: "/api/groups", body: { name: "River fields", plots: ["farm_1"] } });
  });
});
