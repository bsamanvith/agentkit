import { Button } from "@agentkit/ui/components/button";
import {
  ArrowLeft,
  ChevronRight,
  Download,
  FolderPlus,
  Globe,
  LayoutGrid,
  Map as MapIcon,
  Menu,
  MessageCircle,
  Mountain,
  Pencil,
  Plus,
  Sprout,
  Trash2,
  Wheat,
  X,
} from "lucide-react";
import { lazy, Suspense, useCallback, useEffect, useState, type ReactNode } from "react";
import { api } from "./api";
import { place } from "./coords";
import { acresOf, plantColor, plantColors, plotsOf } from "./distribution";
import { designShapes } from "./landview";
import { ReportView, StatusBadge } from "./report";
import { PlotShape } from "./shape";
import type { ZoneShape } from "./map";
import type { Config, Farm, Group } from "./types";

// The map (Leaflet), the group pages and the assistant load when first
// needed: the overview comes up first on a slow connection.
const Editor = lazy(() => import("./editor").then((m) => ({ default: m.Editor })));
const FarmMap = lazy(() => import("./map").then((m) => ({ default: m.FarmMap })));
const Planning = lazy(() => import("./planning").then((m) => ({ default: m.Planning })));
const Land = lazy(() => import("./land").then((m) => ({ default: m.Land })));
const GroupPage = lazy(() => import("./groups").then((m) => ({ default: m.GroupPage })));
const GroupForm = lazy(() => import("./groups").then((m) => ({ default: m.GroupForm })));
const ResearchSettings = lazy(() => import("./research").then((m) => ({ default: m.ResearchSettings })));
const Assistant = lazy(() => import("./assistant").then((m) => ({ default: m.Assistant })));

const loading = <p className="text-sm text-muted-foreground">Loading…</p>;

type View =
  | { kind: "home" }
  | { kind: "edit"; farm?: Farm }
  | { kind: "farm"; id: string }
  | { kind: "group"; id: string }
  | { kind: "group-form"; group?: Group };

function message(e: unknown): string {
  return e instanceof Error ? e.message : String(e);
}

/** A group's colour on the overview: by its place in the list. */
function groupColor(groups: Group[], id: string): string {
  return (
    plantColors[
      Math.max(
        0,
        groups.findIndex((g) => g.id === id),
      ) % plantColors.length
    ] ?? "#4d7c0f"
  );
}

