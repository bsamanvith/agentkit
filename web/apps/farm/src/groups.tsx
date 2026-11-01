import { Button } from "@agentkit/ui/components/button";
import { Input } from "@agentkit/ui/components/input";
import { Status, type StatusTone } from "@agentkit/ui";
import {
  AlertTriangle,
  Columns3,
  Droplets,
  Fence,
  Grid3x3,
  Handshake,
  Image,
  Info,
  LayoutGrid,
  Pencil,
  Plus,
  Ruler,
  Satellite,
  Save,
  Sparkles,
  Sun,
  Trash2,
  Undo2,
  Wind,
  X,
  XCircle,
} from "lucide-react";
import { lazy, Suspense, useEffect, useMemo, useState } from "react";
import { api } from "./api";
import {
  acresOf,
  area,
  effectWords,
  newPlant,
  placeWords,
  plantColor,
  plotsOf,
  problems,
  shareFor,
  sortEffects,
  templates,
  withPlace,
} from "./distribution";
import { GroupPicture } from "./grouppicture";
import { PlotShape } from "./shape";
import { localName } from "./plan";
import type {
  Arrange,
  Crop,
  Distribution,
  DistributionResult,
  DistributionStatus,
  Effect,
  EffectKind,
  Farm,
  Group,
  Place,
  PlantGroup,
  Rows,
} from "./types";

const FarmMap = lazy(() => import("./map").then((m) => ({ default: m.FarmMap })));

function message(e: unknown): string {
  return e instanceof Error ? e.message : String(e);
}

const statusTone: Record<DistributionStatus, StatusTone> = { sound: "ok", needs_review: "warn", invalid: "bad" };
const statusWords: Record<DistributionStatus, string> = {
  sound: "Works well",
  needs_review: "Worth a look",
  invalid: "Has problems",
};

const kindIcon: Record<EffectKind, typeof Sun> = {
  shade: Sun,
  edge: Fence,
  spacing: Grid3x3,
  water: Droplets,
  neighbours: Handshake,
  wind: Wind,
  layout: Ruler,
};

const arranges: { id: Arrange; label: string; hint: string }[] = [
  { id: "strips", label: "Strips", hint: "One strip per group, by share" },
  { id: "alternate", label: "Alternating", hint: "Strips that take turns" },
  { id: "by_plot", label: "Whole plots", hint: "Each plot to one group" },
];

const rowsChoices: { id: Rows; label: string }[] = [
  { id: "long_side", label: "Along the plot" },
  { id: "north_south", label: "North–south" },
  { id: "east_west", label: "East–west" },
];

const fieldClass = "h-9 w-full rounded-md border border-ak-control bg-background px-2 text-sm";

/** A row of buttons, one pressed: a segmented control. */
function Segmented<T extends string>({
  label,
  value,
  options,
  onChange,
}: {
  label: string;
  value: T;
  options: { id: T; label: string; hint?: string }[];
  onChange: (v: T) => void;
}) {
  return (
    <div role="radiogroup" aria-label={label} className="grid auto-cols-fr grid-flow-col gap-1 rounded-lg bg-ak-bg p-1">
      {options.map((o) => (
        <button
          key={o.id}
          type="button"
          role="radio"
          aria-checked={value === o.id}
          title={o.hint}
          onClick={() => onChange(o.id)}
          className="rounded-md px-2 py-1.5 text-xs font-medium text-muted-foreground transition-colors aria-checked:bg-ak-surface aria-checked:text-foreground aria-checked:shadow-sm"
        >
          {o.label}
        </button>
      ))}
    </div>
  );
}

/**
 * A plot group: its plots drawn together, its plant groups and how they
 * are spread, laid out live as the farmer changes them, with what the
 * arrangement does (shade, edges, spacing, water, neighbours, wind).
 */
