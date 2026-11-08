import { Button } from "@agentkit/ui/components/button";
import { Status, type StatusTone } from "@agentkit/ui";
import {
  Bell,
  CalendarDays,
  CheckCheck,
  CloudRain,
  CloudSun,
  Play,
  Plus,
  Power,
  Sprout,
  Thermometer,
  Trash2,
  Wind,
} from "lucide-react";
import { useCallback, useEffect, useState, type ReactNode } from "react";
import { call } from "./api";
import { formatDate } from "./plan";
import type {
  AdviceSeverity,
  Automation,
  AutomationRun,
  CareTask,
  CropCalendar,
  Farm,
  Notice,
  WeatherDay,
  Week,
} from "./types";

// The routes only this page uses, here so they load with it.
const api = {
  week: (id: string) => call<Week>("GET", `/api/farms/${encodeURIComponent(id)}/week`),
  calendar: (id: string) => call<CropCalendar>("GET", `/api/farms/${encodeURIComponent(id)}/calendar`),
  notices: (farm?: string, unread = false) =>
    call<{ notices: Notice[]; unread: number }>(
      "GET",
      `/api/notices?farm=${encodeURIComponent(farm ?? "")}${unread ? "&unread=1" : ""}`,
    ),
  readNotices: (farm?: string, ids?: string[]) => call<{ marked: number }>("POST", "/api/notices/read", { farm, ids }),
  automations: () => call<{ automations: Automation[]; rules: Record<string, string> }>("GET", "/api/automations"),
  createAutomation: (a: Partial<Automation>) => call<Automation>("POST", "/api/automations", a),
  updateAutomation: (a: Automation) => call<Automation>("PUT", `/api/automations/${encodeURIComponent(a.id)}`, a),
  removeAutomation: (id: string) => call<undefined>("DELETE", `/api/automations/${encodeURIComponent(id)}`),
  runAutomation: async (id: string, farm?: string) =>
    (await call<{ runs: AutomationRun[] }>("POST", `/api/automations/${encodeURIComponent(id)}/run`, { farm })).runs,
  runs: async (id: string) =>
    (await call<{ runs: AutomationRun[] }>("GET", `/api/automations/${encodeURIComponent(id)}/runs?limit=10`)).runs,
};

function message(e: unknown): string {
  return e instanceof Error ? e.message : String(e);
}

const tones: Record<AdviceSeverity, StatusTone> = { urgent: "bad", warning: "warn", advice: "ok" };
const severityWords: Record<AdviceSeverity, string> = { urgent: "Urgent", warning: "Warning", advice: "Advice" };
const border: Record<AdviceSeverity, string> = {
  urgent: "border-l-ak-bad",
  warning: "border-l-ak-warn",
  advice: "border-l-ak-ok",
};

function weekday(date: string): string {
  const d = new Date(date + "T00:00:00Z");
  return Number.isNaN(d.getTime()) ? date : d.toLocaleDateString(undefined, { weekday: "short", timeZone: "UTC" });
}

function shortDate(date: string): string {
  const d = new Date(date + "T00:00:00Z");
  return Number.isNaN(d.getTime())
    ? date
    : d.toLocaleDateString(undefined, { day: "numeric", month: "short", timeZone: "UTC" });
}

function Section({ title, icon, children }: { title: string; icon: ReactNode; children: ReactNode }) {
  return (
    <section className="flex flex-col gap-3 rounded-xl border bg-ak-surface p-4 shadow-sm">
      <h2 className="flex items-center gap-2 font-semibold [&_svg]:size-4 [&_svg]:text-muted-foreground">
        {icon}
        {title}
      </h2>
      {children}
    </section>
  );
}

