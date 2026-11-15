import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { areaM2, plane } from "./draw";
import { Editor } from "./editor";
import type { Handle, Stroke } from "./map";
import type { Corner, Point } from "./types";

interface MapProps {
  goto?: Point;
  corners: Corner[];
  stroke?: Stroke;
  taps?: boolean;
  onStroke?: (pts: Point[]) => void;
  onChange?: (c: Corner[]) => void;
  handles?: Handle[];
  onHandle?: (id: string, at: Point) => void;
}

// The map is Leaflet, which jsdom cannot draw: a stand-in shows where the
// editor asked it to go, and keeps its props so a test can draw on it as
// the farmer's finger would.
const map = vi.hoisted(() => ({ props: undefined as unknown as MapProps }));
vi.mock("./map", () => ({
  FarmMap: (p: MapProps) => {
    map.props = p;
    return <div data-testid="map">{p.goto ? `${p.goto.lat},${p.goto.lng}` : "India"}</div>;
  },
}));

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

const lalapet = [
  {
    name: "Lalapet",
    kind: "village",
    point: { lat: 12.99, lng: 79.3 },
    address: "Lalapet, Wallajah, Ranipet, Tamil Nadu, India",
  },
  {
    name: "Lalapet",
    kind: "suburb",
    point: { lat: 16.29, lng: 80.45 },
    address: "Lalapet, Guntur Mandal, Guntur District, Andhra Pradesh, India",
  },
];

afterEach(() => vi.unstubAllGlobals());

describe("Editor", () => {
  it("finds the farmer's village and moves the map there", async () => {
    const user = userEvent.setup();
    const fetch = vi.fn(async (url: string) =>
      String(url).startsWith("/api/places") ? json({ places: lalapet }) : json({}),
    );
    vi.stubGlobal("fetch", fetch);
    render(<Editor others={[]} onSaved={() => {}} onCancel={() => {}} />);
    expect(screen.getByTestId("map")).toHaveTextContent("India");

    await user.type(screen.getByRole("textbox", { name: "Village or town" }), "Lalapet");
    await user.click(screen.getByRole("button", { name: /find/i }));
    expect(String(fetch.mock.calls[0]?.[0])).toBe("/api/places?q=Lalapet&limit=5");
    // Two places by that name: the farmer picks theirs by its district.
    await user.click(await screen.findByRole("button", { name: /Guntur District/ }));
    expect(screen.getByTestId("map")).toHaveTextContent("16.29,80.45");
    expect(screen.queryByRole("list", { name: "Places found" })).not.toBeInTheDocument();
  });

  it("says when no place has that name", async () => {
    const user = userEvent.setup();
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => json({ places: [] })),
    );
    render(<Editor others={[]} onSaved={() => {}} onCancel={() => {}} />);
    await user.type(screen.getByRole("textbox", { name: "Village or town" }), "Nowhereville{Enter}");
    expect(await screen.findByText(/No place by that name/)).toBeInTheDocument();
    expect(screen.getByTestId("map")).toHaveTextContent("India");
  });

  it("says when the size on the papers is not a number it can check", async () => {
    const user = userEvent.setup();
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => json({})),
    );
    render(<Editor others={[]} onSaved={() => {}} onCancel={() => {}} />);
    const size = screen.getByRole("textbox", { name: /size on your papers/i });
    await user.type(size, "2 acre 10 gunta");
    expect(screen.getByRole("status")).toHaveTextContent(/Type just the number/);
    await user.clear(size);
    await user.type(size, "2.5");
    expect(screen.queryByText(/Type just the number/)).not.toBeInTheDocument();
  });
});

// A field near Guntur, in metres east and north of its corner.
const pl = plane({ lat: 16.29, lng: 80.45 });
const at = (x: number, y: number) => pl.point({ x, y });

const verified = {
  status: "verified",
  measures: {
    area_m2: 2400,
    hectares: 0.24,
    acres: 0.59,
    guntas: 23.7,
    cents: 59.3,
    acres_text: "0.59 acre",
    perimeter_m: 220,
    centroid: { lat: 16.2902, lng: 80.4501 },
    corners: 4,
  },
  checks: [],
  checked_at: "2026-11-15T06:00:00Z",
};

/** A server that verifies whatever it is sent, and records the boundaries. */
function verifier() {
  const sent: Corner[][] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url: string, init?: RequestInit) => {
      if (String(url) === "/api/verify") sent.push(JSON.parse(String(init?.body)).boundary.corners);
      return json(verified);
    }),
  );
  return sent;
}

