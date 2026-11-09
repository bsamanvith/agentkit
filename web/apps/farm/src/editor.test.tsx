import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { Editor } from "./editor";
import type { Point } from "./types";

// The map is Leaflet, which jsdom cannot draw: a stand-in shows where the
// editor asked it to go.
vi.mock("./map", () => ({
  FarmMap: ({ goto }: { goto?: Point }) => <div data-testid="map">{goto ? `${goto.lat},${goto.lng}` : "India"}</div>,
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
