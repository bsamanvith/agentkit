import { Button } from "@agentkit/ui/components/button";
import { Status } from "@agentkit/ui";
import { AlertTriangle, Check, CloudOff, Plus, RotateCcw, SkipForward, X } from "lucide-react";
import { useCallback, useEffect, useMemo, useState, type ReactNode } from "react";
import {
  addDays,
  applyQueued,
  dayLabel,
  enqueue,
  flush,
  kindNames,
  newID,
  phaseNames,
  queued,
  Refused,
  roadmapApi,
  type Roadmap as RoadmapData,
  type Task,
  type WorkEvent,
} from "./roadmap-data";
import type { Farm } from "./types";

function message(e: unknown): string {
  return e instanceof Error ? e.message : String(e);
}

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

const stateText: Record<string, string> = { done: "Done", skipped: "Skipped", not_needed: "Not needed" };

/** The plot's season roadmap: every task of its plan, ticked off as the farmer goes. */
export function Roadmap({ farm }: { farm: Farm }) {
  const [data, setData] = useState<RoadmapData>();
  const [pending, setPending] = useState<WorkEvent[]>(() => queued(farm.id));
  const [error, setError] = useState("");
  const [offline, setOffline] = useState(false);
  const [all, setAll] = useState(false);
  const [adding, setAdding] = useState(false);

  const sync = useCallback(async () => {
    try {
      const r = await flush(farm.id);
      if (r) setData(r);
      setOffline(false);
    } catch (e) {
      if (e instanceof Refused) setError(`A change was not accepted: ${message(e)}`);
      else setOffline(true);
    }
    setPending(queued(farm.id));
  }, [farm.id]);

  useEffect(() => {
    const ctl = new AbortController();
    roadmapApi.get(farm.id, ctl.signal).then(
      (r) => {
        setData(r);
        void sync();
      },
      (e: unknown) => {
        if (ctl.signal.aborted) return;
        if (e instanceof Refused) setError(message(e));
        else setOffline(true);
      },
    );
    const online = () => void sync();
    window.addEventListener("online", online);
    return () => {
      ctl.abort();
      window.removeEventListener("online", online);
    };
  }, [farm.id, sync]);

  const record = (e: WorkEvent) => {
    setError("");
    enqueue(farm.id, { ...e, at: new Date().toISOString() });
    setPending(queued(farm.id));
    void sync();
  };

  const view = useMemo(() => (data ? applyQueued(data, pending) : undefined), [data, pending]);
  if (error && !view)
    return (
      <p role="alert" className="text-sm text-ak-bad">
        {error}
      </p>
    );
  if (!view)
    return (
      <p className="text-sm text-muted-foreground">
        {offline ? "No connection: the roadmap will load when you are online." : "Loading the season…"}
      </p>
    );

  const soon = addDays(view.today, 14);
  // Now: what is open up to two weeks ahead (overdue included), and what
  // was closed in the last week, so a tick stays in sight.
  const recent = addDays(view.today, -7);
  const shown = view.tasks.filter((t) => all || (t.state === "open" ? t.date <= soon : (t.day ?? t.date) >= recent));
  const crop = (t: Task) => view.zones.find((z) => z.zone === t.zone)?.crop_name;
  const phases = (["prepare", "sow", "grow", "harvest"] as const).filter((p) => shown.some((t) => t.phase === p));
  return (
    <div className="flex flex-col gap-4">
      <Card
        title="This season"
        aside={
          pending.length > 0 && (
            <span className="flex items-center gap-1 text-xs text-muted-foreground">
              <CloudOff className="size-3.5" aria-hidden="true" />
              {pending.length} {pending.length === 1 ? "change" : "changes"} saved on this phone
              {offline ? ", will send when online" : ""}
            </span>
          )
        }
      >
        <ul className="flex flex-col gap-1 text-sm">
          {view.zones.map((z) => (
            <li key={z.zone}>
              <span className="font-medium">
                {z.crop_name}
                {z.with ? ` with ${z.with}` : ""}
              </span>
              {" · "}
              {z.sown ? `sown ${dayLabel(z.sown)}` : `sowing planned ${dayLabel(z.planned_sow)}`}
              {" · harvest "}
              {dayLabel(z.harvest_from)} to {dayLabel(z.harvest_by)}
            </li>
          ))}
        </ul>
        <p className="mt-2 text-sm text-muted-foreground">
          {view.counts.done} done · {view.counts.open} to do
          {view.counts.overdue > 0 ? ` · ${view.counts.overdue} past their date` : ""}
        </p>
        {error && (
          <p role="alert" className="mt-2 text-sm text-ak-bad">
            {error}
          </p>
        )}
      </Card>
      <div className="flex flex-wrap items-center gap-2">
        <div role="group" aria-label="Which tasks" className="flex gap-1">
          <Button size="sm" variant={all ? "outline" : "default"} onClick={() => setAll(false)}>
            Now and next two weeks
          </Button>
          <Button size="sm" variant={all ? "default" : "outline"} onClick={() => setAll(true)}>
            Whole season
          </Button>
        </div>
        <Button size="sm" variant="outline" onClick={() => setAdding((a) => !a)}>
          <Plus /> My own task
        </Button>
      </div>
      {adding && (
        <AddTask
          today={view.today}
          zones={view.zones.map((z) => ({ zone: z.zone, name: z.crop_name }))}
          onAdd={(e) => {
            record(e);
            setAdding(false);
          }}
          onCancel={() => setAdding(false)}
        />
      )}
      {shown.length === 0 && <p className="text-sm text-muted-foreground">Nothing to do in the next two weeks.</p>}
      {phases.map((p) => (
        <Card key={p} title={phaseNames[p]}>
          <ul className="flex flex-col divide-y">
            {shown
              .filter((t) => t.phase === p)
              .map((t) => (
                <TaskRow key={t.id} task={t} crop={crop(t)} today={view.today} onRecord={record} />
              ))}
          </ul>
        </Card>
      ))}
      {view.notes?.map((n) => (
        <p key={n} className="text-xs text-muted-foreground">
          {n}
        </p>
      ))}
    </div>
  );
}