export function GroupPage({
  group,
  farms,
  planner,
  onGroup,
  onDeleted,
  onEdit,
  onOpenPlot,
}: {
  group: Group;
  farms: Farm[];
  planner: boolean;
  onGroup: (g: Group) => void;
  onDeleted: () => void;
  onEdit: () => void;
  onOpenPlot: (id: string) => void;
}) {
  const plots = plotsOf(group, farms);
  const [crops, setCrops] = useState<Crop[]>([]);
  const [draft, setDraft] = useState<Distribution | undefined>(group.distribution);
  const [preview, setPreview] = useState<DistributionResult>();
  const [busy, setBusy] = useState(false);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  const [highlight, setHighlight] = useState<string>();
  const [view, setView] = useState<"picture" | "satellite">("picture");
  const [confirm, setConfirm] = useState(false);

  useEffect(() => {
    if (planner) api.crops().then(setCrops, (e: unknown) => setError(message(e)));
  }, [planner]);

  const plotKey = plots.map((f) => f.id + f.updated_at).join();
  const issues = draft ? problems(draft) : [];
  const canLay = planner && draft !== undefined && plots.length > 0 && issues.length === 0;
  const result = canLay ? preview : undefined;
  // Lay the draft out a moment after the last change.
  useEffect(() => {
    if (!canLay) return;
    const ctl = new AbortController();
    const t = setTimeout(() => {
      setBusy(true);
      api
        .previewDistribution(group.id, draft, ctl.signal)
        .then(
          (r) => {
            setPreview(r);
            setError("");
          },
          (e: unknown) => {
            if (!ctl.signal.aborted) setError(message(e));
          },
        )
        .finally(() => {
          if (!ctl.signal.aborted) setBusy(false);
        });
    }, 350);
    return () => {
      clearTimeout(t);
      ctl.abort();
    };
  }, [JSON.stringify(draft), group.id, plotKey, canLay]); // eslint-disable-line react-hooks/exhaustive-deps

  const saved = JSON.stringify(group.distribution ?? null);
  const dirty = draft !== undefined && JSON.stringify(result?.distribution ?? draft) !== saved;
  const save = () => {
    if (!draft) return;
    setSaving(true);
    setError("");
    api
      .saveDistribution(group.id, draft)
      .then(
        (r) => {
          onGroup(r.group);
          setDraft(r.group.distribution);
          setPreview(r.result);
        },
        (e: unknown) => setError(message(e)),
      )
      .finally(() => setSaving(false));
  };
  const have = useMemo(() => new Set(crops.map((c) => c.id)), [crops]);

  return (
    <div className="flex flex-col gap-5">
      <header className="flex flex-wrap items-end justify-between gap-3">
        <div className="flex min-w-0 flex-col gap-1">
          <p className="text-xs font-medium tracking-wide text-muted-foreground uppercase">Plot group</p>
          <h1 className="truncate text-2xl font-semibold">{group.name}</h1>
          <p className="text-sm text-muted-foreground">
            {plots.length} {plots.length === 1 ? "plot" : "plots"} · {acresOf(plots).toFixed(2)} acres
            {group.distribution ? ` · ${group.distribution.plants.length} plant groups` : ""}
          </p>
        </div>
        <div className="flex flex-wrap gap-2">
          <Button variant="outline" size="sm" onClick={onEdit}>
            <Pencil /> Name and plots
          </Button>
          {confirm ? (
            <Button
              variant="destructive"
              size="sm"
              onClick={() => api.removeGroup(group.id).then(onDeleted, (e: unknown) => setError(message(e)))}
            >
              Delete the group (plots stay)
            </Button>
          ) : (
            <Button variant="ghost" size="sm" onClick={() => setConfirm(true)} aria-label="Delete group">
              <Trash2 />
            </Button>
          )}
        </div>
      </header>

      {plots.length > 0 && (
        <ul className="flex gap-2 overflow-x-auto pb-1" aria-label="Plots in the group">
          {plots.map((f) => (
            <li key={f.id} className="shrink-0">
              <button
                onClick={() => onOpenPlot(f.id)}
                className="flex items-center gap-2 rounded-full border bg-ak-surface py-1 pr-3 pl-1 text-sm hover:bg-accent"
              >
                <PlotShape farms={[f]} className="size-7 rounded-full bg-ak-bg p-0.5" />
                {f.name}
                <span className="text-muted-foreground">{f.report.measures?.acres.toFixed(2)} ac</span>
              </button>
            </li>
          ))}
        </ul>
      )}

      {error && (
        <p role="alert" className="rounded-md border border-ak-bad/30 bg-ak-bad/5 px-3 py-2 text-sm text-ak-bad">
          {error}
        </p>
      )}

      {plots.length === 0 ? (
        <div className="flex flex-col items-center gap-3 rounded-xl border border-dashed p-10 text-center">
          <LayoutGrid className="size-8 text-ak-ok" aria-hidden="true" />
          <p className="max-w-sm text-sm text-muted-foreground">
            This group has no plots yet. Add the plots you farm together, then spread your plant groups over them.
          </p>
          <Button size="sm" onClick={onEdit}>
            <Plus /> Add plots
          </Button>
        </div>
      ) : (
        <div className="grid gap-5 lg:grid-cols-[minmax(0,1fr)_25rem]">
          <div className="flex min-w-0 flex-col gap-4">
            <section
              aria-label="The group's plots"
              className="relative overflow-hidden rounded-xl border bg-ak-surface shadow-sm"
            >
              <div className="m-2 flex w-fit rounded-lg bg-ak-surface/90 p-0.5 shadow-sm backdrop-blur sm:absolute sm:top-2 sm:left-2 sm:z-[500] sm:m-0">
                <Button
                  size="sm"
                  variant={view === "picture" ? "secondary" : "ghost"}
                  aria-pressed={view === "picture"}
                  onClick={() => setView("picture")}
                >
                  <Image /> Plan
                </Button>
                <Button
                  size="sm"
                  variant={view === "satellite" ? "secondary" : "ghost"}
                  aria-pressed={view === "satellite"}
                  onClick={() => setView("satellite")}
                >
                  <Satellite /> Satellite
                </Button>
              </div>
              {busy && (
                <span className="absolute top-3 right-3 z-[500] rounded-full bg-ak-surface/90 px-2 py-0.5 text-xs text-muted-foreground shadow-sm">
                  Laying out…
                </span>
              )}
              {view === "picture" ? (
                <GroupPicture
                  key={result ? "laid" : "bare"}
                  farms={plots}
                  result={result}
                  distribution={draft}
                  highlight={highlight}
                  className="block aspect-[16/10] w-full"
                />
              ) : (
                <Suspense fallback={<div className="aspect-[16/10] w-full animate-pulse bg-ak-bg" />}>
                  <FarmMap
                    corners={[]}
                    others={plots.map((f) => ({ id: f.id, name: f.name, corners: f.boundary.corners }))}
                    zones={(result?.plots ?? []).flatMap((p) =>
                      p.regions.map((r) => ({
                        ring: r.ring,
                        hole: r.hole,
                        label: "",
                        color: plantColor(draft, r.plant),
                      })),
                    )}
                    fit={1}
                    className="aspect-[4/3] w-full sm:aspect-[16/10]"
                  />
                </Suspense>
              )}
              {draft && result && (
                <Legend draft={draft} result={result} highlight={highlight} onHighlight={setHighlight} />
              )}
            </section>
            {result && <Effects effects={result.effects} draft={draft} status={result.status} />}
            {result && draft && <Measures result={result} draft={draft} crops={crops} />}
          </div>

          <aside className="flex flex-col gap-4 lg:sticky lg:top-4 lg:max-h-[calc(100dvh-6rem)] lg:self-start lg:overflow-y-auto lg:pr-1">
            {!planner ? (
              <p className="text-sm text-muted-foreground">Plant planning is not configured on this server.</p>
            ) : !draft ? (
              <Templates have={have} onPick={setDraft} />
            ) : (
              <Editor draft={draft} crops={crops} issues={issues} onChange={setDraft} onHighlight={setHighlight} />
            )}
            {draft && (
              <div className="sticky bottom-0 flex flex-wrap items-center gap-2 rounded-xl border bg-ak-surface/95 p-3 shadow-sm backdrop-blur">
                <Button size="sm" onClick={save} disabled={saving || !dirty || issues.length > 0 || !result}>
                  <Save /> {saving ? "Saving…" : "Save"}
                </Button>
                {dirty && (
                  <Button size="sm" variant="ghost" onClick={() => setDraft(group.distribution)}>
                    <Undo2 /> {group.distribution ? "Undo changes" : "Start again"}
                  </Button>
                )}
                {!dirty && group.distribution && (
                  <>
                    <span className="text-sm text-ak-ok">Saved</span>
                    <Button
                      size="sm"
                      variant="ghost"
                      className="ml-auto"
                      onClick={() =>
                        api.clearDistribution(group.id).then(
                          (g) => {
                            onGroup(g);
                            setDraft(undefined);
                          },
                          (e: unknown) => setError(message(e)),
                        )
                      }
                    >
                      Remove
                    </Button>
                  </>
                )}
              </div>
            )}
          </aside>
        </div>
      )}
    </div>
  );
}