export function App() {
  const [farms, setFarms] = useState<Farm[]>();
  const [groups, setGroups] = useState<Group[]>([]);
  const [config, setConfig] = useState<Config>();
  const [view, setView] = useState<View>({ kind: "home" });
  const [chat, setChat] = useState(false);
  const [drawer, setDrawer] = useState(false);
  const [webSettings, setWebSettings] = useState(false);
  const [error, setError] = useState("");

  const load = useCallback(() => {
    api.farms().then(setFarms, (e: unknown) => setError(message(e)));
    api.groups().then(setGroups, () => setGroups([]));
  }, []);
  useEffect(() => {
    load();
    api.config().then(setConfig, () => setConfig({ assistant: false, country: "" }));
  }, [load]);

  const go = (v: View) => {
    setView(v);
    setDrawer(false);
    document.getElementById("main")?.scrollTo({ top: 0 });
  };
  const current = view.kind === "farm" ? farms?.find((f) => f.id === view.id) : undefined;
  const group = view.kind === "group" ? groups.find((g) => g.id === view.id) : undefined;
  const setGroup = (g: Group) =>
    setGroups((gs) => (gs.some((x) => x.id === g.id) ? gs.map((x) => (x.id === g.id ? g : x)) : [...gs, g]));

  const nav = (
    <Sidebar
      farms={farms ?? []}
      groups={groups}
      view={view}
      onGo={go}
      onClose={drawer ? () => setDrawer(false) : undefined}
    />
  );

  return (
    <div className="flex h-dvh bg-ak-bg">
      <div className="hidden w-72 shrink-0 border-r bg-ak-surface lg:flex">{nav}</div>
      {drawer && (
        <div className="fixed inset-0 z-[1100] flex lg:hidden">
          <div className="w-80 max-w-[85vw] bg-ak-surface shadow-xl">{nav}</div>
          <button className="flex-1 bg-black/40" aria-label="Close the menu" onClick={() => setDrawer(false)} />
        </div>
      )}
      <div className="flex min-w-0 flex-1 flex-col">
        <header className="flex items-center gap-2 border-b bg-ak-surface/95 px-3 py-2 backdrop-blur lg:px-6">
          <Button variant="ghost" size="sm" className="lg:hidden" aria-label="Menu" onClick={() => setDrawer(true)}>
            <Menu />
          </Button>
          <button className="flex items-center gap-2 font-semibold lg:hidden" onClick={() => go({ kind: "home" })}>
            <Sprout className="size-5 text-ak-ok" aria-hidden="true" /> My land
          </button>
          <Crumbs view={view} farms={farms ?? []} groups={groups} onGo={go} />
          <div className="ml-auto flex gap-1.5">
            {config?.research && (
              <Button variant="ghost" size="sm" onClick={() => setWebSettings(true)} aria-label="Web research">
                <Globe /> <span className="hidden sm:inline">Web research</span>
              </Button>
            )}
            {config?.assistant && (
              <Button variant="outline" size="sm" onClick={() => setChat((c) => !c)} aria-pressed={chat}>
                <MessageCircle /> <span className="hidden sm:inline">Assistant</span>
              </Button>
            )}
          </div>
        </header>
        <main id="main" className="flex-1 overflow-y-auto">
          <div className="mx-auto flex w-full max-w-7xl flex-col gap-5 p-4 lg:p-6">
            <Suspense fallback={loading}>
              {error && (
                <p role="alert" className="text-sm text-ak-bad">
                  {error}
                </p>
              )}
              {view.kind === "home" && (
                <Overview
                  farms={farms}
                  groups={groups}
                  onOpen={(id) => go({ kind: "farm", id })}
                  onOpenGroup={(id) => go({ kind: "group", id })}
                  onAdd={() => go({ kind: "edit" })}
                  onAddGroup={() => go({ kind: "group-form" })}
                />
              )}
              {view.kind === "edit" && (
                <>
                  <h1 className="text-2xl font-semibold">{view.farm ? `Edit ${view.farm.name}` : "Add a plot"}</h1>
                  <Editor
                    farm={view.farm}
                    others={farms ?? []}
                    onCancel={() => go(view.farm ? { kind: "farm", id: view.farm.id } : { kind: "home" })}
                    onSaved={(f) => {
                      setFarms((fs) => [...(fs ?? []).filter((x) => x.id !== f.id), f]);
                      go({ kind: "farm", id: f.id });
                    }}
                  />
                </>
              )}
              {view.kind === "group-form" && (
                <>
                  <h1 className="text-2xl font-semibold">
                    {view.group ? `${view.group.name}: name and plots` : "New plot group"}
                  </h1>
                  {!view.group && (
                    <p className="-mt-3 max-w-2xl text-sm text-muted-foreground">
                      Keep plots you farm together in a group: the home farm, the fields by the river. Each group has
                      its own plant groups and the way they are spread over its plots.
                    </p>
                  )}
                  <GroupForm
                    group={view.group}
                    farms={farms ?? []}
                    groups={groups}
                    onCancel={() => go(view.group ? { kind: "group", id: view.group.id } : { kind: "home" })}
                    onSaved={(g) => {
                      setGroup(g);
                      go({ kind: "group", id: g.id });
                    }}
                  />
                </>
              )}
              {view.kind === "group" && group && (
                <GroupPage
                  key={group.id}
                  group={group}
                  farms={farms ?? []}
                  planner={config?.planner ?? false}
                  onGroup={setGroup}
                  onEdit={() => go({ kind: "group-form", group })}
                  onOpenPlot={(id) => go({ kind: "farm", id })}
                  onDeleted={() => {
                    setGroups((gs) => gs.filter((g) => g.id !== group.id));
                    go({ kind: "home" });
                  }}
                />
              )}
              {view.kind === "farm" && current && (
                <FarmDetail
                  key={current.id}
                  farm={current}
                  others={farms ?? []}
                  group={groups.find((g) => g.plots.includes(current.id))}
                  planner={config?.planner ?? false}
                  surveyor={config?.surveyor ?? false}
                  onChanged={(f) => setFarms((fs) => fs?.map((x) => (x.id === f.id ? f : x)))}
                  onEdit={() => go({ kind: "edit", farm: current })}
                  onOpenGroup={(id) => go({ kind: "group", id })}
                  onDeleted={() => {
                    setFarms((fs) => fs?.filter((x) => x.id !== current.id));
                    setGroups((gs) => gs.map((g) => ({ ...g, plots: g.plots.filter((p) => p !== current.id) })));
                    go({ kind: "home" });
                  }}
                />
              )}
            </Suspense>
          </div>
        </main>
      </div>
      {webSettings && (
        <Suspense fallback={null}>
          <ResearchSettings onClose={() => setWebSettings(false)} />
        </Suspense>
      )}
      {chat && (
        <div className="fixed inset-0 z-[1000] sm:static sm:z-auto sm:w-96 sm:border-l">
          <Suspense fallback={loading}>
            <Assistant
              onClose={() => {
                setChat(false);
                load(); // the assistant may have saved a group or a layout
              }}
            />
          </Suspense>
        </div>
      )}
    </div>
  );
}