describe("Editor drawing tools", () => {
  it("starts a new plot with the pencil, whose drag draws and does not add corners by tapping", () => {
    verifier();
    render(<Editor others={[]} onSaved={() => {}} onCancel={() => {}} />);
    expect(screen.getByRole("button", { name: /pencil/i })).toHaveAttribute("aria-pressed", "true");
    expect(map.props.stroke).toBe("free");
    expect(map.props.taps).toBe(false);
  });

  it("turns a freehand loop into a few corners and sends them to be verified as drawn on the map", async () => {
    const sent = verifier();
    render(<Editor others={[]} onSaved={() => {}} onCancel={() => {}} />);
    const loop: Point[] = [];
    for (let x = 0; x <= 60; x++) loop.push(at(x, 0));
    for (let y = 0; y <= 40; y++) loop.push(at(60, y));
    for (let x = 60; x >= 0; x--) loop.push(at(x, 40));
    for (let y = 40; y >= 2; y--) loop.push(at(0, y));
    act(() => map.props.onStroke?.(loop));
    expect(map.props.corners).toHaveLength(4);
    await waitFor(() => expect(sent).toHaveLength(1));
    expect(sent[0]).toEqual(map.props.corners);
  });

  it("drags a rectangle, turns and stretches it by its handles, and undoes step by step", async () => {
    verifier();
    const user = userEvent.setup();
    render(<Editor others={[]} onSaved={() => {}} onCancel={() => {}} />);
    act(() => screen.getByRole("button", { name: /rectangle/i }).click());
    expect(map.props.stroke).toBe("box");
    act(() => map.props.onStroke?.([at(0, 0), at(30, 80)]));
    const drawn = map.props.corners;
    expect(drawn).toHaveLength(4);
    expect(areaM2(drawn)).toBeCloseTo(2400, -1);
    expect(map.props.handles?.map((h) => h.id).sort()).toEqual(["length", "turn", "width"]);

    // Turn it to face east: the same size, other corners.
    act(() => map.props.onHandle?.("turn", at(100, 40)));
    act(() => map.props.onHandle?.("turn", at(100, 41)));
    const turned = map.props.corners;
    expect(areaM2(turned)).toBeCloseTo(2400, -1);
    expect(turned).not.toEqual(drawn);
    // Stretch its width.
    act(() => map.props.onHandle?.("width", at(15, -20)));
    expect(areaM2(map.props.corners)).toBeGreaterThan(2500);

    // One undo per drag: back to turned, then to drawn, then to nothing.
    await user.click(screen.getByRole("button", { name: /undo/i }));
    expect(map.props.corners).toEqual(turned);
    await user.click(screen.getByRole("button", { name: /undo/i }));
    expect(map.props.corners).toEqual(drawn);
    await user.click(screen.getByRole("button", { name: /undo/i }));
    expect(map.props.corners).toEqual([]);
    expect(screen.getByRole("button", { name: /undo/i })).toBeDisabled();
  });

  it("leaves the rectangle as plain corners once a corner is dragged", () => {
    verifier();
    render(<Editor others={[]} onSaved={() => {}} onCancel={() => {}} />);
    act(() => screen.getByRole("button", { name: /rectangle/i }).click());
    act(() => map.props.onStroke?.([at(0, 0), at(30, 80)]));
    const moved = map.props.corners.map((c, i) => (i === 0 ? at(-5, 85) : c));
    act(() => map.props.onChange?.(moved));
    expect(map.props.corners).toEqual(moved);
    expect(map.props.handles).toEqual([]);
  });

  it("adds a corner per tap with lines, and lets one finger move the map when asked", () => {
    verifier();
    render(<Editor others={[]} onSaved={() => {}} onCancel={() => {}} />);
    act(() => screen.getByRole("button", { name: /lines/i }).click());
    expect(map.props.stroke).toBeUndefined();
    expect(map.props.taps).toBe(true);
    act(() => screen.getByRole("button", { name: /pencil/i }).click());
    act(() => screen.getByRole("button", { name: /move map/i }).click());
    expect(map.props.stroke).toBeUndefined();
    expect(map.props.taps).toBe(false);
  });

  it("clears the boundary, and undo brings it back", async () => {
    verifier();
    const user = userEvent.setup();
    render(<Editor others={[]} onSaved={() => {}} onCancel={() => {}} />);
    act(() => screen.getByRole("button", { name: /rectangle/i }).click());
    act(() => map.props.onStroke?.([at(0, 0), at(30, 80)]));
    const drawn = map.props.corners;
    await user.click(screen.getByRole("button", { name: /clear/i }));
    expect(map.props.corners).toEqual([]);
    await user.click(screen.getByRole("button", { name: /undo/i }));
    expect(map.props.corners).toEqual(drawn);
  });
});