function Templates({ have, onPick }: { have: Set<string>; onPick: (d: Distribution) => void }) {
  return (
    <section
      aria-label="Start a distribution"
      className="flex flex-col gap-3 rounded-xl border bg-ak-surface p-4 shadow-sm"
    >
      <div>
        <h2 className="flex items-center gap-2 font-semibold">
          <Sparkles className="size-4 text-ak-ok" aria-hidden="true" /> Spread your plants
        </h2>
        <p className="mt-1 text-sm text-muted-foreground">
          Start from a pattern and change it: the plots are laid out as you go, with the shade, edges and water it
          makes.
        </p>
      </div>
      <ul className="grid gap-2">
        {templates.map((t) => (
          <li key={t.id}>
            <button
              onClick={() => onPick(t.make(have))}
              disabled={have.size === 0}
              className="flex w-full flex-col gap-0.5 rounded-lg border p-3 text-left transition-colors hover:border-ak-ok/50 hover:bg-accent disabled:cursor-wait disabled:opacity-60"
            >
              <span className="text-sm font-medium">{t.label}</span>
              <span className="text-xs text-muted-foreground">{t.hint}</span>
            </button>
          </li>
        ))}
      </ul>
      {have.size === 0 && <p className="text-xs text-muted-foreground">Loading the crop list…</p>}
    </section>
  );
}

