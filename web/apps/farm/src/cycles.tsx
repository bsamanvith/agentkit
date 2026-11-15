import { Button } from "@agentkit/ui/components/button";
import { AlertTriangle, Info, Pencil, Sprout, X } from "lucide-react";
import { lazy, Suspense, useEffect, useState, type ReactNode } from "react";
import { api, ApiError, call } from "./api";
import {
  cropsOf,
  addDays,
  cyclesApi,
  dayLabel,
  edited,
  sameCycle,
  span,
  type Cycle,
  type Line,
  type Method,
  type Position,
  type WeekTask,
} from "./cycles-data";
import type { Crop, Farm, Plan } from "./types";

// The plot's crop cycles: the current one first (crop, dates, this week's
// work: the basic path), then, one tap away, our suggestion beside the
// farmer's own for up to three cycles. Nothing here is required.

// What to buy, feeding and sprays, and when to sell: optional, collapsed,
// in a chunk of their own that loads after the cycle shows.
const Extras = lazy(() => import("./cycle-extras"));

function message(e: unknown): string {
  if (e instanceof ApiError && e.planReport) {
    const errs = e.planReport.checks.filter((c) => c.severity === "error").map((c) => c.message);
    if (errs.length > 0) return errs.join(" ");
  }
  return e instanceof Error ? e.message : String(e);
}

const names: Record<number, string> = { 0: "This cycle", 1: "Next cycle", 2: "The one after" };

function Card({ title, children, aside }: { title: string; children: ReactNode; aside?: ReactNode }) {
  return (
    <section className="rounded-xl border bg-ak-surface p-4 shadow-sm">
      <div className="mb-3 flex flex-wrap items-baseline justify-between gap-2">
        <h2 className="text-base font-semibold">{title}</h2>
        {aside}
      </div>
      {children}
    </section>
  );
}

/** What a cycle is: crops, sowing and harvest, in the farmer's words. */
function Summary({ c }: { c: Cycle }) {
  return (
    <div className="flex flex-col gap-0.5 text-sm">
      <p className="font-medium">{cropsOf(c)}</p>
      <p>
        {c.sow_kind === "actual" ? "Sown " : "Sow "}
        {dayLabel(c.sow)}
        {" · harvest around "}
        {dayLabel(c.harvest)}
      </p>
      {c.areas.length > 1 && (
        <ul className="text-xs text-muted-foreground">
          {c.areas.map((a) => (
            <li key={a.zone}>
              Area {a.zone + 1}: {a.crop_name} · {a.area_ha} ha · sow {dayLabel(a.sow)}
            </li>
          ))}
        </ul>
      )}
      {c.method === "organic" && <p className="text-xs text-[#4d7c0f]">Organic</p>}
    </div>
  );
}

/** The plot's cycles (the default export, for React.lazy). */
export default function Cycles({ farm, onFarm }: { farm: Farm; onFarm?: (f: Farm) => void }) {
  const [line, setLine] = useState<Line>();
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [more, setMore] = useState(false);

  useEffect(() => {
    const ctl = new AbortController();
    cyclesApi.get(farm.id, ctl.signal).then(setLine, (e: unknown) => {
      if (!ctl.signal.aborted) setError(message(e));
    });
    return () => ctl.abort();
  }, [farm.id]);

  const run = async (f: () => Promise<Line>, savesPlan = false) => {
    setBusy(true);
    setError("");
    try {
      setLine(await f());
      // The current cycle is the saved plan: the other tabs follow it.
      if (savesPlan && onFarm) await api.farm(farm.id).then(onFarm, () => undefined);
    } catch (e) {
      setError(message(e));
    } finally {
      setBusy(false);
    }
  };

  if (!line)
    return error ? (
      <p role="alert" className="text-sm text-ak-bad">
        {error}
      </p>
    ) : (
      <p className="text-sm text-muted-foreground">Working out the cycles…</p>
    );

  const [now, ...later] = line.cycles;
  return (
    <div className="flex flex-col gap-4">
      {error && (
        <p role="alert" className="text-sm text-ak-bad">
          {error}
        </p>
      )}
      {now && <Now line={line} p={now} farm={farm} busy={busy} run={run} />}
      {line.cycles.length > 1 && <Timeline line={line} />}
      {later.length > 0 &&
        (more ? (
          later.map((p) => <Place key={p.position} farm={farm} p={p} busy={busy} run={run} />)
        ) : (
          <Button variant="outline" className="self-start" onClick={() => setMore(true)}>
            Plan the next {later.length === 1 ? "cycle" : `${later.length} cycles`}
          </Button>
        ))}
      {line.notes?.map((n) => (
        <p key={n.id} className="flex items-start gap-1.5 text-xs text-muted-foreground">
          <Info className="mt-0.5 size-3.5 shrink-0" aria-hidden="true" /> {n.message}
        </p>
      ))}
      <label className="flex items-center gap-2 text-sm text-muted-foreground">
        <input
          type="checkbox"
          checked={line.future}
          disabled={busy}
          onChange={(e) => void run(() => cyclesApi.future(farm.id, e.target.checked))}
        />
        Plan the cycles after this one
      </label>
    </div>
  );
}