/** The week's forecast as seven small columns. */
function Forecast({ days }: { days: WeatherDay[] }) {
  const most = Math.max(20, ...days.map((d) => d.rain_mm));
  return (
    <div className="-mx-1 flex gap-1.5 overflow-x-auto px-1 pb-1">
      {days.map((d) => (
        <div
          key={d.date}
          className="flex min-w-[4.5rem] flex-1 flex-col items-center gap-1 rounded-lg bg-ak-bg p-2 text-center text-xs"
        >
          <span className="font-medium">{weekday(d.date)}</span>
          <span className="text-muted-foreground">{shortDate(d.date)}</span>
          <div className="flex h-12 w-4 items-end overflow-hidden rounded bg-ak-hover" aria-hidden="true">
            <div
              className="w-full rounded bg-sky-600"
              style={{ height: `${Math.min(100, (d.rain_mm / most) * 100)}%` }}
            />
          </div>
          <span className="tabular-nums" title="Rain">
            {d.rain_mm < 0.5 ? "dry" : `${d.rain_mm.toFixed(0)} mm`}
          </span>
          <span className="tabular-nums" title="High and low">
            {d.tmax_c.toFixed(0)}° / {d.tmin_c.toFixed(0)}°
          </span>
          <span className="text-muted-foreground tabular-nums" title="Strongest gust">
            {d.gust_kmh.toFixed(0)} km/h
          </span>
        </div>
      ))}
    </div>
  );
}

function AdviceCard({
  severity,
  title,
  action,
  why,
  day,
}: {
  severity: AdviceSeverity;
  title: string;
  action: string;
  why: string;
  day?: string;
}) {
  return (
    <article className={`flex flex-col gap-1.5 rounded-lg border border-l-4 bg-ak-bg p-3 ${border[severity]}`}>
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
        <h3 className="font-medium">{title}</h3>
        <Status tone={tones[severity]} className="text-xs">
          {severityWords[severity]}
          {day ? ` · ${shortDate(day)}` : ""}
        </Status>
      </div>
      <p className="text-sm">{action}</p>
      {why && <p className="text-xs text-muted-foreground">{why}</p>}
    </article>
  );
}

function TaskList({ tasks, today }: { tasks: CareTask[]; today?: string }) {
  if (tasks.length === 0) return <p className="text-sm text-muted-foreground">Nothing due.</p>;
  return (
    <ol className="flex flex-col divide-y">
      {tasks.map((t, i) => (
        <li
          key={`${t.zone}-${t.date}-${i}`}
          className={`flex gap-3 py-2 text-sm ${today && t.date < today ? "text-muted-foreground" : ""}`}
        >
          <span className="w-16 shrink-0 tabular-nums">{shortDate(t.date)}</span>
          <span className="flex min-w-0 flex-col">
            <span className="font-medium">{t.title}</span>
            {t.detail && <span className="text-xs text-muted-foreground">{t.detail}</span>}
          </span>
          <span className="ml-auto shrink-0 text-xs text-muted-foreground tabular-nums">day {t.das}</span>
        </li>
      ))}
    </ol>
  );
}

/** A plot's notices, with marking them read. */
function Inbox({ farm, farms, onRead }: { farm?: string; farms?: Farm[]; onRead?: () => void }) {
  const [notices, setNotices] = useState<Notice[]>();
  const [error, setError] = useState("");
  const load = useCallback(() => {
    api.notices(farm).then(
      (r) => setNotices(r.notices),
      (e: unknown) => setError(message(e)),
    );
  }, [farm]);
  useEffect(load, [load]);
  const read = (ids?: string[]) =>
    api.readNotices(farm, ids).then(
      () => {
        load();
        onRead?.();
      },
      (e: unknown) => setError(message(e)),
    );
  if (error) return <p className="text-sm text-ak-bad">{error}</p>;
  if (!notices) return <p className="text-sm text-muted-foreground">Loading…</p>;
  if (notices.length === 0)
    return (
      <p className="text-sm text-muted-foreground">
        No alerts yet. The app writes here only when the weather or the crop calendar means something needs doing.
      </p>
    );
  const unread = notices.filter((n) => !n.read_at).length;
  const plot = (id: string) => farms?.find((f) => f.id === id)?.name;
  return (
    <div className="flex flex-col gap-2">
      {unread > 0 && (
        <Button variant="outline" size="sm" className="self-start" onClick={() => read()}>
          <CheckCheck /> Mark {unread} read
        </Button>
      )}
      {notices.map((n) => (
        <div key={n.id} className={n.read_at ? "opacity-60" : ""}>
          <AdviceCard
            severity={n.severity}
            title={
              (farms && !farm && plot(n.farm) && !n.title.includes(plot(n.farm) ?? "") ? `${plot(n.farm)}: ` : "") +
              n.title
            }
            action={n.body.split("\n\nWhy: ")[0] ?? n.body}
            why={n.body.includes("\n\nWhy: ") ? (n.body.split("\n\nWhy: ")[1] ?? "") : ""}
            day={n.day}
          />
          <div className="flex items-center gap-3 px-1 pt-1 text-xs text-muted-foreground">
            <span>{new Date(n.created_at).toLocaleString()}</span>
            {!n.read_at && (
              <button className="underline hover:text-foreground" onClick={() => read([n.id])}>
                Mark read
              </button>
            )}
          </div>
        </div>
      ))}
    </div>
  );
}