function Editor({
  draft,
  crops,
  issues,
  onChange,
  onHighlight,
}: {
  draft: Distribution;
  crops: Crop[];
  issues: string[];
  onChange: (d: Distribution) => void;
  onHighlight: (id?: string) => void;
}) {
  const set = (q: Partial<Distribution>) => onChange({ ...draft, ...q });
  const setPlant = (i: number, p: PlantGroup) => set({ plants: draft.plants.map((x, j) => (j === i ? p : x)) });
  const blocks = draft.plants.filter((p) => (p.place ?? "block") === "block");
  const weights = blocks.reduce((s, p) => s + (p.share ?? 1), 0);
  return (
    <div className="flex flex-col gap-4">
      <section aria-label="Arrangement" className="flex flex-col gap-3 rounded-xl border bg-ak-surface p-4 shadow-sm">
        <h2 className="flex items-center gap-2 font-semibold">
          <Columns3 className="size-4 text-muted-foreground" aria-hidden="true" /> Arrangement
        </h2>
        <Segmented
          label="How the blocks are cut"
          value={draft.arrange ?? "strips"}
          options={arranges}
          onChange={(arrange) => set({ arrange })}
        />
        {draft.arrange === "alternate" && (
          <label className="flex flex-col gap-1 text-sm">
            <span className="flex justify-between">
              Strip width <span className="text-muted-foreground">{draft.strip_m ?? 12} m</span>
            </span>
            <input
              type="range"
              min={2}
              max={40}
              value={draft.strip_m ?? 12}
              onChange={(e) => set({ strip_m: Number(e.target.value) })}
              className="accent-[#4d7c0f]"
            />
          </label>
        )}
        <div className="flex flex-col gap-1 text-sm">
          <span>Rows run</span>
          <Segmented
            label="Which way rows run"
            value={draft.rows ?? "long_side"}
            options={rowsChoices}
            onChange={(rows) => set({ rows })}
          />
        </div>
      </section>

      <section aria-label="Plant groups" className="flex flex-col gap-3">
        <div className="flex items-center justify-between">
          <h2 className="font-semibold">Plant groups</h2>
          <Button
            size="sm"
            variant="outline"
            disabled={draft.plants.length >= 12}
            onClick={() => set({ plants: [...draft.plants, newPlant(draft, crops)] })}
          >
            <Plus /> Add
          </Button>
        </div>
        {draft.plants.map((p, i) => (
          <PlantCard
            key={i}
            plant={p}
            color={plantColor(draft, p.id)}
            crops={crops}
            sharePct={
              (p.place ?? "block") === "block"
                ? Math.round(((p.share ?? 1) / Math.max(weights, 1e-9)) * 100)
                : undefined
            }
            others={weights - ((p.place ?? "block") === "block" ? (p.share ?? 1) : 0)}
            canRemove={draft.plants.length > 1}
            onChange={(q) => setPlant(i, q)}
            onRemove={() => set({ plants: draft.plants.filter((_, j) => j !== i) })}
            onHighlight={onHighlight}
          />
        ))}
        {issues.length > 0 && (
          <ul className="flex flex-col gap-1 text-sm text-ak-warn" role="status">
            {issues.map((m) => (
              <li key={m} className="flex gap-1.5">
                <AlertTriangle className="mt-0.5 size-3.5 shrink-0" aria-hidden="true" />
                {m}
              </li>
            ))}
          </ul>
        )}
      </section>
    </div>
  );
}