type Run = (f: () => Promise<Line>, savesPlan?: boolean) => Promise<void>;

/** The current cycle: the basic path. */
function Now({
  line,
  p,
  farm,
  busy,
  run,
}: {
  line: Line;
  p: Position;
  farm: Farm;

  busy: boolean;
  run: Run;
}) {
  const [sowing, setSowing] = useState(false);
  const [day, setDay] = useState(line.today);
  const c = p.planned;
  const harvested = c && !c.perennial && c.harvest <= addDays(line.today, 14) && line.cycles.length > 1;
  return (
    <Card title="This cycle" aside={c && <Badge c={c} />}>
      {c ? <Summary c={c} /> : <p className="text-sm">Nothing is suggested now: say what you will grow.</p>}
      {c?.origin === "suggested" && c.reasons?.[0] && (
        <p className="mt-2 text-xs text-muted-foreground">Why: {c.reasons[0].message}</p>
      )}
      <Checks p={p} />
      <div className="mt-3 flex flex-wrap gap-2">
        {!p.own && p.suggested && (
          <Button
            size="sm"
            disabled={busy}
            onClick={() => void run(() => cyclesApi.set(farm.id, 0, { accept: true }), true)}
          >
            Use this
          </Button>
        )}
        <EditButton farm={farm} p={p} busy={busy} run={run} />
        {c && c.sow_kind !== "actual" && (
          <Button variant="outline" size="sm" disabled={busy} onClick={() => setSowing((s) => !s)}>
            <Sprout /> I already sowed on…
          </Button>
        )}
        {harvested && (
          <Button
            variant="outline"
            size="sm"
            disabled={busy}
            onClick={() => void run(() => cyclesApi.advance(farm.id), true)}
          >
            Harvested: start the next cycle
          </Button>
        )}
      </div>
      {sowing && (
        <form
          className="mt-3 flex flex-wrap items-end gap-2"
          onSubmit={(e) => {
            e.preventDefault();
            setSowing(false);
            void run(() => cyclesApi.sown(farm.id, day), true);
          }}
        >
          <label className="flex flex-col gap-1 text-sm">
            Sown on
            <input
              type="date"
              className="rounded-md border px-2 py-1"
              value={day}
              max={line.today}
              onChange={(e) => setDay(e.target.value)}
              required
            />
          </label>
          <Button type="submit" size="sm" disabled={busy}>
            Save
          </Button>
        </form>
      )}
      {p.own && p.suggested && !sameCycle(p.own, p.suggested) && (
        <p className="mt-3 text-xs text-muted-foreground">
          We would have suggested {cropsOf(p.suggested)}, sown {dayLabel(p.suggested.sow)}.
        </p>
      )}
      {farm.plan && farm.plan.zones.length > 0 && <ThisWeek farm={farm} />}
      {c && <MethodChoice farm={farm} p={p} busy={busy} run={run} />}
      <Suspense fallback={null}>
        <Extras addons={p.addons} />
      </Suspense>
    </Card>
  );
}

function Badge({ c }: { c: Cycle }) {
  return (
    <span className="rounded-full bg-ak-bg px-2 py-0.5 text-xs text-muted-foreground">
      {c.origin === "farmer" ? "Yours" : "Suggested"}
      {c.season ? ` · ${c.season}` : ""}
    </span>
  );
}

