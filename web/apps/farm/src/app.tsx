import { Button } from "@agentkit/ui/components/button";
import { ArrowLeft, Download, MessageCircle, Pencil, Plus, Sprout, Trash2 } from "lucide-react";
import { lazy, Suspense, useCallback, useEffect, useState } from "react";
import { api } from "./api";
import { place } from "./coords";
import { ReportView, StatusBadge } from "./report";
import type { ZoneShape } from "./map";
import type { Config, Farm } from "./types";

// The map (Leaflet) and the assistant load when first needed: the list of
// plots comes up first on a slow connection.
const Editor = lazy(() => import("./editor").then((m) => ({ default: m.Editor })));
const FarmMap = lazy(() => import("./map").then((m) => ({ default: m.FarmMap })));
const Planning = lazy(() => import("./planning").then((m) => ({ default: m.Planning })));
const Assistant = lazy(() => import("./assistant").then((m) => ({ default: m.Assistant })));

const loading = <p className="text-sm text-muted-foreground">Loading…</p>;

type View = { kind: "list" } | { kind: "edit"; farm?: Farm } | { kind: "farm"; id: string };

export function App() {
  const [farms, setFarms] = useState<Farm[]>();
  const [config, setConfig] = useState<Config>();
  const [view, setView] = useState<View>({ kind: "list" });
  const [chat, setChat] = useState(false);
  const [error, setError] = useState("");

  const load = useCallback(() => {
    api
      .farms()
      .then(setFarms)
      .catch((e: unknown) => setError(e instanceof Error ? e.message : String(e)));
  }, []);
  useEffect(() => {
    load();
    api.config().then(setConfig, () => setConfig({ assistant: false, country: "" }));
  }, [load]);

  const current = view.kind === "farm" ? farms?.find((f) => f.id === view.id) : undefined;

  return (
    <div className="flex h-dvh">
      <div className="flex min-w-0 flex-1 flex-col overflow-y-auto">
        <header className="flex items-center justify-between gap-2 border-b px-4 py-3">
          <button className="flex items-center gap-2 font-semibold" onClick={() => setView({ kind: "list" })}>
            <Sprout className="size-5 text-ak-ok" aria-hidden="true" /> My land
          </button>
          <div className="flex gap-2">
            {config?.assistant && (
              <Button variant="outline" size="sm" onClick={() => setChat((c) => !c)} aria-pressed={chat}>
                <MessageCircle /> Assistant
              </Button>
            )}
            {view.kind === "list" && (
              <Button size="sm" onClick={() => setView({ kind: "edit" })}>
                <Plus /> Add plot
              </Button>
            )}
          </div>
        </header>
        <main className="mx-auto flex w-full max-w-6xl flex-col gap-4 p-4">
          <Suspense fallback={loading}>
            {error && (
              <p role="alert" className="text-sm text-ak-bad">
                {error}
              </p>
            )}
            {view.kind === "list" && (
              <FarmList
                farms={farms}
                onOpen={(id) => setView({ kind: "farm", id })}
                onAdd={() => setView({ kind: "edit" })}
              />
            )}
            {view.kind === "edit" && (
              <>
                <BackLink onClick={() => setView(view.farm ? { kind: "farm", id: view.farm.id } : { kind: "list" })} />
                <h1 className="text-xl font-semibold">{view.farm ? `Edit ${view.farm.name}` : "Add a plot"}</h1>
                <Editor
                  farm={view.farm}
                  others={farms ?? []}
                  onCancel={() => setView(view.farm ? { kind: "farm", id: view.farm.id } : { kind: "list" })}
                  onSaved={(f) => {
                    setFarms((fs) => [...(fs ?? []).filter((x) => x.id !== f.id), f]);
                    setView({ kind: "farm", id: f.id });
                  }}
                />
              </>
            )}
            {view.kind === "farm" && current && (
              <FarmDetail
                key={current.id}
                farm={current}
                others={farms ?? []}
                planner={config?.planner ?? false}
                onChanged={(f) => setFarms((fs) => fs?.map((x) => (x.id === f.id ? f : x)))}
                onBack={() => setView({ kind: "list" })}
                onEdit={() => setView({ kind: "edit", farm: current })}
                onDeleted={() => {
                  setFarms((fs) => fs?.filter((x) => x.id !== current.id));
                  setView({ kind: "list" });
                }}
              />
            )}
          </Suspense>
        </main>
      </div>
      {chat && (
        <div className="fixed inset-0 z-[1000] sm:static sm:z-auto sm:w-96">
          <Suspense fallback={loading}>
            <Assistant onClose={() => setChat(false)} />
          </Suspense>
        </div>
      )}
    </div>
  );
}

