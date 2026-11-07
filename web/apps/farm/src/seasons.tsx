import { Button } from "@agentkit/ui/components/button";
import { Status } from "@agentkit/ui";
import { AlertTriangle, CheckCircle2, CloudSun, Plus, Sun, SunDim, X } from "lucide-react";
import { useEffect, useMemo, useState, type ReactNode } from "react";
import { api } from "./api";
import { formatDate, localName, scoreTone } from "./plan";
import type { Climate, Crop, DayHabit, Farm, Light, Option, Sowing, SunMonth, SunReport } from "./types";

function message(e: unknown): string {
  return e instanceof Error ? e.message : String(e);
}

const monthNames = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"];

const seasonLooks: Record<string, { label: string; color: string }> = {
  kharif: { label: "Kharif", color: "#16a34a" },
  rabi: { label: "Rabi", color: "#2563eb" },
  zaid: { label: "Zaid", color: "#d97706" },
};

const lightWords: Record<Light, string> = { full: "Full sun", part: "Part shade", shade: "Shade" };
const dayWords: Record<DayHabit, string> = { neutral: "", short: "Short-day", long: "Long-day" };

/** A score's colour in the calendar: deep green good, pale poor. */
function scoreColor(score: number): string {
  if (score >= 70) return "#15803d";
  if (score >= 55) return "#65a30d";
  if (score >= 40) return "#bef264";
  if (score >= 20) return "#fef3c7";
  return "#e7e5e4";
}

/** The side of the sky the noon sun is in: N or S. */
function side(azimuth: number): string {
  return azimuth < 90 || azimuth > 270 ? "N" : "S";
}

/**
 * The sun and seasons at a plot: the year month by month (light on the
 * ground and on its slope, bright hours, day length, the noon sun's height
 * and side, rain, heat and the Indian seasons), the noon sun's angle on
 * the ground, and a calendar of how each crop does sown in each week.
 */
export function Seasons({ farm }: { farm: Farm }) {
  const [sun, setSun] = useState<SunReport>();
  const [climate, setClimate] = useState<Climate>();
  const [crops, setCrops] = useState<Crop[]>([]);
  const [error, setError] = useState("");

  useEffect(() => {
    Promise.all([api.sun(farm.id), api.climate(farm.id), api.crops()]).then(
      ([s, c, cs]) => {
        setSun(s);
        setClimate(c);
        setCrops(cs);
      },
      (e: unknown) => setError(message(e)),
    );
  }, [farm.id, farm.updated_at]);

  if (error)
    return (
      <p role="alert" className="text-sm text-ak-bad">
        {error}
      </p>
    );
  if (!sun || !climate) return <p className="text-sm text-muted-foreground">Working out the sun at the plot…</p>;
  return (
    <div className="flex flex-col gap-4">
      <section className="rounded-xl border bg-ak-surface p-4 shadow-sm" aria-labelledby="year-title">
        <h2 id="year-title" className="flex items-center gap-2 text-lg font-semibold">
          <Sun className="size-5 text-[#ca8a04]" aria-hidden="true" /> The year at this plot
        </h2>
        <p className="mt-1 text-sm text-muted-foreground">{sun.slope_note}</p>
        <YearChart sun={sun} climate={climate} />
        <div className="mt-4 grid gap-4 lg:grid-cols-[minmax(0,1fr)_20rem]">
          <ul className="flex flex-col gap-2 text-sm" aria-label="What the sun means here">
            {sun.notes.map((n) => (
              <li key={n} className="flex gap-2">
                <CloudSun className="mt-0.5 size-4 shrink-0 text-muted-foreground" aria-hidden="true" />
                <span>{n}</span>
              </li>
            ))}
          </ul>
          <NoonSun sun={sun} />
        </div>
        <p className="mt-3 text-xs text-muted-foreground">
          Sunlight and weather: {climate.source}. Sun angles worked out for the plot's latitude (
          {sun.point.lat.toFixed(2)}°).
        </p>
      </section>
      <CropCalendar farm={farm} crops={crops} />
    </div>
  );
}

const cell = "flex items-center justify-center text-xs tabular-nums";