/** The current cycle's work in the next week, from the season roadmap. */
function ThisWeek({ farm }: { farm: Farm }) {
  const [tasks, setTasks] = useState<WeekTask[]>();
  useEffect(() => {
    const ctl = new AbortController();
    call<{ today: string; tasks: WeekTask[] }>(
      "GET",
      `/api/farms/${encodeURIComponent(farm.id)}/roadmap`,
      undefined,
      ctl.signal,
    ).then(
      (r) => setTasks(r.tasks.filter((t) => t.state === "open" && t.date <= addDays(r.today, 7))),
      () => setTasks(undefined), // the work list is extra: the cycle stands without it
    );
    return () => ctl.abort();
  }, [farm.id, farm.plan]);
  if (!tasks) return null;
  return (
    <div className="mt-3 border-t pt-3">
      <h3 className="text-sm font-medium">This week</h3>
      {tasks.length === 0 ? (
        <p className="text-sm text-muted-foreground">Nothing due.</p>
      ) : (
        <ul className="mt-1 flex flex-col gap-0.5 text-sm">
          {tasks.slice(0, 5).map((t) => (
            <li key={t.id}>
              {t.title} <span className="text-muted-foreground">· {t.overdue ? "overdue" : dayLabel(t.date)}</span>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

/** Warnings about the farmer's own cycle: words, never changes. */
function Checks({ p }: { p: Position }) {
  if (!p.checks?.length) return null;
  return (
    <ul className="mt-2 flex flex-col gap-1 text-sm">
      {p.checks.map((c, i) => (
        <li
          key={i}
          className={`flex items-start gap-1.5 ${c.severity === "info" ? "text-muted-foreground" : "text-ak-warn"}`}
        >
          {c.severity === "info" ? (
            <Info className="mt-0.5 size-4 shrink-0" aria-hidden="true" />
          ) : (
            <AlertTriangle className="mt-0.5 size-4 shrink-0" aria-label="Warning" />
          )}
          {c.message}
        </li>
      ))}
    </ul>
  );
}

function MethodChoice({ farm, p, busy, run }: { farm: Farm; p: Position; busy: boolean; run: Run }) {
  const m = p.planned?.method ?? "conventional";
  const pick = (to: Method) => void run(() => cyclesApi.set(farm.id, p.position, { method: to }));
  return (
    <div className="mt-3 flex flex-wrap items-center gap-1.5 text-sm">
      <span id={`method-${p.position}`} className="text-xs text-muted-foreground">
        Grown
      </span>
      <div className="flex gap-1.5" role="radiogroup" aria-labelledby={`method-${p.position}`}>
        {(["conventional", "organic"] as const).map((x) => (
          <button
            key={x}
            role="radio"
            aria-checked={m === x}
            disabled={busy || !p.planned}
            onClick={() => m !== x && pick(x)}
            className="min-h-9 rounded-full border px-3 py-1 capitalize aria-checked:border-[#4d7c0f] aria-checked:bg-[#4d7c0f]/10 aria-checked:font-medium aria-checked:text-[#3f6212] dark:aria-checked:text-[#bef264]"
          >
            {x}
          </button>
        ))}
      </div>
    </div>
  );
}

/** Change the crop and sowing date of each area of the planned cycle. */
function EditButton({ farm, p, busy, run }: { farm: Farm; p: Position; busy: boolean; run: Run }) {
  const [open, setOpen] = useState(false);
  if (!open)
    return (
      <Button variant="outline" size="sm" disabled={busy} onClick={() => setOpen(true)}>
        <Pencil /> {p.own ? "Change" : "My own crop"}
      </Button>
    );
  return (
    <EditForm
      p={p}
      busy={busy}
      onClose={() => setOpen(false)}
      onSave={(plan) => {
        setOpen(false);
        void run(() => cyclesApi.set(farm.id, p.position, { plan }), p.position === 0);
      }}
    />
  );
}

function EditForm({
  p,
  busy,
  onClose,
  onSave,
}: {
  p: Position;
  busy: boolean;
  onClose: () => void;
  onSave: (plan: Plan) => void;
}) {
  const [crops, setCrops] = useState<Crop[]>();
  // With nothing planned, one area: the server gives it the whole plot.
  const base: Plan = p.planned?.plan ?? { zones: [{ crop: "", sow: p.from, ring: [], row_bearing: 0 }] };
  const [areas, setAreas] = useState(base.zones.map((z) => ({ crop: z.crop, sow: z.sow })));
  useEffect(() => {
    api.crops().then(
      (cs) => setCrops(cs.filter((c) => !c.perennial || p.position === 0)),
      () => setCrops([]),
    );
  }, [p.position]);
  return (
    <form
      className="mt-2 flex w-full flex-col gap-2"
      onSubmit={(e) => {
        e.preventDefault();
        onSave(edited(base, areas));
      }}
    >
      {areas.map((a, i) => (
        <div key={i} className="flex flex-wrap items-end gap-2">
          <label className="flex min-w-40 flex-1 flex-col gap-1 text-sm">
            {areas.length > 1 ? `Area ${i + 1}: crop` : "Crop"}
            <select
              className="rounded-md border px-2 py-1"
              value={a.crop}
              required
              onChange={(e) => setAreas((xs) => xs.map((x, k) => (k === i ? { ...x, crop: e.target.value } : x)))}
            >
              <option value="">Choose…</option>
              {(crops ?? []).map((c) => (
                <option key={c.id} value={c.id}>
                  {c.name}
                </option>
              ))}
            </select>
          </label>
          <label className="flex flex-col gap-1 text-sm">
            Sow on
            <input
              type="date"
              className="rounded-md border px-2 py-1"
              value={a.sow}
              required
              onChange={(e) => setAreas((xs) => xs.map((x, k) => (k === i ? { ...x, sow: e.target.value } : x)))}
            />
          </label>
        </div>
      ))}
      <div className="flex gap-2">
        <Button type="submit" size="sm" disabled={busy || !crops}>
          Save
        </Button>
        <Button type="button" variant="ghost" size="sm" onClick={onClose}>
          <X /> Cancel
        </Button>
      </div>
    </form>
  );
}

/** A later cycle: our suggestion beside the farmer's own. */
function Place({ farm, p, busy, run }: { farm: Farm; p: Position; busy: boolean; run: Run }) {
  return (
    <Card
      title={names[p.position] ?? `Cycle ${p.position + 1}`}
      aside={<span className="text-xs text-muted-foreground">land free from {dayLabel(p.from)}</span>}
    >
      <div className="grid gap-3 sm:grid-cols-2">
        <div className={`rounded-lg border p-3 ${p.own ? "" : "border-[#4d7c0f]"}`}>
          <h3 className="mb-1 text-xs font-medium tracking-wide text-muted-foreground uppercase">Suggested</h3>
          {p.suggested ? (
            <>
              <Summary c={p.suggested} />
              {p.suggested.reasons?.slice(0, 2).map((r) => (
                <p key={r.id} className="mt-1 text-xs text-muted-foreground">
                  {r.message}
                </p>
              ))}
              {!sameCycle(p.own, p.suggested) && (
                <Button
                  size="sm"
                  className="mt-2"
                  disabled={busy}
                  onClick={() => void run(() => cyclesApi.set(farm.id, p.position, { accept: true }))}
                >
                  Use this
                </Button>
              )}
            </>
          ) : (
            <p className="text-sm text-muted-foreground">Nothing fits here now.</p>
          )}
        </div>
        <div className={`rounded-lg border p-3 ${p.own ? "border-[#4d7c0f]" : ""}`}>
          <h3 className="mb-1 text-xs font-medium tracking-wide text-muted-foreground uppercase">Yours</h3>
          {p.own ? (
            <Summary c={p.own} />
          ) : (
            <p className="text-sm text-muted-foreground">Not set: the suggestion is planned.</p>
          )}
          <Checks p={p} />
          <div className="mt-2 flex flex-wrap gap-2">
            <EditButton farm={farm} p={p} busy={busy} run={run} />
            {p.own && (
              <Button
                variant="ghost"
                size="sm"
                disabled={busy}
                onClick={() => void run(() => cyclesApi.clear(farm.id, p.position))}
              >
                Follow the suggestion
              </Button>
            )}
          </div>
        </div>
      </div>
      <MethodChoice farm={farm} p={p} busy={busy} run={run} />
    </Card>
  );
}

const barColors = ["bg-[#4d7c0f]", "bg-[#a16207]", "bg-[#0f766e]"];

/** The planned cycles on one strip of months. */
function Timeline({ line }: { line: Line }) {
  const planned = line.cycles.flatMap((p) => (p.planned ? [{ pos: p.position, c: p.planned }] : []));
  if (planned.length === 0) return null;
  const start = planned.reduce((m, { c }) => (c.sow < m ? c.sow : m), line.today);
  const end = planned.reduce((m, { c }) => (c.harvest > m ? c.harvest : m), line.today);
  return (
    <section aria-label="Timeline" className="rounded-xl border bg-ak-surface p-4 shadow-sm">
      <div className="relative h-8 overflow-hidden rounded-md bg-ak-bg">
        {planned.map(({ pos, c }) => {
          const s = span(start, end, c.sow, c.harvest);
          return (
            <div
              key={pos}
              className={`absolute top-1 bottom-1 truncate rounded px-1 text-[11px] leading-6 text-white ${barColors[pos % 3] ?? ""}`}
              style={{ left: `${s.left}%`, width: `${s.width}%` }}
              title={`${cropsOf(c)}: ${dayLabel(c.sow)} to ${dayLabel(c.harvest)}`}
            >
              {cropsOf(c)}
            </div>
          );
        })}
      </div>
      <div className="mt-1 flex justify-between text-xs text-muted-foreground">
        <span>{dayLabel(start)}</span>
        <span>{dayLabel(end)}</span>
      </div>
    </section>
  );
}