function BackLink({ onClick }: { onClick: () => void }) {
  return (
    <button onClick={onClick} className="flex items-center gap-1 self-start text-sm text-muted-foreground">
      <ArrowLeft className="size-4" aria-hidden="true" /> Back
    </button>
  );
}

function FarmList({ farms, onOpen, onAdd }: { farms?: Farm[]; onOpen: (id: string) => void; onAdd: () => void }) {
  if (!farms) return <p className="text-sm text-muted-foreground">Loading…</p>;
  if (farms.length === 0)
    return (
      <div className="flex flex-col items-center gap-3 py-16 text-center">
        <Sprout className="size-10 text-ak-ok" aria-hidden="true" />
        <h1 className="text-xl font-semibold">Add your first plot</h1>
        <p className="max-w-sm text-sm text-muted-foreground">
          Draw its corners on the satellite map, walk around it with your phone&apos;s GPS, or type the coordinates. We
          measure it and check that it looks right.
        </p>
        <Button onClick={onAdd}>
          <Plus /> Add plot
        </Button>
      </div>
    );
  const total = farms.reduce((s, f) => s + (f.report.measures?.acres ?? 0), 0);
  return (
    <>
      <div className="flex items-baseline justify-between">
        <h1 className="text-xl font-semibold">Your plots</h1>
        <span className="text-sm text-muted-foreground">
          {farms.length} {farms.length === 1 ? "plot" : "plots"} · {total.toFixed(2)} acres
        </span>
      </div>
      <ul className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
        {farms.map((f) => (
          <li key={f.id}>
            <button
              onClick={() => onOpen(f.id)}
              className="flex w-full flex-col gap-1 rounded-lg border p-4 text-left hover:bg-accent"
            >
              <span className="flex items-center justify-between gap-2">
                <span className="font-medium">{f.name}</span>
                <StatusBadge status={f.report.status} />
              </span>
              <span className="text-lg">{f.report.measures?.acres_text ?? "—"}</span>
              <span className="truncate text-sm text-muted-foreground">
                {place(f.report.location) || "Location not checked"}
              </span>
            </button>
          </li>
        ))}
      </ul>
    </>
  );
}

function FarmDetail({
  farm,
  others,
  planner,
  onChanged,
  onBack,
  onEdit,
  onDeleted,
}: {
  farm: Farm;
  others: Farm[];
  planner: boolean;
  onChanged: (f: Farm) => void;
  onBack: () => void;
  onEdit: () => void;
  onDeleted: () => void;
}) {
  const [confirm, setConfirm] = useState(false);
  const [zones, setZones] = useState<ZoneShape[]>([]);
  const [error, setError] = useState("");
  const outlines = others
    .filter((o) => o.id !== farm.id)
    .map((o) => ({ id: o.id, name: o.name, corners: o.boundary.corners }));
  return (
    <>
      <BackLink onClick={onBack} />
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h1 className="text-xl font-semibold">{farm.name}</h1>
        <div className="flex gap-2">
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
              onClick={() =>
                api
                  .remove(farm.id)
                  .then(onDeleted, (e: unknown) => setError(e instanceof Error ? e.message : String(e)))
              }
            >
              Delete for good
            </Button>
          ) : (
            <Button variant="ghost" size="sm" onClick={() => setConfirm(true)}>
              <Trash2 /> Delete
            </Button>
          )}
        </div>
      </div>
      {error && (
        <p role="alert" className="text-sm text-ak-bad">
          {error}
        </p>
      )}
      <div className="flex flex-col gap-4 lg:flex-row">
        <FarmMap
          corners={farm.boundary.corners}
          others={outlines}
          zones={zones}
          flagged={farm.report.checks.flatMap((c) => (c.severity === "info" ? [] : (c.corners ?? [])))}
          fit={1}
          className="h-[50vh] min-h-72 w-full overflow-hidden rounded-lg border lg:h-[70vh] lg:flex-1"
        />
        <div className="rounded-lg border p-4 lg:w-96">
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
      {planner && <Planning farm={farm} onFarm={onChanged} onZones={setZones} />}
    </>
  );
}