function Crumbs({
  view,
  farms,
  groups,
  onGo,
}: {
  view: View;
  farms: Farm[];
  groups: Group[];
  onGo: (v: View) => void;
}) {
  const items: { label: string; to?: View }[] = [{ label: "Overview", to: { kind: "home" } }];
  if (view.kind === "farm") {
    const f = farms.find((x) => x.id === view.id);
    const g = groups.find((x) => x.plots.includes(view.id));
    if (g) items.push({ label: g.name, to: { kind: "group", id: g.id } });
    items.push({ label: f?.name ?? "Plot" });
  }
  if (view.kind === "group") items.push({ label: groups.find((x) => x.id === view.id)?.name ?? "Group" });
  if (view.kind === "group-form") items.push({ label: view.group ? view.group.name : "New group" });
  if (view.kind === "edit") items.push({ label: view.farm ? view.farm.name : "New plot" });
  if (items.length === 1) return null;
  return (
    <nav aria-label="Where you are" className="hidden min-w-0 items-center gap-1 text-sm sm:flex">
      {items.map((it, i) => (
        <span key={i} className="flex min-w-0 items-center gap-1">
          {i > 0 && <ChevronRight className="size-3.5 shrink-0 text-muted-foreground" aria-hidden="true" />}
          {it.to && i < items.length - 1 ? (
            <button
              className="truncate text-muted-foreground hover:text-foreground"
              onClick={() => it.to && onGo(it.to)}
            >
              {it.label}
            </button>
          ) : (
            <span className="truncate font-medium">{it.label}</span>
          )}
        </span>
      ))}
    </nav>
  );
}

