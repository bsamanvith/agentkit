// The roadmap tab is a lazy chunk: its types, calls and the phone's queue
// of unsent changes live here, not in api.ts and types.ts, so the first
// load does not grow.

export interface Actuals {
  quantity?: number;
  unit?: string;
  cost_inr?: number;
  workers?: number;
}

export type TaskState = "open" | "done" | "skipped" | "not_needed";
export type EventKind = "done" | "skipped" | "not_needed" | "reopened" | "added" | "removed";

export interface Task {
  id: string;
  source: "calendar" | "farmer";
  zone: number;
  crop?: string;
  kind: string;
  phase: "prepare" | "sow" | "grow" | "harvest";
  title: string;
  detail?: string;
  date: string;
  das?: number;
  stage?: string;
  state: TaskState;
  day?: string;
  by?: string;
  note?: string;
  actuals?: Actuals;
  late?: boolean;
  overdue?: boolean;
}

export interface RoadmapZone {
  zone: number;
  crop: string;
  crop_name: string;
  with?: string;
  planned_sow: string;
  sown?: string;
  harvest_from: string;
  harvest_by: string;
}

export interface Roadmap {
  farm: string;
  today: string;
  zones: RoadmapZone[];
  tasks: Task[];
  counts: { open: number; done: number; skipped: number; not_needed: number; overdue: number };
  notes?: string[];
}

export interface WorkEvent {
  id: string;
  task?: string;
  kind: EventKind;
  day?: string;
  at?: string;
  by?: string;
  note?: string;
  actuals?: Actuals;
  title?: string;
  task_kind?: string;
  zone?: number;
}

/** An error the server gave: the change itself is wrong, resending won't help. */
export class Refused extends Error {
  constructor(
    msg: string,
    readonly status: number,
    /** The one change the server refused, when it said which. */
    readonly eventID?: string,
  ) {
    super(msg);
  }
}

// Answers worth trying again later, like a lost connection.
const retryable = new Set([408, 425, 429]);

async function call<T>(path: string, init?: RequestInit): Promise<T> {
  const resp = await fetch(path, init);
  const data = (await resp.json().catch(() => ({}))) as { error?: string; event_id?: string };
  if (resp.status >= 400 && resp.status < 500 && !retryable.has(resp.status))
    throw new Refused(data.error ?? `HTTP ${resp.status}`, resp.status, data.event_id);
  if (!resp.ok) throw new Error(data.error ?? `HTTP ${resp.status}`);
  return data as T;
}

export const roadmapApi = {
  get: (farm: string, signal?: AbortSignal) =>
    call<Roadmap>(`/api/farms/${encodeURIComponent(farm)}/roadmap`, { signal }),
  record: (farm: string, events: WorkEvent[]) =>
    call<{ added: number; roadmap: Roadmap }>(`/api/farms/${encodeURIComponent(farm)}/roadmap/events`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ events }),
    }),
};

export function newID(): string {
  const c = globalThis.crypto;
  if (c && typeof c.randomUUID === "function") return "web-" + c.randomUUID();
  return "web-" + Date.now().toString(36) + "-" + Math.random().toString(36).slice(2, 10);
}

// The queue keeps changes made without a connection on the phone, per
// plot, until the server has them. Events carry their own IDs, so sending
// one twice changes nothing.
const queueKey = (farm: string) => `farm.roadmap.queue.${farm}`;

export function queued(farm: string): WorkEvent[] {
  try {
    const raw = localStorage.getItem(queueKey(farm));
    const v: unknown = raw ? JSON.parse(raw) : [];
    return Array.isArray(v) ? (v as WorkEvent[]) : [];
  } catch {
    return [];
  }
}

function save(farm: string, events: WorkEvent[]) {
  try {
    if (events.length === 0) localStorage.removeItem(queueKey(farm));
    else localStorage.setItem(queueKey(farm), JSON.stringify(events));
  } catch {
    // Storage off or full: the change is sent now or lost with the page.
  }
}

export function enqueue(farm: string, e: WorkEvent) {
  save(farm, [...queued(farm), e]);
}

