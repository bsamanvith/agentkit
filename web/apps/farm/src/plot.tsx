import { Button } from "@agentkit/ui/components/button";
import {
  ArrowLeft,
  CloudSun,
  Download,
  IndianRupee,
  Lightbulb,
  ListChecks,
  Map as MapIcon,
  Mountain,
  Pencil,
  Sprout,
  Sun as SunIcon,
  Trash2,
  Wheat,
} from "lucide-react";
import { lazy, Suspense, useState, type ReactNode } from "react";
import { api } from "./api";
import { place } from "./coords";
import { designShapes } from "./landview";
import type { ZoneShape } from "./map";
import { ReportView, StatusBadge } from "./report";
import { withHolding } from "./session";
import { units, type Farm, type Group } from "./types";

// The plot page, its tab bar and their icons: a lazy chunk, so the first
// screen (the overview, or the first run) loads without them. The tabs'
// own pages load when first opened.
const FarmMap = lazy(() => import("./map").then((m) => ({ default: m.FarmMap })));
const Planning = lazy(() => import("./planning").then((m) => ({ default: m.Planning })));
const Land = lazy(() => import("./land").then((m) => ({ default: m.Land })));
const Seasons = lazy(() => import("./seasons").then((m) => ({ default: m.Seasons })));
const Prices = lazy(() => import("./prices").then((m) => ({ default: m.Prices })));
const Roadmap = lazy(() => import("./roadmap").then((m) => ({ default: m.Roadmap })));
const Cycles = lazy(() => import("./cycles"));
const Ideas = lazy(() => import("./ideas"));
const Care = lazy(() => import("./alerts").then((m) => ({ default: m.Care })));

function message(e: unknown): string {
  return e instanceof Error ? e.message : String(e);
}

export type Tab = "map" | "cycles" | "roadmap" | "care" | "land" | "sun" | "plan" | "prices" | "ideas";