function Sidebar({
  farms,
  groups,
  view,
  onGo,
  onClose,
}: {
  farms: Farm[];
  groups: Group[];
  view: View;
  onGo: (v: View) => void;
  onClose?: () => void;
}) {
  const grouped = new Set(groups.flatMap((g) => g.plots));
  const loose = farms.filter((f) => !grouped.has(f.id));
  const at = (v: View) =>
    v.kind === view.kind && ("id" in v && "id" in view ? v.id === view.id : !("id" in v)) ? "page" : undefined;
  const item =
    "flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left text-sm hover:bg-ak-hover aria-[current=page]:bg-ak-select aria-[current=page]:font-medium";
  const plotItem = (f: Farm, color: string) => (
    <li key={f.id}>
      <button
        className={item}
        aria-current={at({ kind: "farm", id: f.id })}
        onClick={() => onGo({ kind: "farm", id: f.id })}
      >
        <PlotShape farms={[f]} color={color} className="size-5 shrink-0" />
        <span className="truncate">{f.name}</span>
        <span className="ml-auto shrink-0 text-xs text-muted-foreground tabular-nums">
          {f.report.measures?.acres.toFixed(1)}
        </span>
      </button>
    </li>
  );
  return (
    <nav aria-label="Your land" className="flex w-full flex-col">
      <div className="flex items-center justify-between px-4 py-3">
        <button className="flex items-center gap-2 text-base font-semibold" onClick={() => onGo({ kind: "home" })}>
          <span className="flex size-7 items-center justify-center rounded-lg bg-[#3f6212] text-white">
            <Sprout className="size-4" aria-hidden="true" />
          </span>
          My land
        </button>
        {onClose && (
          <Button variant="ghost" size="sm" aria-label="Close the menu" onClick={onClose}>
            <X />
          </Button>
        )}
      </div>
      <div className="flex-1 overflow-y-auto px-2 pb-4">
        <button className={item} aria-current={at({ kind: "home" })} onClick={() => onGo({ kind: "home" })}>
          <LayoutGrid className="size-4 text-muted-foreground" aria-hidden="true" /> Overview
        </button>
        <div className="mt-4 flex items-center justify-between px-2">
          <h2 className="text-xs font-medium tracking-wide text-muted-foreground uppercase">Groups</h2>
          <Button
            variant="ghost"
            size="sm"
            className="h-6 px-1.5"
            aria-label="New group"
            onClick={() => onGo({ kind: "group-form" })}
          >
            <FolderPlus className="size-3.5" />
          </Button>
        </div>
        {groups.length === 0 && <p className="px-2 py-1 text-xs text-muted-foreground">No groups yet.</p>}
        <ul className="flex flex-col gap-0.5">
          {groups.map((g) => {
            const color = groupColor(groups, g.id);
            return (
              <li key={g.id}>
                <button
                  className={item}
                  aria-current={at({ kind: "group", id: g.id })}
                  onClick={() => onGo({ kind: "group", id: g.id })}
                >
                  <span className="size-2.5 shrink-0 rounded-full" style={{ background: color }} aria-hidden="true" />
                  <span className="truncate">{g.name}</span>
                  <span className="ml-auto text-xs text-muted-foreground">{g.plots.length}</span>
                </button>
                {g.plots.length > 0 && (
                  <ul className="ml-3 border-l pl-1">{plotsOf(g, farms).map((f) => plotItem(f, color))}</ul>
                )}
              </li>
            );
          })}
        </ul>
        {loose.length > 0 && (
          <>
            <h2 className="mt-4 px-2 text-xs font-medium tracking-wide text-muted-foreground uppercase">
              {groups.length > 0 ? "Not in a group" : "Plots"}
            </h2>
            <ul className="flex flex-col gap-0.5">{loose.map((f) => plotItem(f, "#57534e"))}</ul>
          </>
        )}
      </div>
      <div className="border-t p-3">
        <Button className="w-full" size="sm" onClick={() => onGo({ kind: "edit" })}>
          <Plus /> Add plot
        </Button>
      </div>
    </nav>
  );
}

function Stat({ label, value, icon }: { label: string; value: string; icon: ReactNode }) {
  return (
    <div className="flex flex-col gap-2 rounded-xl border bg-ak-surface p-3 shadow-sm sm:flex-row sm:items-center sm:gap-3 sm:p-4">
      <span className="flex size-8 items-center justify-center rounded-lg bg-[#3f6212]/10 text-[#3f6212] sm:size-10 dark:text-[#bef264]">
        {icon}
      </span>
      <div>
        <p className="text-xl font-semibold tabular-nums sm:text-2xl">{value}</p>
        <p className="text-xs text-muted-foreground">{label}</p>
      </div>
    </div>
  );
}