/** The twelve months side by side, one row per measure. */
export function YearChart({ sun, climate }: { sun: SunReport; climate: Climate }) {
  const now = Number(sun.today.slice(5, 7));
  const maxMJ = Math.max(28, ...sun.months.map((m) => m.clear_mj ?? m.flat_mj));
  const maxRain = Math.max(50, ...climate.months.map((m) => m.rain_mm));
  const light = sun.months.some((m) => m.flat_mj > 0);
  const sloped = !!sun.slope;
  const spans = sun.seasons.flatMap((s) =>
    s.from <= s.to
      ? [{ ...s, a: s.from, b: s.to }]
      : [
          { ...s, a: s.from, b: 12 },
          { ...s, a: 1, b: s.to },
        ],
  );
  const row = (label: string, hint: string, render: (m: SunMonth, i: number) => ReactNode) => (
    <>
      <div className="flex flex-col justify-center pr-2 text-xs">
        <span className="font-medium">{label}</span>
        <span className="text-muted-foreground">{hint}</span>
      </div>
      {sun.months.map((m, i) => (
        <div key={i} className={`${cell} ${i + 1 === now ? "bg-[#4d7c0f]/8" : ""}`}>
          {render(m, i)}
        </div>
      ))}
    </>
  );
  return (
    <div className="-mx-4 mt-3 overflow-x-auto px-4">
      <div
        className="grid min-w-[46rem] gap-y-1"
        style={{ gridTemplateColumns: "7rem repeat(12, minmax(2.6rem, 1fr))" }}
        role="table"
        aria-label="The year month by month"
      >
        <div />
        {monthNames.map((n, i) => (
          <div
            key={n}
            role="columnheader"
            className={`${cell} rounded-t py-1 font-medium ${i + 1 === now ? "bg-[#4d7c0f] text-white" : "text-muted-foreground"}`}
          >
            {n}
          </div>
        ))}
        <div className="flex items-center pr-2 text-xs font-medium">Sowing season</div>
        <div className="grid" style={{ gridColumn: "2 / 14", gridTemplateColumns: "repeat(12, minmax(0, 1fr))" }}>
          {spans.map((s) => (
            <div
              key={`${s.season}${s.a}`}
              className="mx-0.5 truncate rounded px-1.5 py-0.5 text-center text-xs font-medium text-white"
              style={{
                gridColumn: `${s.a} / ${s.b + 1}`,
                gridRow: 1,
                background: seasonLooks[s.season]?.color ?? "#57534e",
              }}
            >
              {seasonLooks[s.season]?.label ?? s.season}
            </div>
          ))}
        </div>
        {light &&
          row("Sunlight", sloped ? "MJ/m² a day, on the slope" : "MJ/m² a day", (m) => (
            <div
              className="flex h-16 flex-col items-center justify-end gap-0.5"
              title={`${m.slope_mj} MJ/m² of a clear sky's ${m.clear_mj ?? "?"}`}
            >
              <div className="relative flex h-12 w-4 items-end">
                {m.clear_mj ? (
                  <div
                    className="absolute bottom-0 w-4 rounded-t bg-[#fde68a]"
                    style={{ height: `${(m.clear_mj / maxMJ) * 100}%` }}
                  />
                ) : null}
                <div
                  className="relative w-4 rounded-t bg-[#f59e0b]"
                  style={{ height: `${(m.slope_mj / maxMJ) * 100}%` }}
                />
              </div>
              <span>{Math.round(m.slope_mj)}</span>
            </div>
          ))}
        {light && row("Bright sun", "hours a day", (m) => m.sun_hours.toFixed(1))}
        {row("Day length", "hours", (m) => m.day_length_h.toFixed(1))}
        {row("Noon sun", "height and side", (m) => (
          <span
            title={`At noon the sun is ${m.noon.elevation}° above the ${side(m.noon.azimuth) === "N" ? "northern" : "southern"} horizon`}
          >
            {Math.round(m.noon.elevation)}° {side(m.noon.azimuth)}
          </span>
        ))}
        {row(
          "Rays strike",
          sloped ? "the slope, ° from straight" : "the ground, ° from straight",
          (m) => `${Math.round(m.noon_incidence)}°`,
        )}
        {sloped &&
          light &&
          row("Slope vs flat", "sun on it", (m) => {
            const d = Math.round((m.slope_factor - 1) * 100);
            return (
              <span className={d < -4 ? "text-ak-bad" : d > 4 ? "text-[#15803d]" : "text-muted-foreground"}>
                {d > 0 ? `+${d}` : d}%
              </span>
            );
          })}
        {row("Rain", "mm a month", (_, i) => {
          const r = climate.months[i]?.rain_mm ?? 0;
          return (
            <div className="flex h-12 flex-col items-center justify-end gap-0.5">
              <div className="w-4 rounded-t bg-[#38bdf8]" style={{ height: `${Math.max(2, (r / maxRain) * 36)}px` }} />
              <span>{Math.round(r)}</span>
            </div>
          );
        })}
        {row("Heat", "°C low – high", (_, i) => {
          const c = climate.months[i];
          return c ? `${Math.round(c.temp_min)}–${Math.round(c.temp_max)}` : "";
        })}
      </div>
    </div>
  );
}