function TaskRow({
  task: t,
  crop,
  today,
  onRecord,
}: {
  task: Task;
  crop?: string;
  today: string;
  onRecord: (e: WorkEvent) => void;
}) {
  const [form, setForm] = useState<"done" | "skipped" | undefined>();
  const [more, setMore] = useState(false);
  const closed = t.state !== "open";
  return (
    <li className="flex flex-col gap-2 py-3">
      <div className="flex flex-wrap items-start justify-between gap-2">
        <div className="min-w-0">
          <p className={closed ? "text-sm text-muted-foreground line-through" : "text-sm font-medium"}>{t.title}</p>
          <p className="text-xs text-muted-foreground">
            {dayLabel(t.date)}
            {crop ? ` · ${crop}` : ""}
            {` · ${kindNames[t.kind] ?? t.kind}`}
            {t.source === "farmer" ? " · my task" : ""}
          </p>
        </div>
        {closed ? (
          <span className="flex items-center gap-2">
            <Status tone={t.state === "done" ? "ok" : "neutral"}>
              {stateText[t.state]}
              {t.day ? ` ${dayLabel(t.day)}` : ""}
              {t.late ? " (late)" : ""}
            </Status>
            <Button
              size="sm"
              variant="ghost"
              aria-label={`Undo: ${t.title}`}
              onClick={() => onRecord({ id: newID(), task: t.id, kind: "reopened" })}
            >
              <RotateCcw />
            </Button>
          </span>
        ) : (
          !form && (
            <span className="flex gap-1">
              <Button size="sm" onClick={() => setForm("done")}>
                <Check /> {t.kind === "sow" && t.source === "calendar" ? "Sown" : "Done"}
              </Button>
              <Button size="sm" variant="outline" aria-label={`Skip: ${t.title}`} onClick={() => setForm("skipped")}>
                <SkipForward />
              </Button>
            </span>
          )
        )}
      </div>
      {t.overdue && !form && (
        <p className="flex items-center gap-1 text-xs text-ak-warn">
          <AlertTriangle className="size-3.5" aria-hidden="true" /> Past its date: did you do it?
        </p>
      )}
      {t.detail && (
        <button className="self-start text-left text-xs text-muted-foreground" onClick={() => setMore((m) => !m)}>
          {more ? t.detail : "How to do it…"}
        </button>
      )}
      {t.note && closed && <p className="text-xs text-muted-foreground">Note: {t.note}</p>}
      {form && (
        <CloseForm
          task={t}
          kind={form}
          today={today}
          onSave={(e) => {
            onRecord(e);
            setForm(undefined);
          }}
          onCancel={() => setForm(undefined)}
        />
      )}
    </li>
  );
}

function num(v: string): number | undefined {
  const n = Number(v);
  return v.trim() === "" || !Number.isFinite(n) || n < 0 ? undefined : n;
}