/** A plot: its map and checks, and a tab per feature that is on. */
export default function FarmDetail({
  farm,
  tab: initial,
  others,
  group,
  planner,
  cycles,
  surveyor,
  weather,
  automations,
  prices,
  ideas,
  roadmap,
  onChanged,
  onEdit,
  onOpenGroup,
  onDeleted,
}: {
  farm: Farm;
  tab?: Tab;
  others: Farm[];
  group?: Group;
  planner: boolean;
  /** Crop cycles are served: the Cycles tab. */
  cycles: boolean;
  surveyor: boolean;
  weather: boolean;
  automations: boolean;
  prices: boolean;
  /** The idea check is served: the Ideas tab. */
  ideas: boolean;
  roadmap: boolean;
  onChanged: (f: Farm) => void;
  onEdit: () => void;
  onOpenGroup: (id: string) => void;
  onDeleted: () => void;
}) {
  const [confirm, setConfirm] = useState(false);
  const [zones, setZones] = useState<ZoneShape[]>([]);
  // A plot with no plan opens on its cycles: the crop and its dates are
  // the next useful thing (basics first); one with a plan on its map.
  const planned = (farm.plan?.zones.length ?? 0) > 0;
  const [tab, setTab] = useState<Tab>(initial ?? (cycles && !planned ? "cycles" : "map"));
  const [error, setError] = useState("");
  const outlines = others
    .filter((o) => o.id !== farm.id)
    .map((o) => ({ id: o.id, name: o.name, corners: o.boundary.corners }));
  // Cycles leads for a plot that had no plan when it was opened; the order
  // stays put when "Use this" saves one, so no tab moves under a finger.
  const [cyclesFirst] = useState(!planned);
  const cyclesTab = { id: "cycles" as const, label: "Cycles", icon: <Wheat />, on: cycles };
  const tabs: { id: Tab; label: string; icon: ReactNode; on: boolean }[] = [
    ...(cyclesFirst ? [cyclesTab] : []),
    { id: "map", label: "Map", icon: <MapIcon />, on: true },
    ...(cyclesFirst ? [] : [cyclesTab]),
    { id: "roadmap", label: "Season work", icon: <ListChecks />, on: roadmap && planned },
    { id: "care", label: "Weather and care", icon: <CloudSun />, on: weather || (planner && automations) },
    { id: "land", label: "Land and layout", icon: <Mountain />, on: surveyor },
    { id: "sun", label: "Sun and seasons", icon: <SunIcon />, on: planner },
    { id: "plan", label: "Season plan", icon: <Sprout />, on: planner },
    { id: "prices", label: "Prices", icon: <IndianRupee />, on: prices },
    { id: "ideas", label: "Ideas", icon: <Lightbulb />, on: ideas },
  ];
  return (
    <>
      <div className="flex flex-wrap items-end justify-between gap-3">
        <div className="flex min-w-0 flex-col gap-1">
          {group ? (
            <button
              className="flex items-center gap-1 self-start text-xs font-medium tracking-wide text-muted-foreground uppercase hover:text-foreground"
              onClick={() => onOpenGroup(group.id)}
            >
              <ArrowLeft className="size-3" aria-hidden="true" /> {group.name}
            </button>
          ) : (
            <p className="text-xs font-medium tracking-wide text-muted-foreground uppercase">Plot</p>
          )}
          <h1 className="flex items-center gap-3 text-2xl font-semibold">
            {farm.name} <StatusBadge status={farm.report.status} />
          </h1>
          <p className="text-sm text-muted-foreground">
            {farm.report.measures?.acres_text}
            {place(farm.report.location) ? ` · ${place(farm.report.location)}` : ""}
          </p>
        </div>
        <div className="flex flex-wrap gap-2">
          <Button variant="outline" size="sm" onClick={onEdit}>
            <Pencil /> Edit
          </Button>
          <Button variant="outline" size="sm" asChild>
            <a href={withHolding(`/api/farms/${encodeURIComponent(farm.id)}/geojson`)} download>
              <Download /> GeoJSON
            </a>
          </Button>
          {confirm ? (
            <>
              <Button
                variant="destructive"
                size="sm"
                onClick={() => api.remove(farm.id).then(onDeleted, (e: unknown) => setError(message(e)))}
              >
                Delete “{farm.name}” for good
              </Button>
              <Button variant="ghost" size="sm" onClick={() => setConfirm(false)}>
                Keep it
              </Button>
            </>
          ) : (
            <Button variant="ghost" size="sm" onClick={() => setConfirm(true)} aria-label="Delete plot">
              <Trash2 />
            </Button>
          )}
        </div>
      </div>
      {error && (
        <p role="alert" className="text-sm text-ak-bad">
          {error}
        </p>
      )}
      {tabs.filter((t) => t.on).length > 1 && (
        <div
          className="sticky top-0 z-[600] -mx-4 flex gap-1 overflow-x-auto border-b bg-ak-bg/95 px-4 backdrop-blur lg:mx-0 lg:px-0"
          role="tablist"
          aria-label="About the plot"
        >
          {tabs
            .filter((t) => t.on)
            .map((t) => (
              <button
                key={t.id}
                role="tab"
                aria-selected={tab === t.id}
                onClick={() => setTab(t.id)}
                className="flex shrink-0 items-center gap-1.5 border-b-2 border-transparent px-3 py-2.5 text-sm text-muted-foreground transition-colors hover:text-foreground aria-selected:border-[#4d7c0f] aria-selected:font-medium aria-selected:text-foreground [&_svg]:size-4"
              >
                {t.icon}
                {t.label}
              </button>
            ))}
        </div>
      )}
      {/* A tab's page loads when first opened; the tab bar stays meanwhile. */}
      <Suspense fallback={<p className="text-sm text-muted-foreground">Loading…</p>}>
        {tab === "care" && <Care farm={farm} weather={weather} automations={automations} />}
        {tab === "land" && <Land farm={farm} onFarm={onChanged} />}
        {tab === "sun" && <Seasons farm={farm} />}
        {tab === "prices" && <Prices farm={farm} />}
        {tab === "ideas" && <Ideas farm={farm} />}
        {tab === "roadmap" && <Roadmap farm={farm} />}
        {tab === "cycles" && <Cycles farm={farm} onFarm={onChanged} />}
        <div className={tab === "map" ? "grid gap-4 lg:grid-cols-[minmax(0,1fr)_24rem]" : "hidden"}>
          <FarmMap
            corners={farm.boundary.corners}
            others={outlines}
            zones={[...zones, ...designShapes(farm.design)]}
            flagged={farm.report.checks.flatMap((c) => (c.severity === "info" ? [] : (c.corners ?? [])))}
            fit={1}
            className="h-[55vh] min-h-72 w-full overflow-hidden rounded-xl border shadow-sm lg:h-[70vh]"
          />
          <div className="rounded-xl border bg-ak-surface p-4 shadow-sm">
            <ReportView report={farm.report} />
            {farm.declared && (
              <p className="mt-3 text-sm text-muted-foreground">
                On papers: {farm.declared.value}{" "}
                {units.find((u) => u.id === farm.declared?.unit)?.label ?? farm.declared.unit}
              </p>
            )}
            <p className="mt-1 text-xs text-muted-foreground">
              Checked {new Date(farm.report.checked_at).toLocaleString()}
            </p>
          </div>
        </div>
        {planner && (
          <div className={tab === "plan" ? "" : "hidden"}>
            <Planning farm={farm} onFarm={onChanged} onZones={setZones} onShowMap={() => setTab("map")} />
          </div>
        )}
      </Suspense>
    </>
  );
}