/**
 * The noon sun's path seen from the east: its height over the northern or
 * southern horizon in June, March and December, and the ground's slope
 * along north to south.
 */
export function NoonSun({ sun }: { sun: SunReport }) {
  const W = 300;
  const H = 170;
  const cx = W / 2;
  const cy = 140;
  const R = 110;
  // The slope's fall along the north-south line, in degrees: positive
  // falling to the south.
  const ns = sun.slope
    ? (Math.atan(Math.tan((sun.slope.tilt_deg * Math.PI) / 180) * -Math.cos((sun.slope.aspect_deg * Math.PI) / 180)) *
        180) /
      Math.PI
    : 0;
  const picks = [
    { m: sun.months[5], label: "Jun", color: "#ea580c" },
    { m: sun.months[2], label: "Mar", color: "#ca8a04" },
    { m: sun.months[11], label: "Dec", color: "#2563eb" },
  ].flatMap((p) => (p.m ? [{ ...p, m: p.m }] : []));
  const jun = sun.months[5]?.noon_incidence ?? 0;
  const dec = sun.months[11]?.noon_incidence ?? 0;
  const slopeRad = (ns * Math.PI) / 180;
  const dx = Math.cos(slopeRad) * 90;
  const dy = Math.sin(slopeRad) * 90;
  return (
    <figure className="rounded-lg border bg-ak-bg p-2">
      <svg
        viewBox={`0 0 ${W} ${H}`}
        className="w-full"
        role="img"
        aria-label="The noon sun's height through the year, seen from the east"
      >
        <path
          d={`M ${cx - R} ${cy} A ${R} ${R} 0 0 1 ${cx + R} ${cy}`}
          fill="none"
          stroke="currentColor"
          strokeOpacity="0.15"
          strokeDasharray="3 4"
        />
        <line x1={10} y1={cy} x2={W - 10} y2={cy} stroke="currentColor" strokeOpacity="0.25" />
        <line
          x1={cx - dx}
          y1={cy - dy}
          x2={cx + dx}
          y2={cy + dy}
          stroke="#78350f"
          strokeWidth="5"
          strokeLinecap="round"
        />
        <text x={12} y={cy + 18} fontSize="11" fill="currentColor" opacity="0.6">
          North
        </text>
        <text x={W - 12} y={cy + 18} fontSize="11" textAnchor="end" fill="currentColor" opacity="0.6">
          South
        </text>
        {picks.map(({ m, label, color }) => {
          // North is to the left.
          const north = side(m.noon.azimuth) === "N";
          const a = (m.noon.elevation * Math.PI) / 180;
          const x = cx + (north ? -1 : 1) * R * Math.cos(a);
          const y = cy - R * Math.sin(a);
          return (
            <g key={label}>
              <line x1={x} y1={y} x2={cx} y2={cy} stroke={color} strokeWidth="1.5" strokeDasharray="4 3" />
              <circle cx={x} cy={y} r="8" fill={color} />
              <text
                x={x + (north ? -12 : 12)}
                y={y + 4}
                fontSize="11"
                textAnchor={north ? "end" : "start"}
                fill="currentColor"
              >
                {label} {Math.round(m.noon.elevation)}°
              </text>
            </g>
          );
        })}
      </svg>
      <figcaption className="px-1 text-xs text-muted-foreground">
        The noon sun's height in June, March and December, seen from the east
        {sun.slope ? ", over the plot's slope" : ""}. Rays at noon strike the ground {Math.round(jun)}° from straight-on
        in June and {Math.round(dec)}° in December: the larger the angle, the weaker each square metre's light.
      </figcaption>
    </figure>
  );
}

