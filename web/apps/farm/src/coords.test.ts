import { describe, expect, it } from "vitest";
import { formatCorners, parseCorners, place } from "./coords";

describe("parseCorners", () => {
  it("reads decimal degrees in the usual separators", () => {
    const p = parseCorners("17.385, 78.4867\n17.385;78.4873\n\n# a note\n17.38557 78.4873\n17.38557\t78.4867");
    expect(p.errors).toEqual([]);
    expect(p.corners).toEqual([
      { lat: 17.385, lng: 78.4867 },
      { lat: 17.385, lng: 78.4873 },
      { lat: 17.38557, lng: 78.4873 },
      { lat: 17.38557, lng: 78.4867 },
    ]);
  });

  it("reads degrees, minutes and seconds with hemispheres", () => {
    const p = parseCorners(`17°23'6"N, 78°29'12"E\n33 52 0 S, 151 12 30 E\n78.5E, 17.5N`);
    expect(p.errors).toEqual([]);
    expect(p.corners[0]?.lat).toBeCloseTo(17.385, 6);
    expect(p.corners[0]?.lng).toBeCloseTo(78.486667, 5);
    expect(p.corners[1]?.lat).toBeCloseTo(-33.866667, 5);
    expect(p.corners[1]?.lng).toBeCloseTo(151.208333, 5);
    // Marked longitude first: swapped into place.
    expect(p.corners[2]).toEqual({ lat: 17.5, lng: 78.5 });
  });

  it("names the lines it cannot read", () => {
    const p = parseCorners("17.385, 78.4867\nnorth corner\n97, 78\n1, 2, 3");
    expect(p.corners).toHaveLength(1);
    expect(p.errors.map((e) => e.line)).toEqual([2, 3, 4]);
  });

  it("reads back what formatCorners writes", () => {
    const corners = [
      { lat: 17.385, lng: 78.4867 },
      { lat: -0.000123, lng: -179.5 },
    ];
    expect(parseCorners(formatCorners(corners)).corners).toEqual(corners);
  });
});

describe("place", () => {
  it("joins the location from the village up, without repeats", () => {
    expect(place({ village: "Nampally", district: "Hyderabad", state: "Telangana", country: "India" })).toBe(
      "Nampally, Hyderabad, Telangana, India",
    );
    expect(place({ district: "Hyderabad", sub_district: "Hyderabad", country: "India" })).toBe("Hyderabad, India");
    expect(place({ display_name: "Somewhere" })).toBe("Somewhere");
    expect(place(undefined)).toBe("");
  });
});