/**
 * A plot's crop care: the week's forecast read against its crops (advice
 * only when there is something to do), the week's work, the plot's alerts
 * and the season's calendar.
 */
export function Care({ farm, weather, automations }: { farm: Farm; weather: boolean; automations: boolean }) {
  const [week, setWeek] = useState<Week>();
  const [cal, setCal] = useState<CropCalendar>();
  const [error, setError] = useState("");
  const planned = (farm.plan?.zones.length ?? 0) > 0;

  useEffect(() => {
    if (weather)
      api.week(farm.id).then(
        (w) => {
          setWeek(w);
          setError("");
        },
        (e: unknown) => setError(message(e)),
      );
    if (planned) api.calendar(farm.id).then(setCal, () => setCal(undefined));
  }, [farm.id, farm.updated_at, weather, planned]);

  return (
    <div className="grid gap-4 lg:grid-cols-[minmax(0,1fr)_24rem]">
      <div className="flex min-w-0 flex-col gap-4">
        {weather && (
          <Section title="The week ahead" icon={<CloudSun />}>
            {error && <p className="text-sm text-ak-bad">{error}</p>}
            {!week && !error && <p className="text-sm text-muted-foreground">Reading the forecast…</p>}
            {week && (
              <>
                <Forecast days={week.days} />
                <p className="text-xs text-muted-foreground">
                  <CloudRain className="mr-1 inline size-3.5" aria-hidden="true" />
                  {week.totals.rain_mm.toFixed(0)} mm of rain over {week.days.length} days ·
                  <Thermometer className="mx-1 inline size-3.5" aria-hidden="true" />
                  {week.totals.min_c.toFixed(0)}° to {week.totals.max_c.toFixed(0)}° ·
                  <Wind className="mx-1 inline size-3.5" aria-hidden="true" />
                  gusts to {week.totals.gust_kmh.toFixed(0)} km/h · {week.source}
                </p>
                {week.advice.length === 0 ? (
                  <p className="rounded-lg border border-l-4 border-l-ak-ok bg-ak-bg p-3 text-sm">
                    Nothing to do because of the weather this week.
                    {planned ? "" : " (No crop plan is saved, so only weather that harms any land is checked.)"}
                  </p>
                ) : (
                  week.advice.map((a) => (
                    <AdviceCard
                      key={a.key}
                      severity={a.severity}
                      title={a.title}
                      action={a.action}
                      why={a.why}
                      day={a.day}
                    />
                  ))
                )}
              </>
            )}
          </Section>
        )}
        {planned && (
          <Section title="This week's work" icon={<Sprout />}>
            {week?.crops && week.crops.length > 0 && (
              <ul className="flex flex-wrap gap-2 text-xs">
                {week.crops.map((c) => (
                  <li key={c.zone} className="rounded-full bg-ak-bg px-2.5 py-1">
                    {c.name}: {c.stage.name.replace("_", " ")}
                    {c.stage.das >= 0 ? `, day ${c.stage.das}` : ` in ${-c.stage.das} days`}
                  </li>
                ))}
              </ul>
            )}
            <TaskList tasks={week?.tasks ?? []} />
          </Section>
        )}
        {cal && (
          <Section title="Season calendar" icon={<CalendarDays />}>
            {cal.zones.map((z) => (
              <details key={z.zone} open={cal.zones.length === 1} className="group">
                <summary className="cursor-pointer text-sm font-medium">
                  {z.crop_name}: sown {formatDate(z.sow)}, harvest {shortDate(z.harvest_from)} to{" "}
                  {formatDate(z.harvest_by)}
                </summary>
                <TaskList tasks={z.tasks} today={week?.today} />
              </details>
            ))}
            {cal.notes?.map((n) => (
              <p key={n} className="text-xs text-muted-foreground">
                {n}
              </p>
            ))}
          </Section>
        )}
        {!planned && (
          <p className="text-sm text-muted-foreground">
            Save a season plan to get the crop calendar and weather advice for its crops.
          </p>
        )}
      </div>
      {automations && (
        <Section title="Alerts for this plot" icon={<Bell />}>
          <Inbox farm={farm.id} />
        </Section>
      )}
    </div>
  );
}