function PlantCard({
  plant: p,
  color,
  crops,
  sharePct,
  others,
  canRemove,
  onChange,
  onRemove,
  onHighlight,
}: {
  plant: PlantGroup;
  color: string;
  crops: Crop[];
  sharePct?: number;
  /** The other blocks' shares added up: the slider sets a percentage of the whole. */
  others: number;
  canRemove: boolean;
  onChange: (p: PlantGroup) => void;
  onRemove: () => void;
  onHighlight: (id?: string) => void;
}) {
  const place = p.place ?? "block";
  const name = (id: string) => crops.find((c) => c.id === id)?.name ?? id;
  return (
    <div
      className="flex flex-col gap-3 rounded-xl border bg-ak-surface p-3 shadow-sm"
      style={{ borderLeft: `4px solid ${color}` }}
      onMouseEnter={() => onHighlight(p.id)}
      onMouseLeave={() => onHighlight(undefined)}
      data-plant={p.id}
    >
      <div className="flex items-center gap-2">
        <Input
          aria-label="Plant group name"
          value={p.name}
          onChange={(e) => onChange({ ...p, name: e.target.value })}
          className="h-8 font-medium"
        />
        {canRemove && (
          <Button size="sm" variant="ghost" aria-label={`Remove ${p.name}`} onClick={onRemove}>
            <X />
          </Button>
        )}
      </div>
      <div className="flex flex-wrap gap-1.5" aria-label="Crops">
        {p.crops.map((c, k) => (
          <span key={c} className="flex items-center gap-1 rounded-full bg-ak-bg py-0.5 pr-1 pl-2.5 text-xs">
            {k === 0 ? <strong className="font-medium">{name(c)}</strong> : name(c)}
            <button
              aria-label={`Remove ${name(c)}`}
              className="rounded-full p-0.5 hover:bg-ak-hover"
              onClick={() => onChange({ ...p, crops: p.crops.filter((x) => x !== c) })}
            >
              <X className="size-3" />
            </button>
          </span>
        ))}
        {p.crops.length < 4 && (
          <select
            aria-label={`Add a crop to ${p.name}`}
            className="h-6 rounded-full border border-dashed bg-transparent px-2 text-xs text-muted-foreground"
            value=""
            onChange={(e) => e.target.value && onChange({ ...p, crops: [...p.crops, e.target.value] })}
          >
            <option value="">{p.crops.length === 0 ? "Pick a crop" : "+ mix in a crop"}</option>
            {crops
              .filter((c) => !p.crops.includes(c.id))
              .map((c) => (
                <option key={c.id} value={c.id}>
                  {c.name}
                  {localName(c.names) ? ` · ${localName(c.names)}` : ""}
                </option>
              ))}
          </select>
        )}
      </div>
      <div className="grid grid-cols-2 gap-2">
        <label className="col-span-2 flex flex-col gap-1 text-xs text-muted-foreground sm:col-span-1">
          Where
          <select
            className={fieldClass}
            value={place}
            onChange={(e) => onChange(withPlace(p, e.target.value as Place))}
          >
            {(Object.keys(placeWords) as Place[]).map((k) => (
              <option key={k} value={k}>
                {placeWords[k].label}
              </option>
            ))}
          </select>
          <span>{placeWords[place].hint}</span>
        </label>
        {place === "block" && (
          <label className="col-span-2 flex flex-col gap-1 text-xs text-muted-foreground sm:col-span-1">
            <span className="flex justify-between">
              Share <span className="text-foreground">{sharePct}%</span>
            </span>
            <input
              type="range"
              min={5}
              max={95}
              step={5}
              aria-label={`Share of ${p.name}`}
              value={sharePct ?? 100}
              disabled={others <= 0}
              title={others <= 0 ? "The only block takes all the land left" : undefined}
              onChange={(e) => onChange({ ...p, share: shareFor(Number(e.target.value), others) })}
              className="mt-2 accent-[#4d7c0f] disabled:opacity-50"
            />
          </label>
        )}
        {place !== "block" && (
          <label className="flex flex-col gap-1 text-xs text-muted-foreground">
            Band width (m)
            <Input
              type="number"
              min={0.5}
              max={30}
              step={0.5}
              className="h-9"
              value={p.width_m ?? ""}
              onChange={(e) => onChange({ ...p, width_m: Number(e.target.value) || undefined })}
            />
          </label>
        )}
        {place === "alley" && (
          <label className="flex flex-col gap-1 text-xs text-muted-foreground">
            Rows every (m)
            <Input
              type="number"
              min={4}
              max={100}
              className="h-9"
              value={p.every_m ?? ""}
              onChange={(e) => onChange({ ...p, every_m: Number(e.target.value) || undefined })}
            />
          </label>
        )}
      </div>
    </div>
  );
}