/** How each crop does sown in each week of the coming year. */
export function CropCalendar({ farm, crops }: { farm: Farm; crops: Crop[] }) {
  const [picked, setPicked] = useState<string[]>();
  const [loaded, setLoaded] = useState<{ key: string; options: Option[] }>();
  const [error, setError] = useState("");
  const [detail, setDetail] = useState<{ crop: string; sow: string; option?: Option }>();
  const [adding, setAdding] = useState("");

  // The farm's best six to start with.
  useEffect(() => {
    api.recommend(farm.id, { limit: 6 }).then(
      (r) => setPicked(r.options.map((o) => o.crop)),
      (e: unknown) => setError(message(e)),
    );
  }, [farm.id]);

  const key = picked?.join(",") ?? "";
  useEffect(() => {
    if (!picked || picked.length === 0) return;
    api.recommend(farm.id, { crops: picked, calendar: true }).then(
      (r) =>
        setLoaded({ key: picked.join(","), options: picked.flatMap((id) => r.options.filter((o) => o.crop === id)) }),
      (e: unknown) => setError(message(e)),
    );
  }, [farm.id, picked]);
  // While a new set loads, the old one shows without the crops taken off.
  const options = useMemo(() => (loaded?.options ?? []).filter((o) => picked?.includes(o.crop)), [loaded, picked]);
  const busy = !!picked?.length && loaded?.key !== key;

  const open = (crop: string, sow: string) => {
    setDetail({ crop, sow });
    api.recommend(farm.id, { crops: [crop], around: sow, spread: 3 }).then(
      (r) => setDetail({ crop, sow, option: r.options[0] }),
      (e: unknown) => setError(message(e)),
    );
  };

  const weeks = useMemo(() => options[0]?.calendar ?? [], [options]);
  const marks = useMemo(() => {
    const out: { i: number; label: string }[] = [];
    weeks.forEach((w, i) => {
      const m = Number(w.sow.slice(5, 7));
      if (i === 0 || m !== Number(weeks[i - 1]?.sow.slice(5, 7))) out.push({ i, label: monthNames[m - 1] ?? "" });
    });
    return out;
  }, [weeks]);
  const byID = new Map(crops.map((c) => [c.id, c]));
  const left = crops.filter((c) => !picked?.includes(c.id));

  return (
    <section className="rounded-xl border bg-ak-surface p-4 shadow-sm" aria-labelledby="calendar-title">
      <div className="flex flex-wrap items-end justify-between gap-2">
        <div>
          <h2 id="calendar-title" className="text-lg font-semibold">
            When each crop does well here
          </h2>
          <p className="text-sm text-muted-foreground">
            Each square is a week to sow in, from today: the darker, the better the whole season goes (heat, rain, sun,
            soil). Tap one to see why.
          </p>
        </div>
        <label className="flex items-center gap-2 text-sm">
          <Plus className="size-4" aria-hidden="true" />
          <select
            className="h-9 rounded-md border bg-background px-2 text-sm"
            value={adding}
            aria-label="Add a crop"
            onChange={(e) => {
              const id = e.target.value;
              setAdding("");
              if (id && picked && picked.length < 10) setPicked([...picked, id]);
            }}
          >
            <option value="">Add a crop…</option>
            {left.map((c) => (
              <option key={c.id} value={c.id}>
                {c.name}
              </option>
            ))}
          </select>
        </label>
      </div>
      {error && (
        <p role="alert" className="mt-2 text-sm text-ak-bad">
          {error}
        </p>
      )}
      {!picked || (busy && options.length === 0) ? (
        <p className="mt-3 text-sm text-muted-foreground">Scoring a year of sowings…</p>
      ) : (
        <div className="-mx-4 mt-3 overflow-x-auto px-4">
          <div className="min-w-[46rem]" role="table" aria-label="Sowing calendar">
            <div className="flex pl-44" aria-hidden="true">
              <div className="relative h-5 flex-1">
                {marks.map((m) => (
                  <span
                    key={m.i}
                    className="absolute text-xs text-muted-foreground"
                    style={{ left: `${(m.i / weeks.length) * 100}%` }}
                  >
                    {m.label}
                  </span>
                ))}
              </div>
            </div>
            {options.map((o) => {
              const c = byID.get(o.crop);
              const want = (c?.light ?? "full") as Light;
              return (
                <div key={o.crop} role="row" className="flex items-center gap-2 py-1">
                  <div className="flex w-42 shrink-0 items-center gap-1.5" role="rowheader">
                    <button
                      className="rounded p-0.5 text-muted-foreground hover:text-foreground"
                      aria-label={`Remove ${o.name}`}
                      onClick={() => setPicked(picked.filter((p) => p !== o.crop))}
                    >
                      <X className="size-3.5" />
                    </button>
                    <div className="min-w-0">
                      <p className="truncate text-sm font-medium">
                        {o.name}{" "}
                        {localName(o.names) && (
                          <span className="font-normal text-muted-foreground">{localName(o.names)}</span>
                        )}
                      </p>
                      <p className="flex items-center gap-1 text-xs text-muted-foreground">
                        {want === "full" ? (
                          <Sun className="size-3" aria-hidden="true" />
                        ) : (
                          <SunDim className="size-3" aria-hidden="true" />
                        )}
                        {lightWords[want]}
                        {c?.day_length && dayWords[c.day_length] ? ` · ${dayWords[c.day_length]}` : ""}
                      </p>
                    </div>
                  </div>
                  <div className="flex h-7 flex-1 gap-px">
                    {(o.calendar ?? []).map((w: Sowing) => (
                      <button
                        key={w.sow}
                        role="cell"
                        title={`Sow ${formatDate(w.sow)}: ${w.score}`}
                        aria-label={`${o.name} sown ${formatDate(w.sow)}: ${w.score} of 100`}
                        aria-pressed={detail?.crop === o.crop && detail.sow === w.sow}
                        onClick={() => open(o.crop, w.sow)}
                        className="flex-1 rounded-[2px] outline-offset-1 hover:outline-2 hover:outline-[#4d7c0f] aria-pressed:outline-2 aria-pressed:outline-black"
                        style={{ background: scoreColor(w.score) }}
                      />
                    ))}
                  </div>
                </div>
              );
            })}
            <Legend />
          </div>
        </div>
      )}
      {detail && <SowingDetail detail={detail} onClose={() => setDetail(undefined)} />}
    </section>
  );
}