/** The most changes one request carries (the server takes up to 500). */
export const chunk = 200;

function drop(farm: string, ids: Set<string>) {
  save(
    farm,
    queued(farm).filter((e) => !ids.has(e.id)),
  );
}

/**
 * flush sends the plot's queued changes, a chunk at a time. Sent changes
 * leave the queue. On a network failure (or a busy server) the rest stay
 * for the next try. When the server refuses one change it says which: only
 * that one is dropped and the others are sent again; the refusals are
 * thrown together at the end. If the plot is gone, the queue goes with it.
 */
export async function flush(farm: string): Promise<Roadmap | undefined> {
  let latest: Roadmap | undefined;
  const refusals: string[] = [];
  // Each round sends a chunk or drops a change, so this ends.
  for (let round = 0; round < 10_000; round++) {
    const before = queued(farm);
    const events = before.slice(0, chunk);
    if (events.length === 0) break;
    try {
      latest = (await roadmapApi.record(farm, events)).roadmap;
      drop(farm, new Set(events.map((e) => e.id)));
    } catch (e) {
      if (!(e instanceof Refused)) throw e;
      if (e.eventID && events.some((x) => x.id === e.eventID)) {
        refusals.push(e.message);
        drop(farm, new Set([e.eventID]));
        continue;
      }
      // Refused without saying which (the plot is gone, or the request
      // is malformed): resending this chunk cannot help.
      refusals.push(e.message);
      drop(farm, new Set(events.map((x) => x.id)));
      if (e.status === 404) drop(farm, new Set(queued(farm).map((x) => x.id)));
    }
    // Storage that cannot be written never shrinks: stop rather than resend.
    if (queued(farm).length >= before.length) break;
  }
  if (refusals.length > 0) throw new Refused(refusals.join("; "), 400);
  return latest;
}

/** applyQueued shows the queued changes on the roadmap before the server has them. */
export function applyQueued(r: Roadmap, events: WorkEvent[]): Roadmap {
  if (events.length === 0) return r;
  let tasks = r.tasks.map((t) => ({ ...t }));
  for (const e of events) {
    if (e.kind === "added") {
      tasks.push({
        id: e.id,
        source: "farmer",
        zone: e.zone ?? -1,
        kind: e.task_kind ?? "other",
        phase: "grow",
        title: e.title ?? "",
        detail: e.note,
        date: e.day ?? r.today,
        state: "open",
      });
      continue;
    }
    if (e.kind === "removed") {
      tasks = tasks.filter((t) => t.id !== e.task);
      continue;
    }
    const t = tasks.find((x) => x.id === e.task);
    if (!t) continue;
    if (e.kind === "reopened") Object.assign(t, { state: "open", day: undefined, by: undefined, actuals: undefined });
    else if (t.state === "open")
      Object.assign(t, { state: e.kind, day: e.day, by: e.by, note: e.note, actuals: e.actuals, overdue: false });
  }
  tasks.sort((a, b) => (a.date < b.date ? -1 : a.date > b.date ? 1 : a.zone - b.zone));
  return { ...r, tasks };
}

export const phaseNames: Record<Task["phase"], string> = {
  prepare: "Prepare the land",
  sow: "Sowing",
  grow: "Growing",
  harvest: "Harvest",
};

export const kindNames: Record<string, string> = {
  prepare: "Preparing",
  sow: "Sowing",
  weed: "Weeding",
  fertilise: "Fertiliser",
  irrigate: "Water",
  scout: "Field walk",
  protect: "Spraying",
  prune: "Pruning",
  harvest: "Harvest",
  other: "Other",
};

export function dayLabel(day: string): string {
  const d = new Date(day + "T00:00:00");
  if (Number.isNaN(d.getTime())) return day;
  return d.toLocaleDateString("en-IN", { weekday: "short", day: "numeric", month: "short" });
}

export function addDays(day: string, n: number): string {
  const d = new Date(day + "T00:00:00Z");
  d.setUTCDate(d.getUTCDate() + n);
  return d.toISOString().slice(0, 10);
}