function Legend({
  draft,
  result,
  highlight,
  onHighlight,
}: {
  draft: Distribution;
  result: DistributionResult;
  highlight?: string;
  onHighlight: (id?: string) => void;
}) {
  return (
    <ul className="flex flex-wrap gap-x-4 gap-y-1.5 border-t px-3 py-2.5 text-sm" aria-label="Legend">
      {result.plants.map((m) => {
        const p = draft.plants.find((x) => x.id === m.plant);
        return (
          <li key={m.plant}>
            <button
              className="flex items-center gap-2 data-[on=true]:font-medium"
              data-on={highlight === m.plant}
              onMouseEnter={() => onHighlight(m.plant)}
              onMouseLeave={() => onHighlight(undefined)}
              onFocus={() => onHighlight(m.plant)}
              onBlur={() => onHighlight(undefined)}
            >
              <span
                className="size-3 rounded-sm"
                style={{ background: plantColor(draft, m.plant) }}
                aria-hidden="true"
              />
              {p?.name ?? m.plant}
              <span className="text-muted-foreground">{Math.round(m.share * 100)}%</span>
            </button>
          </li>
        );
      })}
    </ul>
  );
}

function SeverityIcon({ e }: { e: Effect }) {
  if (e.severity === "error") return <XCircle className="size-4 text-ak-bad" aria-label="Problem" />;
  if (e.severity === "warning") return <AlertTriangle className="size-4 text-ak-warn" aria-label="Warning" />;
  return <Info className="size-4 text-ak-muted" aria-label="Note" />;
}