const metricWords: Record<string, string> = {
  rain_mm: "a day's rain (mm)",
  rain_total_mm: "rain over the days (mm)",
  tmax_c: "a day's high (°C)",
  tmin_c: "a night's low (°C)",
  gust_kmh: "wind gusts (km/h)",
  humidity_pct: "humidity (%)",
  et0_total_mm: "crop water use over the days (mm)",
};

const dayNames = ["Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"];

/** A cron schedule in words, for the common shapes; UTC times shown in India's time. */
function describeSchedule(s: string): string {
  const every = /^0 \*\/(\d+) \* \* \*$/.exec(s);
  if (every) return `every ${every[1]} hours`;
  const m = /^(\d+) (\d+) \* \* (\*|\d)$/.exec(s);
  if (!m) return s;
  let mins = Number(m[2]) * 60 + Number(m[1]) + 330; // UTC to India, +5:30
  let day = m[3] === "*" ? -1 : Number(m[3]);
  if (mins >= 24 * 60) {
    mins -= 24 * 60;
    if (day >= 0) day = (day + 1) % 7;
  }
  const t = `${String(Math.floor(mins / 60)).padStart(2, "0")}:${String(mins % 60).padStart(2, "0")} IST`;
  return day < 0 ? `daily at ${t}` : `${dayNames[day]}s at ${t}`;
}

function triggerWords(a: Automation): string {
  const t = a.trigger;
  if (t.kind === "event") return `when ${t.event?.replace("farm.", "a plot's ").replace("_", " ")}`;
  const when = describeSchedule(t.schedule ?? "");
  if (t.kind === "weather" && t.weather)
    return `${when}, if ${metricWords[t.weather.metric] ?? t.weather.metric} ${t.weather.op} ${t.weather.value} in the next ${t.weather.days || 3} days`;
  return when;
}

const runTones: Record<AutomationRun["status"], StatusTone> = { notified: "warn", no_action: "ok", failed: "bad" };
const runWords: Record<AutomationRun["status"], string> = {
  notified: "alerted",
  no_action: "nothing to do",
  failed: "failed",
};