function CloseForm({
  task,
  kind,
  today,
  onSave,
  onCancel,
}: {
  task: Task;
  kind: "done" | "skipped";
  today: string;
  onSave: (e: WorkEvent) => void;
  onCancel: () => void;
}) {
  const [day, setDay] = useState(today);
  const [workers, setWorkers] = useState("");
  const [cost, setCost] = useState("");
  const [note, setNote] = useState("");
  const [why, setWhy] = useState<"skipped" | "not_needed">("skipped");
  const sowing = task.kind === "sow" && task.source === "calendar";
  return (
    <form
      className="grid gap-2 rounded-lg border bg-ak-bg p-3 text-sm sm:grid-cols-2"
      onSubmit={(ev) => {
        ev.preventDefault();
        const w = num(workers);
        const c = num(cost);
        onSave({
          id: newID(),
          task: task.id,
          kind: kind === "done" ? "done" : why,
          day,
          note: note.trim() || undefined,
          actuals: kind === "done" && (w !== undefined || c !== undefined) ? { workers: w, cost_inr: c } : undefined,
        });
      }}
    >
      <label className="flex flex-col gap-1">
        {sowing ? "Sown on" : kind === "done" ? "Done on" : "On"}
        <input
          type="date"
          required
          max={today}
          value={day}
          onChange={(e) => setDay(e.target.value)}
          className="rounded border px-2 py-1"
        />
      </label>
      {kind === "done" ? (
        <>
          <label className="flex flex-col gap-1">
            Workers
            <input
              inputMode="numeric"
              value={workers}
              onChange={(e) => setWorkers(e.target.value)}
              className="rounded border px-2 py-1"
            />
          </label>
          <label className="flex flex-col gap-1">
            Cost (₹)
            <input
              inputMode="decimal"
              value={cost}
              onChange={(e) => setCost(e.target.value)}
              className="rounded border px-2 py-1"
            />
          </label>
        </>
      ) : (
        <label className="flex flex-col gap-1">
          Why
          <select
            value={why}
            onChange={(e) => setWhy(e.target.value as "skipped" | "not_needed")}
            className="rounded border px-2 py-1"
          >
            <option value="skipped">Could not do it</option>
            <option value="not_needed">Not needed</option>
          </select>
        </label>
      )}
      <label className="flex flex-col gap-1 sm:col-span-2">
        Note
        <input
          value={note}
          maxLength={500}
          onChange={(e) => setNote(e.target.value)}
          className="rounded border px-2 py-1"
        />
      </label>
      {sowing && (
        <p className="text-xs text-muted-foreground sm:col-span-2">
          The rest of the season&apos;s work moves to follow the day you sowed.
        </p>
      )}
      <div className="flex gap-2 sm:col-span-2">
        <Button size="sm" type="submit">
          Save
        </Button>
        <Button size="sm" type="button" variant="ghost" onClick={onCancel}>
          <X /> Cancel
        </Button>
      </div>
    </form>
  );
}

function AddTask({
  today,
  zones,
  onAdd,
  onCancel,
}: {
  today: string;
  zones: { zone: number; name: string }[];
  onAdd: (e: WorkEvent) => void;
  onCancel: () => void;
}) {
  const [title, setTitle] = useState("");
  const [day, setDay] = useState(today);
  const [kind, setKind] = useState("other");
  const [zone, setZone] = useState(-1);
  return (
    <form
      className="grid gap-2 rounded-xl border bg-ak-surface p-4 text-sm shadow-sm sm:grid-cols-2"
      onSubmit={(ev) => {
        ev.preventDefault();
        if (!title.trim()) return;
        onAdd({
          id: newID(),
          kind: "added",
          title: title.trim(),
          day,
          task_kind: kind,
          zone: zone >= 0 ? zone : undefined,
        });
      }}
    >
      <label className="flex flex-col gap-1 sm:col-span-2">
        What
        <input
          required
          maxLength={120}
          value={title}
          onChange={(e) => setTitle(e.target.value)}
          placeholder="Repair the motor"
          className="rounded border px-2 py-1"
        />
      </label>
      <label className="flex flex-col gap-1">
        When
        <input
          type="date"
          required
          value={day}
          onChange={(e) => setDay(e.target.value)}
          className="rounded border px-2 py-1"
        />
      </label>
      <label className="flex flex-col gap-1">
        Kind
        <select value={kind} onChange={(e) => setKind(e.target.value)} className="rounded border px-2 py-1">
          {Object.entries(kindNames).map(([k, v]) => (
            <option key={k} value={k}>
              {v}
            </option>
          ))}
        </select>
      </label>
      {zones.length > 1 && (
        <label className="flex flex-col gap-1">
          Crop
          <select value={zone} onChange={(e) => setZone(Number(e.target.value))} className="rounded border px-2 py-1">
            <option value={-1}>Whole plot</option>
            {zones.map((z) => (
              <option key={z.zone} value={z.zone}>
                {z.name}
              </option>
            ))}
          </select>
        </label>
      )}
      <div className="flex gap-2 sm:col-span-2">
        <Button size="sm" type="submit">
          Add
        </Button>
        <Button size="sm" type="button" variant="ghost" onClick={onCancel}>
          Cancel
        </Button>
      </div>
    </form>
  );
}