function Overview({
  farms,
  groups,
  onOpen,
  onOpenGroup,
  onAdd,
  onAddGroup,
}: {
  farms?: Farm[];
  groups: Group[];
  onOpen: (id: string) => void;
  onOpenGroup: (id: string) => void;
  onAdd: () => void;
  onAddGroup: () => void;
}) {
  if (!farms) return loading;
  if (farms.length === 0)
    return (
      <div className="flex flex-col items-center gap-4 rounded-2xl border bg-ak-surface px-6 py-16 text-center shadow-sm">
        <span className="flex size-14 items-center justify-center rounded-2xl bg-[#3f6212]/10">
          <Sprout className="size-8 text-ak-ok" aria-hidden="true" />
        </span>
        <h1 className="text-2xl font-semibold">Add your first plot</h1>
        <p className="max-w-sm text-sm text-muted-foreground">
          Draw its corners on the satellite map, walk around it with your phone&apos;s GPS, or type the coordinates. We
          measure it and check that it looks right.
        </p>
        <Button onClick={onAdd}>
          <Plus /> Add plot
        </Button>
      </div>
    );
  const grouped = new Set(groups.flatMap((g) => g.plots));
  const loose = farms.filter((f) => !grouped.has(f.id));
  return (
    <>
      <div className="flex flex-wrap items-end justify-between gap-3">
        <div>
          <h1 className="text-2xl font-semibold">Your land</h1>
          <p className="text-sm text-muted-foreground">Plots, the groups you keep them in, and what grows where.</p>
        </div>
        <div className="flex gap-2">
          <Button variant="outline" size="sm" onClick={onAddGroup}>
            <FolderPlus /> New group
          </Button>
          <Button size="sm" onClick={onAdd}>
            <Plus /> Add plot
          </Button>
        </div>
      </div>
      <div className="grid grid-cols-3 gap-2 sm:gap-3">
        <Stat
          label={farms.length === 1 ? "plot" : "plots"}
          value={String(farms.length)}
          icon={<MapIcon className="size-5" />}
        />
        <Stat label="acres in all" value={acresOf(farms).toFixed(2)} icon={<Wheat className="size-5" />} />
        <Stat
          label={groups.length === 1 ? "group" : "groups"}
          value={String(groups.length)}
          icon={<LayoutGrid className="size-5" />}
        />
      </div>

      <section aria-label="Groups" className="flex flex-col gap-3">
        <h2 className="text-lg font-semibold">Groups</h2>
        <ul className="grid gap-3 sm:grid-cols-2 xl:grid-cols-3">
          {groups.map((g) => {
            const plots = plotsOf(g, farms);
            const color = groupColor(groups, g.id);
            return (
              <li key={g.id}>
                <button
                  onClick={() => onOpenGroup(g.id)}
                  className="group flex h-full w-full flex-col overflow-hidden rounded-xl border bg-ak-surface text-left shadow-sm transition hover:-translate-y-0.5 hover:shadow-md"
                >
                  <div className="flex h-32 items-center justify-center bg-[#eef2e6] p-3 dark:bg-[#1b2117]">
                    {plots.length > 0 ? (
                      <PlotShape farms={plots} color={color} className="h-full w-full" />
                    ) : (
                      <span className="text-sm text-muted-foreground">No plots yet</span>
                    )}
                  </div>
                  <div className="flex flex-1 flex-col gap-1.5 p-4">
                    <span className="flex items-center gap-2 font-medium">
                      <span className="size-2.5 rounded-full" style={{ background: color }} aria-hidden="true" />
                      {g.name}
                      <ChevronRight
                        className="ml-auto size-4 text-muted-foreground transition group-hover:translate-x-0.5"
                        aria-hidden="true"
                      />
                    </span>
                    <span className="text-sm text-muted-foreground">
                      {plots.length} {plots.length === 1 ? "plot" : "plots"} · {acresOf(plots).toFixed(2)} acres
                    </span>
                    {g.distribution ? (
                      <span className="flex flex-wrap gap-1.5 pt-1">
                        {g.distribution.plants.map((p) => (
                          <span
                            key={p.id}
                            className="flex items-center gap-1 rounded-full bg-ak-bg px-2 py-0.5 text-xs"
                          >
                            <span
                              className="size-2 rounded-full"
                              style={{ background: plantColor(g.distribution, p.id) }}
                              aria-hidden="true"
                            />
                            {p.name}
                          </span>
                        ))}
                      </span>
                    ) : (
                      <span className="pt-1 text-xs text-muted-foreground">No plant groups yet</span>
                    )}
                  </div>
                </button>
              </li>
            );
          })}
          <li>
            <button
              onClick={onAddGroup}
              className="flex h-full min-h-40 w-full flex-col items-center justify-center gap-2 rounded-xl border border-dashed p-6 text-sm text-muted-foreground transition hover:border-ak-ok/60 hover:text-foreground"
            >
              <FolderPlus className="size-6" aria-hidden="true" />
              New group
              <span className="max-w-56 text-center text-xs">
                Keep plots together and plan what grows where across them.
              </span>
            </button>
          </li>
        </ul>
      </section>

      {loose.length > 0 && (
        <section aria-label="Plots" className="flex flex-col gap-3">
          <h2 className="text-lg font-semibold">{groups.length > 0 ? "Plots not in a group" : "Plots"}</h2>
          <ul className="grid gap-3 sm:grid-cols-2 xl:grid-cols-3">
            {loose.map((f) => (
              <li key={f.id}>
                <FarmCard farm={f} onOpen={() => onOpen(f.id)} />
              </li>
            ))}
          </ul>
        </section>
      )}
    </>
  );
}

