import { Button } from "@agentkit/ui/components/button";
import { Input } from "@agentkit/ui/components/input";
import { Status, type StatusTone } from "@agentkit/ui";
import { AlertTriangle, CheckCircle2, Info, Plus, Sprout, Trash2, XCircle } from "lucide-react";
import { useEffect, useState } from "react";
import { ApiError, api } from "./api";
import type { ZoneShape } from "./map";
import { colors, cropLabel, formatDate, localName, partsOf, scoreTone, shapes } from "./plan";
import type {
  Crop,
  Farm,
  Option,
  Part,
  Plan,
  PlanCheck,
  PlanReport,
  PlanStatus,
  Profile,
  Recommendation,
  Soil,
  WaterKind,
} from "./types";

const waterKinds: { id: WaterKind; label: string }[] = [
  { id: "borewell", label: "Borewell" },
  { id: "open_well", label: "Open well" },
  { id: "canal", label: "Canal" },
  { id: "river", label: "River" },
  { id: "tank", label: "Village tank or lake" },
  { id: "farm_pond", label: "Farm pond" },
];

const selectClass = "h-9 w-full rounded-md border bg-background px-2 text-sm";

function message(e: unknown): string {
  return e instanceof Error ? e.message : String(e);
}

/** Crop planning for one plot: its land and water, what to plant, and the plan. */
export function Planning({
  farm,
  onFarm,
  onZones,
}: {
  farm: Farm;
  onFarm: (f: Farm) => void;
  onZones: (z: ZoneShape[]) => void;
}) {
  const [soils, setSoils] = useState<Soil[]>([]);
  const [crops, setCrops] = useState<Crop[]>([]);
  const [error, setError] = useState("");
  const [parts, setParts] = useState<Part[]>(() => partsOf(farm.plan, farm.plan_report));

  useEffect(() => {
    api.soils().then(setSoils, (e: unknown) => setError(message(e)));
    api.crops().then(
      (cs) => {
        setCrops(cs);
        onZones(shapes(farm.plan, cs));
      },
      (e: unknown) => setError(message(e)),
    );
    // Once per plot.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [farm.id]);

  return (
    <div className="flex flex-col gap-4">
      {error && (
        <p role="alert" className="text-sm text-ak-bad">
          {error}
        </p>
      )}
      <div className="grid gap-4 lg:grid-cols-2">
        <ProfileForm farm={farm} soils={soils} crops={crops} onFarm={onFarm} />
        <WhatToPlant
          farm={farm}
          crops={crops}
          onAdd={(p) => setParts((ps) => [...ps.filter((x) => x.crop !== p.crop || x.with), p])}
        />
      </div>
      <PlanEditor farm={farm} crops={crops} parts={parts} setParts={setParts} onFarm={onFarm} onZones={onZones} />
    </div>
  );
}

function Card({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <section aria-label={title} className="flex flex-col gap-3 rounded-lg border p-4">
      <h2 className="font-semibold">{title}</h2>
      {children}
    </section>
  );
}

function ProfileForm({
  farm,
  soils,
  crops,
  onFarm,
}: {
  farm: Farm;
  soils: Soil[];
  crops: Crop[];
  onFarm: (f: Farm) => void;
}) {
  const [p, setP] = useState<Profile>(() => farm.profile ?? {});
  const [state, setState] = useState<"" | "saving" | "saved">("");
  const [error, setError] = useState("");
  const soil = soils.find((s) => s.id === p.soil);
  const water = p.water ?? [];
  const change = (q: Partial<Profile>) => {
    setP((x) => ({ ...x, ...q }));
    setState("");
  };
  const toggle = (kind: WaterKind, on: boolean) =>
    change({ water: on ? [...water, { kind, reliable: true }] : water.filter((w) => w.kind !== kind) });
  const save = () => {
    setState("saving");
    setError("");
    api.setProfile(farm.id, p).then(
      (f) => {
        onFarm(f);
        setState("saved");
      },
      (e: unknown) => {
        setError(message(e));
        setState("");
      },
    );
  };
  return (
    <Card title="Land and water">
      <label className="flex flex-col gap-1 text-sm">
        Soil
        <select
          aria-label="Soil"
          className={selectClass}
          value={p.soil ?? ""}
          onChange={(e) => change({ soil: e.target.value || undefined })}
        >
          <option value="">I don&apos;t know</option>
          {soils.map((s) => (
            <option key={s.id} value={s.id}>
              {s.name}
              {localName(s.names) && ` · ${localName(s.names)}`}
            </option>
          ))}
        </select>
      </label>
      {soil ? (
        <p className="text-sm text-muted-foreground">{soil.signs}</p>
      ) : (
        <details className="text-sm">
          <summary className="cursor-pointer text-muted-foreground">How to tell your soil</summary>
          <ul className="mt-2 flex flex-col gap-1">
            {soils.map((s) => (
              <li key={s.id}>
                <button className="text-left underline-offset-2 hover:underline" onClick={() => change({ soil: s.id })}>
                  <span className="font-medium">{s.name}</span>: {s.signs}
                </button>
              </li>
            ))}
          </ul>
        </details>
      )}
      <label className="flex flex-col gap-1 text-sm">
        pH from a soil test (optional)
        <Input
          type="number"
          step="0.1"
          min="3"
          max="11"
          value={p.ph ?? ""}
          onChange={(e) => change({ ph: e.target.value === "" ? undefined : Number(e.target.value) })}
        />
      </label>
      <fieldset className="flex flex-col gap-1 text-sm">
        <legend className="mb-1">Water besides rain</legend>
        {waterKinds.map((k) => {
          const w = water.find((x) => x.kind === k.id);
          return (
            <div key={k.id} className="flex flex-wrap items-center gap-x-4">
              <label className="flex items-center gap-2">
                <input type="checkbox" checked={!!w} onChange={(e) => toggle(k.id, e.target.checked)} />
                {k.label}
              </label>
              {w && (
                <label className="flex items-center gap-2 text-muted-foreground">
                  <input
                    type="checkbox"
                    checked={!w.reliable}
                    onChange={(e) =>
                      change({ water: water.map((x) => (x.kind === k.id ? { ...x, reliable: !e.target.checked } : x)) })
                    }
                  />
                  runs dry sometimes
                </label>
              )}
            </div>
          );
        })}
        <label className="mt-1 flex items-center gap-2">
          <input type="checkbox" checked={!!p.drip} onChange={(e) => change({ drip: e.target.checked || undefined })} />
          Drip or sprinkler
        </label>
      </fieldset>
      <label className="flex flex-col gap-1 text-sm">
        Last season&apos;s crop
        <select
          aria-label="Last season's crop"
          className={selectClass}
          value={p.previous_crop ?? ""}
          onChange={(e) => change({ previous_crop: e.target.value || undefined })}
        >
          <option value="">None or not sure</option>
          {crops.map((c) => (
            <option key={c.id} value={c.id}>
              {c.name}
            </option>
          ))}
        </select>
      </label>
      {error && (
        <p role="alert" className="text-sm text-ak-bad">
          {error}
        </p>
      )}
      <div className="flex items-center gap-3">
        <Button size="sm" onClick={save} disabled={state === "saving"}>
          Save land and water
        </Button>
        {state === "saved" && <span className="text-sm text-ak-ok">Saved</span>}
      </div>
    </Card>
  );
}

function WhatToPlant({ farm, crops, onAdd }: { farm: Farm; crops: Crop[]; onAdd: (p: Part) => void }) {
  const [when, setWhen] = useState<"next" | "around">("next");
  const [around, setAround] = useState("");
  const [crop, setCrop] = useState("");
  const [busy, setBusy] = useState(false);
  const [rec, setRec] = useState<Recommendation>();
  const [error, setError] = useState("");
  const ask = () => {
    setBusy(true);
    setError("");
    api
      .recommend(farm.id, {
        around: when === "around" && around ? around : undefined,
        crops: crop ? [crop] : undefined,
        limit: crop ? undefined : 8,
      })
      .then(setRec, (e: unknown) => setError(message(e)))
      .finally(() => setBusy(false));
  };
  return (
    <Card title="What to plant">
      <fieldset className="flex flex-col gap-2 text-sm">
        <legend className="mb-1">When do you want to sow?</legend>
        <label className="flex items-center gap-2">
          <input type="radio" name="when" checked={when === "next"} onChange={() => setWhen("next")} />
          At the next good time
        </label>
        <label className="flex flex-wrap items-center gap-2">
          <input type="radio" name="when" checked={when === "around"} onChange={() => setWhen("around")} />
          Around
          <Input
            type="date"
            aria-label="Sowing around"
            className="h-8 w-44"
            value={around}
            onChange={(e) => {
              setAround(e.target.value);
              setWhen("around");
            }}
          />
          <span className="text-muted-foreground">(give or take six weeks)</span>
        </label>
      </fieldset>
      <label className="flex flex-col gap-1 text-sm">
        Crop
        <select aria-label="Crop" className={selectClass} value={crop} onChange={(e) => setCrop(e.target.value)}>
          <option value="">Any crop: suggest the best</option>
          {crops.map((c) => (
            <option key={c.id} value={c.id}>
              {c.name}
            </option>
          ))}
        </select>
      </label>
      <Button size="sm" className="self-start" onClick={ask} disabled={busy}>
        <Sprout /> {busy ? "Working it out…" : "Suggest crops"}
      </Button>
      {error && (
        <p role="alert" className="text-sm text-ak-bad">
          {error}
        </p>
      )}
      {rec && (
        <div className="flex flex-col gap-2" aria-label="Suggestions">
          {rec.notes?.map((n) => (
            <p key={n.id} className="text-sm text-muted-foreground">
              {n.message}
            </p>
          ))}
          <ol className="flex flex-col gap-2">
            {rec.options.map((o) => (
              <OptionCard key={o.crop} o={o} onAdd={() => onAdd({ crop: o.crop, share: 0.5, sow: o.sow })} />
            ))}
          </ol>
        </div>
      )}
    </Card>
  );
}

function OptionCard({ o, onAdd }: { o: Option; onAdd: () => void }) {
  const local = localName(o.names);
  return (
    <li className="flex flex-col gap-1 rounded-md border p-3 text-sm" data-crop={o.crop}>
      <div className="flex items-center justify-between gap-2">
        <span className="font-medium">
          {o.name}
          {local && <span className="font-normal text-muted-foreground"> · {local}</span>}
          {o.analog && <span className="ml-2 rounded bg-accent px-1.5 py-0.5 text-xs">new to India</span>}
        </span>
        <Status tone={scoreTone(o.score)}>{o.score}/100</Status>
      </div>
      <div>
        Sow {formatDate(o.sow)}, harvest about {formatDate(o.harvest)}
        {o.window && (
          <span className="text-muted-foreground">
            {" "}
            · good from {formatDate(o.window.from)} to {formatDate(o.window.to)}
          </span>
        )}
      </div>
      {!o.window && o.next && (
        <div className="text-muted-foreground">
          Next good window: {formatDate(o.next.from)} to {formatDate(o.next.to)}
        </div>
      )}
      <div className="text-muted-foreground">
        Water: needs about {o.water.need_mm} mm, rain gives {o.water.rain_mm} mm
        {o.water.short_mm > 0 && <span className="text-ak-bad"> · {o.water.short_mm} mm short</span>}
      </div>
      <ul className="flex flex-col gap-0.5">
        {o.risks.map((n) => (
          <li key={n.id} className="flex gap-1.5">
            <AlertTriangle className="mt-0.5 size-3.5 shrink-0 text-ak-warn" aria-label="Risk" />
            {n.message}
          </li>
        ))}
        {o.reasons.map((n) => (
          <li key={n.id} className="flex gap-1.5 text-muted-foreground">
            <CheckCircle2 className="mt-0.5 size-3.5 shrink-0 text-ak-ok" aria-label="Good" />
            {n.message}
          </li>
        ))}
      </ul>
      <Button variant="outline" size="sm" className="self-start" onClick={onAdd}>
        <Plus /> Add to plan
      </Button>
    </li>
  );
}

const planTone: Record<PlanStatus, StatusTone> = { sound: "ok", needs_review: "warn", invalid: "bad" };
const planLabel: Record<PlanStatus, string> = { sound: "Sound", needs_review: "Needs review", invalid: "Has errors" };

function PlanEditor({
  farm,
  crops,
  parts,
  setParts,
  onFarm,
  onZones,
}: {
  farm: Farm;
  crops: Crop[];
  parts: Part[];
  setParts: React.Dispatch<React.SetStateAction<Part[]>>;
  onFarm: (f: Farm) => void;
  onZones: (z: ZoneShape[]) => void;
}) {
  const [plan, setPlan] = useState<Plan | undefined>(farm.plan);
  const [report, setReport] = useState<PlanReport | undefined>(farm.plan_report);
  const [dirty, setDirty] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const total = parts.reduce((s, p) => s + p.share, 0);
  const edit = (i: number, q: Partial<Part>) => {
    setParts((ps) => ps.map((p, j) => (j === i ? { ...p, ...q } : p)));
    setDirty(true);
  };
  const layout = () => {
    setBusy(true);
    setError("");
    api
      .layout(farm.id, parts)
      .then(
        (r) => {
          setPlan(r.plan);
          setReport(r.report);
          setDirty(false);
          onZones(shapes(r.plan, crops));
        },
        (e: unknown) => setError(message(e)),
      )
      .finally(() => setBusy(false));
  };
  const save = () => {
    if (!plan) return;
    setBusy(true);
    setError("");
    api
      .savePlan(farm.id, plan)
      .then(onFarm, (e: unknown) => {
        if (e instanceof ApiError && e.planReport) setReport(e.planReport);
        setError(message(e));
      })
      .finally(() => setBusy(false));
  };
  const clear = () => {
    api.clearPlan(farm.id).then(
      (f) => {
        onFarm(f);
        setPlan(undefined);
        setReport(undefined);
        setParts([]);
        onZones([]);
      },
      (e: unknown) => setError(message(e)),
    );
  };
  const saved = farm.plan !== undefined && JSON.stringify(plan) === JSON.stringify(farm.plan);
  return (
    <Card title="Season plan">
      <p className="text-sm text-muted-foreground">
        Share the plot between crops. We cut it into strips along its long side, rows running north to south, and check
        the plan.
      </p>
      {parts.length > 0 && (
        <div className="flex flex-col gap-2">
          {parts.map((p, i) => (
            <div
              key={i}
              className="grid grid-cols-2 items-end gap-2 sm:grid-cols-[1fr_1fr_6rem_6rem_10rem_auto]"
              data-part={i}
            >
              <label className="flex flex-col gap-1 text-xs">
                Crop
                <select
                  aria-label="Zone crop"
                  className={selectClass}
                  value={p.crop}
                  onChange={(e) => edit(i, { crop: e.target.value })}
                >
                  {!p.crop && <option value="">Pick a crop</option>}
                  {crops.map((c) => (
                    <option key={c.id} value={c.id}>
                      {c.name}
                    </option>
                  ))}
                </select>
              </label>
              <label className="flex flex-col gap-1 text-xs">
                In rows with
                <select
                  className={selectClass}
                  value={p.with ?? ""}
                  onChange={(e) =>
                    edit(i, { with: e.target.value || undefined, pattern: e.target.value ? [2, 1] : undefined })
                  }
                >
                  <option value="">Nothing (one crop)</option>
                  {crops
                    .filter((c) => c.id !== p.crop)
                    .map((c) => (
                      <option key={c.id} value={c.id}>
                        {c.name}
                        {crops.find((x) => x.id === p.crop)?.intercrops?.includes(c.id) ? " (good pair)" : ""}
                      </option>
                    ))}
                </select>
              </label>
              <label className="flex flex-col gap-1 text-xs">
                Rows
                <span className="flex items-center gap-1">
                  <Input
                    type="number"
                    min="1"
                    aria-label="Rows of the crop"
                    className="h-9 px-1"
                    disabled={!p.with}
                    value={p.pattern?.[0] ?? ""}
                    onChange={(e) => edit(i, { pattern: [Number(e.target.value), p.pattern?.[1] ?? 1] })}
                  />
                  :
                  <Input
                    type="number"
                    min="1"
                    aria-label="Rows of the second crop"
                    className="h-9 px-1"
                    disabled={!p.with}
                    value={p.pattern?.[1] ?? ""}
                    onChange={(e) => edit(i, { pattern: [p.pattern?.[0] ?? 2, Number(e.target.value)] })}
                  />
                </span>
              </label>
              <label className="flex flex-col gap-1 text-xs">
                Share %
                <Input
                  type="number"
                  min="1"
                  max="100"
                  className="h-9"
                  value={Math.round(p.share * 100)}
                  onChange={(e) => edit(i, { share: Number(e.target.value) / 100 })}
                />
              </label>
              <label className="flex flex-col gap-1 text-xs">
                Sow on
                <Input type="date" className="h-9" value={p.sow} onChange={(e) => edit(i, { sow: e.target.value })} />
              </label>
              <Button
                variant="ghost"
                size="sm"
                aria-label={`Remove ${cropLabel(crops, p.crop)}`}
                onClick={() => {
                  setParts((ps) => ps.filter((_, j) => j !== i));
                  setDirty(true);
                }}
              >
                <Trash2 />
              </Button>
            </div>
          ))}
          <p className={`text-xs ${total > 1.0001 ? "text-ak-bad" : "text-muted-foreground"}`}>
            {Math.round(total * 100)}% of the plot{total < 0.999 ? "; the rest stays unplanned" : ""}
          </p>
        </div>
      )}
      <div className="flex flex-wrap gap-2">
        <Button
          variant="outline"
          size="sm"
          onClick={() => {
            setParts((ps) => [...ps, { crop: "", share: Math.max(0.1, 1 - total), sow: "" }]);
            setDirty(true);
          }}
        >
          <Plus /> Add a crop
        </Button>
        <Button
          size="sm"
          onClick={layout}
          disabled={busy || parts.length === 0 || parts.some((p) => !p.crop || !p.sow)}
        >
          Lay out on the map
        </Button>
        {plan && !dirty && !saved && (
          <Button size="sm" onClick={save} disabled={busy || report?.status === "invalid"}>
            Save plan
          </Button>
        )}
        {farm.plan && (
          <Button variant="ghost" size="sm" onClick={clear}>
            Remove plan
          </Button>
        )}
      </div>
      {saved && <p className="text-sm text-ak-ok">This plan is saved.</p>}
      {error && (
        <p role="alert" className="text-sm text-ak-bad">
          {error}
        </p>
      )}
      {report && plan && <PlanReportView report={report} plan={plan} crops={crops} />}
    </Card>
  );
}

function CheckIcon({ c }: { c: PlanCheck }) {
  switch (c.severity) {
    case "error":
      return <XCircle className="mt-0.5 size-4 shrink-0 text-ak-bad" aria-label="Error" />;
    case "warning":
      return <AlertTriangle className="mt-0.5 size-4 shrink-0 text-ak-warn" aria-label="Warning" />;
  }
  return <Info className="mt-0.5 size-4 shrink-0 text-ak-muted" aria-label="Note" />;
}

/** A checked plan: its verdict, each zone's numbers, and every check. */
export function PlanReportView({ report, plan, crops }: { report: PlanReport; plan: Plan; crops: Crop[] }) {
  const order = { error: 0, warning: 1, info: 2 };
  const checks = [...report.checks].sort((a, b) => order[a.severity] - order[b.severity]);
  return (
    <section aria-label="Plan check" className="flex flex-col gap-3">
      <Status tone={planTone[report.status]}>{planLabel[report.status]}</Status>
      {report.zones && (
        <ul className="grid gap-2 sm:grid-cols-2">
          {report.zones.map((z, i) => {
            const zone = plan.zones[i];
            if (!zone) return null;
            return (
              <li key={i} className="flex gap-2 rounded-md border p-2 text-sm">
                <span
                  className="mt-1 size-3 shrink-0 rounded-sm"
                  aria-hidden="true"
                  style={{ background: colors[i % colors.length] }}
                />
                <span>
                  <span className="font-medium">
                    {i + 1}. {cropLabel(crops, zone.crop, zone.with)}
                  </span>
                  <br />
                  {(z.area_m2 / 4046.8564224).toFixed(2)} acres ({Math.round(z.share * 100)}%) · {z.rows} rows ·{" "}
                  {z.plants.toLocaleString()} plants
                  {z.with_plants ? ` + ${z.with_plants.toLocaleString()}` : ""}
                  <br />
                  <span className="text-muted-foreground">Sow {formatDate(zone.sow)}</span>
                </span>
              </li>
            );
          })}
        </ul>
      )}
      <ul className="flex flex-col gap-1.5">
        {checks.map((c, i) => (
          <li key={i} className="flex gap-2 text-sm" data-check={c.id} data-severity={c.severity}>
            <CheckIcon c={c} />
            <span>{c.message}</span>
          </li>
        ))}
      </ul>
    </section>
  );
}
