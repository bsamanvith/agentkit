import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { ReportView } from "./report";
import type { Report } from "./types";

const report: Report = {
  status: "needs_review",
  measures: {
    area_m2: 4046.86,
    hectares: 0.404686,
    acres: 1,
    guntas: 40,
    cents: 100,
    acres_text: "1 acre",
    perimeter_m: 254.5,
    centroid: { lat: 17.385, lng: 78.4867 },
    corners: 4,
  },
  location: { village: "Nampally", state: "Telangana", country: "India", country_code: "IN" },
  checks: [
    { id: "location.found", severity: "info", message: "in Nampally, Telangana, India" },
    { id: "gps.accuracy", severity: "warning", message: "1 corner has a GPS fix worse than 10 m", corners: [2] },
  ],
  checked_at: "2026-10-31T06:00:00Z",
};

describe("ReportView", () => {
  it("shows the status, the sizes, where it is and the checks, warnings first", () => {
    render(<ReportView report={report} />);
    expect(screen.getByText("Needs review")).toBeInTheDocument();
    expect(screen.getByTestId("area")).toHaveTextContent("1 acre");
    expect(screen.getByText(/0\.405 ha/)).toBeInTheDocument();
    expect(screen.getByText("Nampally, Telangana, India")).toBeInTheDocument();
    const items = screen.getAllByRole("listitem");
    expect(items.map((li) => li.dataset.check)).toEqual(["gps.accuracy", "location.found"]);
    // Corners are numbered from 1 for people.
    expect(items[0]).toHaveTextContent("(corner 3)");
  });

  it("shows no sizes for an invalid shape", () => {
    render(
      <ReportView
        report={{
          status: "invalid",
          checks: [{ id: "shape.crossing", severity: "error", message: "the boundary crosses itself" }],
          checked_at: "2026-10-31T06:00:00Z",
        }}
      />,
    );
    expect(screen.getByText("Invalid")).toBeInTheDocument();
    expect(screen.queryByTestId("area")).toBeNull();
  });
});