function Legend() {
  const items: [number, string][] = [
    [80, "Good"],
    [60, "Fair"],
    [45, "Middling"],
    [25, "Poor"],
    [0, "Not here"],
  ];
  return (
    <ul className="mt-2 flex flex-wrap gap-3 pl-44 text-xs text-muted-foreground" aria-label="Legend">
      {items.map(([s, label]) => (
        <li key={label} className="flex items-center gap-1">
          <span className="inline-block size-3 rounded-[2px]" style={{ background: scoreColor(s) }} /> {label}
        </li>
      ))}
    </ul>
  );
}

function SowingDetail({
  detail,
  onClose,
}: {
  detail: { crop: string; sow: string; option?: Option };
  onClose: () => void;
}) {
  const o = detail.option;
  if (!o) return <p className="mt-3 text-sm text-muted-foreground">Looking at {formatDate(detail.sow)}…</p>;
  const l = o.light;
  return (
    <div className="mt-4 rounded-lg border bg-ak-bg p-3" aria-live="polite">
      <div className="flex flex-wrap items-center gap-2">
        <h3 className="font-semibold">
          {o.name}, sown {formatDate(o.sow)}
        </h3>
        <Status tone={scoreTone(o.score)}>{o.score} of 100</Status>
        <span className="text-sm text-muted-foreground">
          {seasonLooks[o.season]?.label ?? o.season} · harvest about {formatDate(o.harvest)}
        </span>
        <Button variant="ghost" size="sm" className="ml-auto" onClick={onClose} aria-label="Close">
          <X />
        </Button>
      </div>
      {l && (
        <p className="mt-2 text-sm">
          {l.sun_mj > 0 && (
            <>
              Sun on the ground through the season: about <b>{l.sun_mj} MJ/m²</b> a day, <b>{l.sun_hours} h</b> of
              bright sun
              {l.dimmest_month ? `, least in ${l.dimmest_month}` : ""}.{" "}
            </>
          )}
          Days {l.day_length_h[0]}–{l.day_length_h[1]} h.
          {l.slope_factor !== 1 &&
            ` The slope gets ${Math.round(Math.abs(l.slope_factor - 1) * 100)}% ${l.slope_factor < 1 ? "less" : "more"} sun than flat ground.`}
        </p>
      )}
      <div className="mt-2 grid gap-3 sm:grid-cols-2">
        <ul className="flex flex-col gap-1 text-sm" aria-label="Why it works">
          {o.reasons.map((n) => (
            <li key={n.id + n.message} className="flex gap-1.5">
              <CheckCircle2 className="mt-0.5 size-4 shrink-0 text-[#15803d]" aria-hidden="true" />
              {n.message}
            </li>
          ))}
        </ul>
        <ul className="flex flex-col gap-1 text-sm" aria-label="Risks">
          {o.risks.map((n) => (
            <li key={n.id + n.message} className="flex gap-1.5">
              <AlertTriangle className="mt-0.5 size-4 shrink-0 text-[#ca8a04]" aria-hidden="true" />
              {n.message}
            </li>
          ))}
        </ul>
      </div>
    </div>
  );
}