function FarmCard({ farm: f, onOpen }: { farm: Farm; onOpen: () => void }) {
  return (
    <button
      onClick={onOpen}
      className="flex w-full items-center gap-3 rounded-xl border bg-ak-surface p-3 text-left shadow-sm transition hover:-translate-y-0.5 hover:shadow-md"
    >
      <PlotShape farms={[f]} className="size-16 shrink-0 rounded-lg bg-[#eef2e6] p-1.5 dark:bg-[#1b2117]" />
      <span className="flex min-w-0 flex-1 flex-col gap-0.5">
        <span className="flex items-center justify-between gap-2">
          <span className="truncate font-medium">{f.name}</span>
          <StatusBadge status={f.report.status} />
        </span>
        <span className="text-sm">{f.report.measures?.acres_text ?? "—"}</span>
        <span className="truncate text-xs text-muted-foreground">
          {place(f.report.location) || "Location not checked"}
        </span>
      </span>
    </button>
  );
}

type Tab = "map" | "land" | "plan";

function FarmDetail({
  farm,
  others,
  group,
  planner,
  surveyor,
  onChanged,
  onEdit,
  onOpenGroup,
  onDeleted,
}: {
  farm: Farm;
  others: Farm[];
  group?: Group;
  planner: boolean;
  surveyor: boolean;
  onChanged: (f: Farm) => void;
  onEdit: () => void;
  onOpenGroup: (id: string) => void;
  onDeleted: () => void;
}) {
  const [confirm, setConfirm] = useState(false);
  const [zones, setZones] = useState<ZoneShape[]>([]);
  const [tab, setTab] = useState<Tab>("map");
  const [error, setError] = useState("");
  const outlines = others
    .filter((o) => o.id !== farm.id)
    .map((o) => ({ id: o.id, name: o.name, corners: o.boundary.corners }));
  const tabs: { id: Tab; label: string; icon: ReactNode; on: boolean }[] = [
    { id: "map", label: "Map", icon: <MapIcon />, on: true },
    { id: "land", label: "Land and layout", icon: <Mountain />, on: surveyor },
    { id: "plan", label: "Season plan", icon: <Sprout />, on: planner },
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
            <a href={`/api/farms/${encodeURIComponent(farm.id)}/geojson`} download>
              <Download /> GeoJSON
            </a>
          </Button>
          {confirm ? (
            <Button
              variant="destructive"
              size="sm"
              onClick={() => api.remove(farm.id).then(onDeleted, (e: unknown) => setError(message(e)))}
            >
              Delete for good
            </Button>
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
      {tab === "land" && <Land farm={farm} onFarm={onChanged} />}
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
              On papers: {farm.declared.value} {farm.declared.unit}
            </p>
          )}
          <p className="mt-1 text-xs text-muted-foreground">
            Checked {new Date(farm.report.checked_at).toLocaleString()}
          </p>
        </div>
      </div>
      {planner && (
        <div className={tab === "plan" ? "" : "hidden"}>
          <Planning farm={farm} onFarm={onChanged} onZones={setZones} />
        </div>
      )}
    </>
  );
}
