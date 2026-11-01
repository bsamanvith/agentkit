import { Button } from "@agentkit/ui/components/button";
import { Status, type StatusTone } from "@agentkit/ui";
import { AlertTriangle, Info, RefreshCw, Save, Trash2, Wand2, XCircle } from "lucide-react";
import { useEffect, useState } from "react";
import { ApiError, api } from "./api";
import { LandView } from "./landpicture";
import { elementLooks, looks, metres, nearbyLabel } from "./landview";
import type { Crop, Design, DesignReport, DesignStatus, Farm, LandNote, ShapeKind, Survey, Want } from "./types";

function message(e: unknown): string {
  return e instanceof Error ? e.message : String(e);
}

const shapeWords: Record<ShapeKind, string> = {
  square: "Square",
  rectangle: "Rectangle",
  long_strip: "Long strip",
  triangle: "Triangle",
  trapezium: "Trapezium",
  irregular: "Irregular",
};

const designTone: Record<DesignStatus, StatusTone> = { sound: "ok", needs_review: "warn", invalid: "bad" };
const designWords: Record<DesignStatus, string> = {
  sound: "Looks sound",
  needs_review: "Worth a second look",
  invalid: "Has problems",
};

/**
 * The plot's land: the animated land view, what is around it (from the
 * map), its shape and slope, and laying out the farm (gate, path, shed,
 * pond and border trees) with checks.
 */