/** What the arrangement does, problems first, each with its kind's icon. */
export function Effects({
  effects,
  draft,
  status,
}: {
  effects: Effect[];
  draft?: Distribution;
  status: DistributionStatus;
}) {
  const sorted = sortEffects(effects);
  return (
    <section
      aria-label="What this arrangement does"
      className="flex flex-col gap-3 rounded-xl border bg-ak-surface p-4 shadow-sm"
    >
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h2 className="font-semibold">What this arrangement does</h2>
        <Status tone={statusTone[status]}>{statusWords[status]}</Status>
      </div>
      {sorted.length === 0 ? (
        <p className="text-sm text-muted-foreground">Nothing to watch: no shade, edge, water or neighbour problems.</p>
      ) : (
        <ul className="flex flex-col divide-y">
          {sorted.map((e, i) => {
            const Icon = kindIcon[e.kind] ?? Info;
            return (
              <li
                key={i}
                className="flex gap-3 py-2.5 first:pt-0 last:pb-0"
                data-effect={e.id}
                data-severity={e.severity}
              >
                <span
                  className="mt-0.5 flex size-8 shrink-0 items-center justify-center rounded-full"
                  style={{ background: e.plant && draft ? `${plantColor(draft, e.plant)}22` : undefined }}
                >
                  <Icon className="size-4" aria-hidden="true" />
                </span>
                <div className="flex min-w-0 flex-col gap-0.5 text-sm">
                  <span className="flex items-center gap-1.5 text-xs font-medium text-muted-foreground">
                    <SeverityIcon e={e} /> {effectWords[e.kind]}
                  </span>
                  <span>{e.message}</span>
                </div>
              </li>
            );
          })}
        </ul>
      )}
    </section>
  );
}

/** Each plant group's numbers: land, plants, shade and edge. */
function Measures({ result, draft, crops }: { result: DistributionResult; draft: Distribution; crops: Crop[] }) {
  const name = (id: string) => crops.find((c) => c.id === id)?.name ?? id;
  return (
    <section aria-label="Numbers" className="grid gap-3 sm:grid-cols-2 xl:grid-cols-3">
      {result.plants.map((m) => {
        const p = draft.plants.find((x) => x.id === m.plant);
        const color = plantColor(draft, m.plant);
        return (
          <article
            key={m.plant}
            className="flex flex-col gap-2 rounded-xl border bg-ak-surface p-3 shadow-sm"
            data-measure={m.plant}
          >
            <div className="flex items-center gap-2">
              <span className="size-3 rounded-sm" style={{ background: color }} aria-hidden="true" />
              <h3 className="truncate text-sm font-medium">{p?.name ?? m.plant}</h3>
              <span className="ml-auto text-xs text-muted-foreground">
                {p ? placeWords[p.place ?? "block"].label : ""}
              </span>
            </div>
            <p className="text-xl font-semibold tabular-nums">
              {area(m.area_m2)}{" "}
              <span className="text-sm font-normal text-muted-foreground">{Math.round(m.share * 100)}%</span>
            </p>
            <div className="h-1.5 overflow-hidden rounded-full bg-ak-bg" aria-hidden="true">
              <div
                className="h-full rounded-full"
                style={{ width: `${Math.min(100, m.share * 100)}%`, background: color }}
              />
            </div>
            <dl className="grid grid-cols-2 gap-x-2 gap-y-0.5 text-xs">
              {m.counts.map((c) => (
                <div key={c.crop} className="contents">
                  <dt className="text-muted-foreground">{name(c.crop)}</dt>
                  <dd className="text-right tabular-nums">{c.plants.toLocaleString()}</dd>
                </div>
              ))}
              {m.shaded_m2 > 0 && (
                <>
                  <dt className="text-muted-foreground">In shade</dt>
                  <dd className="text-right tabular-nums">{Math.round(m.shaded_m2).toLocaleString()} m²</dd>
                </>
              )}
              {p?.place !== "border" && (
                <>
                  <dt className="text-muted-foreground">Near the edge</dt>
                  <dd className="text-right tabular-nums">{Math.round(m.edge_share * 100)}%</dd>
                </>
              )}
              <dt className="text-muted-foreground">Height</dt>
              <dd className="text-right tabular-nums">{m.height_m} m</dd>
            </dl>
          </article>
        );
      })}
    </section>
  );
}