function AutomationCard({ a, farms, onChanged }: { a: Automation; farms: Farm[]; onChanged: () => void }) {
  const [runs, setRuns] = useState<AutomationRun[]>();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const plot = (id: string) => farms.find((f) => f.id === id)?.name ?? id;
  const showRuns = () => api.runs(a.id).then(setRuns, (e: unknown) => setError(message(e)));
  return (
    <article className="flex flex-col gap-2 rounded-xl border bg-ak-surface p-4 shadow-sm">
      <div className="flex flex-wrap items-start justify-between gap-2">
        <div className="flex min-w-0 flex-col gap-0.5">
          <h3 className="font-medium">{a.name}</h3>
          <p className="text-xs text-muted-foreground">
            {triggerWords(a)} · {a.action.kind === "skill" ? `assistant skill ${a.action.skill}` : a.action.rule}
            {a.scope.with_plan ? " · plots with a plan" : ""}
          </p>
        </div>
        <Status tone={a.enabled ? "ok" : "neutral"} className="text-xs">
          {a.enabled ? "On" : "Off"}
        </Status>
      </div>
      {a.description && <p className="text-sm text-muted-foreground">{a.description}</p>}
      <div className="flex flex-wrap gap-2">
        <Button
          variant="outline"
          size="sm"
          onClick={() =>
            api.updateAutomation({ ...a, enabled: !a.enabled }).then(onChanged, (e: unknown) => setError(message(e)))
          }
        >
          <Power /> Turn {a.enabled ? "off" : "on"}
        </Button>
        <Button
          variant="outline"
          size="sm"
          disabled={busy}
          onClick={() => {
            setBusy(true);
            api
              .runAutomation(a.id)
              .then(
                (rs) => {
                  setRuns(rs);
                  onChanged();
                },
                (e: unknown) => setError(message(e)),
              )
              .finally(() => setBusy(false));
          }}
        >
          <Play /> {busy ? "Running…" : "Run now"}
        </Button>
        <Button variant="ghost" size="sm" onClick={showRuns}>
          Recent runs
        </Button>
        {!a.builtin && (
          <Button
            variant="ghost"
            size="sm"
            aria-label={`Delete ${a.name}`}
            onClick={() => api.removeAutomation(a.id).then(onChanged, (e: unknown) => setError(message(e)))}
          >
            <Trash2 />
          </Button>
        )}
      </div>
      {error && <p className="text-sm text-ak-bad">{error}</p>}
      {runs && (
        <ul className="flex flex-col gap-1 text-xs">
          {runs.length === 0 && <li className="text-muted-foreground">No runs yet.</li>}
          {runs.map((r) => (
            <li key={r.id} className="flex flex-wrap gap-x-2">
              <span className="tabular-nums text-muted-foreground">{new Date(r.started).toLocaleString()}</span>
              <span className="font-medium">{plot(r.farm)}</span>
              <Status tone={runTones[r.status]}>{runWords[r.status]}</Status>
              <span className="text-muted-foreground">{r.error || r.note}</span>
            </li>
          ))}
        </ul>
      )}
    </article>
  );
}