export function Land({ farm, onFarm }: { farm: Farm; onFarm: (f: Farm) => void }) {
  const [survey, setSurvey] = useState<Survey | undefined>(farm.survey);
  const [busy, setBusy] = useState(!farm.survey);
  const [error, setError] = useState("");
  const [crops, setCrops] = useState<Crop[]>([]);
  const [draft, setDraft] = useState<{ design: Design; report: DesignReport }>();

  const refresh = () => {
    setBusy(true);
    setError("");
    api
      .survey(farm.id, true)
      .then(setSurvey, (e: unknown) => setError(message(e)))
      .finally(() => setBusy(false));
  };
  useEffect(() => {
    if (!farm.survey)
      api
        .survey(farm.id)
        .then(setSurvey, (e: unknown) => setError(message(e)))
        .finally(() => setBusy(false));
    if (farm.plan?.zones.length) api.crops().then(setCrops, () => setCrops([]));
    // Once per plot.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [farm.id]);

  const design = draft?.design ?? farm.design;
  return (
    <div className="grid gap-4 lg:grid-cols-[minmax(0,1fr)_24rem]">
      <div>
        <LandView
          corners={farm.boundary.corners}
          survey={survey}
          design={design}
          zones={farm.plan?.zones}
          crops={crops}
        />
        {busy && (
          <p className="mt-2 text-sm text-muted-foreground" role="status">
            Looking at the map around your plot…
          </p>
        )}
        {error && (
          <p role="alert" className="mt-2 text-sm text-ak-bad">
            {error}
          </p>
        )}
      </div>
      <div className="flex flex-col gap-4">
        {survey && <Surroundings survey={survey} busy={busy} onRefresh={refresh} />}
        <ShapeFarm
          farm={farm}
          draft={draft}
          onDraft={setDraft}
          onFarm={(f) => {
            setDraft(undefined);
            onFarm(f);
          }}
        />
      </div>
    </div>
  );
}

function NoteIcon({ n }: { n: LandNote }) {
  switch (n.severity) {
    case "error":
      return <XCircle className="mt-0.5 size-4 shrink-0 text-ak-bad" aria-label="Problem" />;
    case "warning":
      return <AlertTriangle className="mt-0.5 size-4 shrink-0 text-ak-warn" aria-label="Warning" />;
  }
  return <Info className="mt-0.5 size-4 shrink-0 text-ak-muted" aria-label="Note" />;
}

function Notes({ notes }: { notes: LandNote[] }) {
  const order = { error: 0, warning: 1, info: 2 };
  const sorted = [...notes].sort((a, b) => order[a.severity] - order[b.severity]);
  if (sorted.length === 0) return null;
  return (
    <ul className="flex flex-col gap-1.5">
      {sorted.map((n, i) => (
        <li key={i} className="flex gap-2 text-sm" data-note={n.id} data-severity={n.severity}>
          <NoteIcon n={n} />
          <span>{n.message}</span>
        </li>
      ))}
    </ul>
  );
}

/** The plot's shape, slope and nearest roads, water and houses. */
export function Surroundings({ survey, busy, onRefresh }: { survey: Survey; busy: boolean; onRefresh: () => void }) {
  const s = survey.shape;
  const t = survey.terrain;
  return (
    <section aria-label="The land and around it" className="flex flex-col gap-3 rounded-lg border p-4">
      <div className="flex items-center justify-between gap-2">
        <h2 className="font-semibold">The land and around it</h2>
        <Button variant="ghost" size="sm" onClick={onRefresh} disabled={busy} aria-label="Look at the map again">
          <RefreshCw className={busy ? "animate-spin" : undefined} />
        </Button>
      </div>
      <dl className="grid grid-cols-2 gap-x-3 gap-y-1 text-sm">
        <dt className="text-muted-foreground">Shape</dt>
        <dd data-testid="shape">{shapeWords[s.kind]}</dd>
        <dt className="text-muted-foreground">Size</dt>
        <dd>
          about {metres(s.box.length_m)} × {metres(s.box.width_m)}
        </dd>
        <dt className="text-muted-foreground">Boundary</dt>
        <dd>{metres(s.perimeter_m)} round</dd>
        {s.long_side && (
          <>
            <dt className="text-muted-foreground">Long sides face</dt>
            <dd>{s.long_side}</dd>
          </>
        )}
        {t && (
          <>
            <dt className="text-muted-foreground">Slope</dt>
            <dd>
              {t.flat
                ? "flat"
                : `falls ${t.fall_m.toFixed(1)} m${t.downhill ? ` to the ${t.downhill}` : ""} (${t.slope_pct.toFixed(1)}%)`}
            </dd>
          </>
        )}
      </dl>
      {survey.nearest.length > 0 ? (
        <div>
          <h3 className="mb-1 text-sm font-medium">Nearest</h3>
          <ul className="flex flex-col gap-0.5 text-sm" aria-label="Nearest features">
            {survey.nearest.map((n) => (
              <li key={n.id} className="flex items-center gap-2">
                <span
                  aria-hidden="true"
                  className="inline-block size-2.5 rounded-full"
                  style={{ background: looks[n.kind]?.mark ?? looks[n.kind]?.stroke ?? "#999" }}
                />
                {nearbyLabel(n)}
              </li>
            ))}
          </ul>
        </div>
      ) : (
        <p className="text-sm text-muted-foreground">Nothing is mapped within {metres(survey.radius_m)}.</p>
      )}
      <Notes notes={survey.notes} />
      {survey.missing && survey.missing.length > 0 && (
        <p className="text-xs text-ak-warn">Could not reach: {survey.missing.join("; ")}. Try again later.</p>
      )}
      <p className="text-xs text-muted-foreground">
        From {survey.sources.join(", ")} · {new Date(survey.surveyed_at).toLocaleDateString()}
      </p>
    </section>
  );
}

const parts: { id: keyof Want & ("gate" | "path" | "shed" | "pond" | "border"); label: string }[] = [
  { id: "gate", label: "Gate on the road side" },
  { id: "path", label: "Cart path" },
  { id: "shed", label: "Shed by the gate" },
  { id: "pond", label: "Farm pond" },
  { id: "border", label: "Border trees" },
];

/** Lay out the farm: pick the pieces, get a suggestion, check it, save it. */
export function ShapeFarm({
  farm,
  draft,
  onDraft,
  onFarm,
}: {
  farm: Farm;
  draft?: { design: Design; report: DesignReport };
  onDraft: (d?: { design: Design; report: DesignReport }) => void;
  onFarm: (f: Farm) => void;
}) {
  const [want, setWant] = useState<Want>({ gate: true, path: true, shed: true, pond: true, border: true });
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const report = draft?.report ?? farm.design_report;
  const design = draft?.design ?? farm.design;

  const run = (p: Promise<unknown>) => {
    setBusy(true);
    setError("");
    p.catch((e: unknown) => {
      if (e instanceof ApiError && e.designReport && draft) onDraft({ design: draft.design, report: e.designReport });
      setError(message(e));
    }).finally(() => setBusy(false));
  };

  return (
    <section aria-label="Shape the farm" className="flex flex-col gap-3 rounded-lg border p-4">
      <h2 className="font-semibold">Shape the farm</h2>
      <fieldset className="flex flex-col gap-1.5 text-sm">
        <legend className="sr-only">What to lay out</legend>
        {parts.map((p) => (
          <label key={p.id} className="flex items-center gap-2">
            <input
              type="checkbox"
              checked={Boolean(want[p.id])}
              onChange={(e) => setWant((w) => ({ ...w, [p.id]: e.target.checked }))}
            />
            {p.label}
          </label>
        ))}
        {want.pond && (
          <label className="flex items-center gap-2">
            Pond side
            <select
              className="h-8 rounded-md border bg-background px-2"
              value={want.pond_side_m ?? 0}
              onChange={(e) => setWant((w) => ({ ...w, pond_side_m: Number(e.target.value) || undefined }))}
            >
              <option value={0}>by the plot&apos;s size</option>
              {[10, 15, 20, 30].map((m) => (
                <option key={m} value={m}>
                  {m} m
                </option>
              ))}
            </select>
          </label>
        )}
      </fieldset>
      <div className="flex flex-wrap gap-2">
        <Button
          size="sm"
          disabled={busy || !parts.some((p) => want[p.id])}
          onClick={() => run(api.suggestDesign(farm.id, want).then(onDraft))}
        >
          <Wand2 /> Suggest a layout
        </Button>
        {draft && (
          <Button
            size="sm"
            variant="outline"
            disabled={busy || draft.report.status === "invalid"}
            onClick={() => run(api.saveDesign(farm.id, draft.design).then(onFarm))}
          >
            <Save /> Save layout
          </Button>
        )}
        {draft && (
          <Button size="sm" variant="ghost" disabled={busy} onClick={() => onDraft(undefined)}>
            Discard
          </Button>
        )}
        {!draft && farm.design && (
          <Button size="sm" variant="ghost" disabled={busy} onClick={() => run(api.clearDesign(farm.id).then(onFarm))}>
            <Trash2 /> Clear layout
          </Button>
        )}
      </div>
      {error && (
        <p role="alert" className="text-sm text-ak-bad">
          {error}
        </p>
      )}
      {design && report && (
        <div className="flex flex-col gap-2">
          <div className="flex items-center justify-between text-sm">
            <Status tone={designTone[report.status]}>{designWords[report.status]}</Status>
            <span className="text-muted-foreground">{draft ? "Suggested, not saved" : "Saved"}</span>
          </div>
          <ul className="flex flex-col gap-1 text-sm" aria-label="Layout">
            {design.elements.map((e, i) => {
              const m = report.elements[i];
              return (
                <li key={i} className="flex flex-col">
                  <span className="flex items-center gap-2 font-medium">
                    <span
                      aria-hidden="true"
                      className="inline-block size-2.5 rounded-sm border"
                      style={{ background: elementLooks[e.kind].fill, borderColor: elementLooks[e.kind].stroke }}
                    />
                    {elementLooks[e.kind].label}
                    {m && m.area_m2 > 0 && (
                      <span className="font-normal text-muted-foreground">
                        {Math.round(m.area_m2)} m²{m.volume_m3 ? `, holds ${Math.round(m.volume_m3)} m³` : ""}
                        {e.trees ? `, ${e.trees} trees` : ""}
                      </span>
                    )}
                  </span>
                  {e.why && <span className="text-xs text-muted-foreground">{e.why}</span>}
                </li>
              );
            })}
          </ul>
          <p className="text-sm">
            Left for crops: <strong>{Math.round(report.fields_m2)} m²</strong> of {Math.round(report.plot_m2)} m² (
            {Math.round((100 * report.fields_m2) / Math.max(1, report.plot_m2))}%)
          </p>
          <Notes notes={report.checks} />
        </div>
      )}
    </section>
  );
}