/** Name a group and pick its plots; plots in another group say which. */
export function GroupForm({
  group,
  farms,
  groups,
  onSaved,
  onCancel,
}: {
  group?: Group;
  farms: Farm[];
  groups: Group[];
  onSaved: (g: Group) => void;
  onCancel: () => void;
}) {
  const [name, setName] = useState(group?.name ?? "");
  const [plots, setPlots] = useState<string[]>(group?.plots ?? []);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const elsewhere = (id: string) => groups.find((g) => g.id !== group?.id && g.plots.includes(id));
  const save = () => {
    setBusy(true);
    setError("");
    const d = { name, plots };
    (group ? api.updateGroup(group.id, d) : api.createGroup(d))
      .then(onSaved, (e: unknown) => setError(message(e)))
      .finally(() => setBusy(false));
  };
  return (
    <form
      className="flex max-w-2xl flex-col gap-5"
      onSubmit={(e) => {
        e.preventDefault();
        save();
      }}
    >
      <label className="flex flex-col gap-1.5 text-sm font-medium">
        Group name
        <Input
          autoFocus
          value={name}
          placeholder="Home farm, River fields, …"
          onChange={(e) => setName(e.target.value)}
          className="h-10 text-base"
        />
      </label>
      <fieldset className="flex flex-col gap-2">
        <legend className="mb-2 text-sm font-medium">Plots in this group</legend>
        {farms.length === 0 && <p className="text-sm text-muted-foreground">Add a plot first.</p>}
        <ul className="grid gap-2 sm:grid-cols-2">
          {farms.map((f) => {
            const other = elsewhere(f.id);
            const on = plots.includes(f.id);
            return (
              <li key={f.id}>
                <label className="flex cursor-pointer items-center gap-3 rounded-xl border p-2.5 transition-colors has-checked:border-ak-ok/60 has-checked:bg-ak-ok/5 has-disabled:cursor-not-allowed has-disabled:opacity-60">
                  <input
                    type="checkbox"
                    className="size-4 accent-[#4d7c0f]"
                    checked={on}
                    disabled={!!other}
                    onChange={(e) =>
                      setPlots((ps) => (e.target.checked ? [...ps, f.id] : ps.filter((x) => x !== f.id)))
                    }
                  />
                  <PlotShape farms={[f]} className="size-10 shrink-0 rounded-md bg-ak-bg p-1" />
                  <span className="flex min-w-0 flex-col">
                    <span className="truncate text-sm font-medium">{f.name}</span>
                    <span className="text-xs text-muted-foreground">
                      {f.report.measures?.acres_text ?? ""}
                      {other ? ` · in ${other.name}` : ""}
                    </span>
                  </span>
                </label>
              </li>
            );
          })}
        </ul>
      </fieldset>
      {error && (
        <p role="alert" className="text-sm text-ak-bad">
          {error}
        </p>
      )}
      <div className="flex gap-2">
        <Button type="submit" disabled={busy || name.trim() === ""}>
          {group ? "Save group" : "Create group"}
        </Button>
        <Button type="button" variant="ghost" onClick={onCancel}>
          Cancel
        </Button>
      </div>
    </form>
  );
}