/** A form for a farmer's own weather or schedule automation. */
function NewAutomation({ rules, onSaved }: { rules: Record<string, string>; onSaved: () => void }) {
  const [name, setName] = useState("Heat watch");
  const [kind, setKind] = useState<"weather" | "schedule">("weather");
  const [metric, setMetric] = useState("tmax_c");
  const [op, setOp] = useState<">=" | "<=">(">=");
  const [value, setValue] = useState(40);
  const [days, setDays] = useState(5);
  const [schedule, setSchedule] = useState("0 1 * * *");
  const [action, setAction] = useState("rule:weather_advice");
  const [error, setError] = useState("");
  const field = "flex flex-col gap-1 text-sm";
  const input = "rounded-md border bg-ak-bg px-2 py-1.5 text-sm";
  const save = () => {
    const [k, v] = action.split(":") as ["rule" | "skill", string];
    api
      .createAutomation({
        name,
        enabled: true,
        trigger:
          kind === "weather"
            ? { kind, schedule, weather: { metric, op, value, days } }
            : { kind: "schedule", schedule },
        scope: { with_plan: true },
        action: k === "rule" ? { kind: "rule", rule: v } : { kind: "skill", skill: v },
      })
      .then(onSaved, (e: unknown) => setError(message(e)));
  };
  return (
    <div className="flex flex-col gap-3 rounded-xl border bg-ak-surface p-4 shadow-sm">
      <div className="grid gap-3 sm:grid-cols-2">
        <label className={field}>
          Name
          <input className={input} value={name} onChange={(e) => setName(e.target.value)} />
        </label>
        <label className={field}>
          Starts
          <select className={input} value={kind} onChange={(e) => setKind(e.target.value as "weather" | "schedule")}>
            <option value="weather">When the forecast crosses a limit</option>
            <option value="schedule">On a schedule</option>
          </select>
        </label>
        {kind === "weather" && (
          <>
            <label className={field}>
              Watch
              <select className={input} value={metric} onChange={(e) => setMetric(e.target.value)}>
                {Object.entries(metricWords).map(([k, v]) => (
                  <option key={k} value={k}>
                    {v}
                  </option>
                ))}
              </select>
            </label>
            <div className="flex gap-2">
              <label className={field}>
                Is
                <select className={input} value={op} onChange={(e) => setOp(e.target.value as ">=" | "<=")}>
                  <option value=">=">at least</option>
                  <option value="<=">at most</option>
                </select>
              </label>
              <label className={field}>
                Value
                <input
                  className={`${input} w-20`}
                  type="number"
                  value={value}
                  onChange={(e) => setValue(Number(e.target.value))}
                />
              </label>
              <label className={field}>
                Days ahead
                <input
                  className={`${input} w-16`}
                  type="number"
                  min={1}
                  max={16}
                  value={days}
                  onChange={(e) => setDays(Number(e.target.value))}
                />
              </label>
            </div>
          </>
        )}
        <label className={field}>
          {kind === "weather" ? "Look at the forecast" : "When"} (cron, UTC: {describeSchedule(schedule)})
          <input className={input} value={schedule} onChange={(e) => setSchedule(e.target.value)} />
        </label>
        <label className={field}>
          Then
          <select className={input} value={action} onChange={(e) => setAction(e.target.value)}>
            {Object.entries(rules).map(([k, v]) => (
              <option key={k} value={`rule:${k}`}>
                {k}: {v}
              </option>
            ))}
            <option value="skill:weather-check">assistant: weather-check skill</option>
            <option value="skill:manage-crop">assistant: manage-crop skill</option>
          </select>
        </label>
      </div>
      {error && <p className="text-sm text-ak-bad">{error}</p>}
      <Button size="sm" className="self-start" onClick={save}>
        <Plus /> Add automation
      </Button>
    </div>
  );
}

/** Every plot's alerts, and the automations that write them. */
export function AlertsPage({ farms, onRead }: { farms: Farm[]; onRead: () => void }) {
  const [list, setList] = useState<{ automations: Automation[]; rules: Record<string, string> }>();
  const [adding, setAdding] = useState(false);
  const [error, setError] = useState("");
  const load = useCallback(() => api.automations().then(setList, (e: unknown) => setError(message(e))), []);
  useEffect(() => {
    void load();
  }, [load]);
  return (
    <>
      <div className="flex flex-col gap-1">
        <h1 className="text-2xl font-semibold">Alerts and automations</h1>
        <p className="max-w-2xl text-sm text-muted-foreground">
          The app checks each plot's weather and crop calendar on a schedule and writes here only when something needs
          doing. Alerts show in the app for now; SMS or WhatsApp can come later.
        </p>
      </div>
      <div className="grid gap-4 lg:grid-cols-[minmax(0,1fr)_minmax(0,1fr)]">
        <Section title="Alerts" icon={<Bell />}>
          <Inbox farms={farms} onRead={onRead} />
        </Section>
        <div className="flex flex-col gap-3">
          <div className="flex items-center justify-between">
            <h2 className="font-semibold">Automations</h2>
            <Button variant="outline" size="sm" onClick={() => setAdding((x) => !x)}>
              <Plus /> New
            </Button>
          </div>
          {error && <p className="text-sm text-ak-bad">{error}</p>}
          {adding && list && (
            <NewAutomation
              rules={list.rules}
              onSaved={() => {
                setAdding(false);
                void load();
              }}
            />
          )}
          {list?.automations.map((a) => (
            <AutomationCard key={a.id} a={a} farms={farms} onChanged={() => void load()} />
          ))}
        </div>
      </div>
    </>
  );
}
