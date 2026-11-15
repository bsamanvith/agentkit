import { ChevronDown, ChevronRight, Info } from "lucide-react";
import { useState, type ReactNode } from "react";

// The optional parts of a cycle, each one tap away and collapsed until
// tapped: what to buy, feeding and sprays, and when to sell. A lazy chunk
// of the Cycles tab; the numbers are the server's (package inputs and
// prices), this only says them.

/** A quantity in what it is sold in. */
export interface Item {
  product: string;
  name: string;
  synthetic?: boolean;
  make?: boolean;
  need: number;
  qty: number;
  unit: string;
  packs?: number;
  pack?: string;
  first: string;
  default?: boolean;
}

export interface Dose {
  product: string;
  name: string;
  qty: number;
  unit: string;
  per_acre?: number;
  per_plant?: number;
  rate?: string;
  note?: string;
}

export interface ScheduleTask {
  field: number;
  crop: string;
  key: string;
  date: string;
  das: number;
  stage: string;
  kind: string;
  title: string;
  doses: Dose[];
  water_per_acre?: string;
  phi_days?: number;
  cautions: string[];
  default?: boolean;
}

export interface WatchOption {
  route: string;
  name?: string;
  rate?: string;
  qty?: number;
  unit?: string;
  water_per_acre?: string;
  phi_days?: number;
  last_spray?: string;
  text?: string;
}

export interface Watch {
  field: number;
  crop: string;
  problem: string;
  name: string;
  kind: string;
  from?: string;
  to?: string;
  signs: string;
  threshold?: string;
  prevent?: string[];
  options: WatchOption[];
}

export interface InputsPlan {
  fields: { area: number; crop: string; crop_name: string; hectares: number; acres: number; sow: string }[];
  acres: number;
  method: "organic" | "conventional";
  water: "rainfed" | "irrigated";
  notes: string[];
  buy_before: Item[];
  buy_later: Item[];
  if_seen: Watch[];
  schedule: ScheduleTask[];
  sources?: { id: string; name: string; publisher: string; url: string }[];
}

export interface SellAdvice {
  sell_now: boolean;
  month: number;
  months_ahead: number;
  from: number;
  price: number;
  gain_pct: number;
  reasons: string[];
}

export interface SowingAdvice {
  shift: boolean;
  sow: string;
  days: number;
  harvest: string;
  gain_pct: number;
  reasons: string[];
}

export interface Timing {
  crop: string;
  crop_name: string;
  commodity: string;
  area?: string;
  sowing?: SowingAdvice;
  selling?: SellAdvice;
  /** The seasonal index, when there were enough years of prices for one. */
  index?: unknown;
  reasons: string[];
}

/** Why there is no timing advice, in a line. */
function noAdvice(t: Timing): string {
  if (t.reasons.some((r) => r.includes("does not keep"))) return `${t.crop_name} does not keep: sell when it is ready.`;
  if (!t.index) return `not enough ${t.crop_name.toLowerCase()} price history here yet.`;
  return "past prices here show no pattern steady enough to advise on.";
}

/**
 * Says a quantity as the server does (inputs.Amount): from a thousand
 * grams on in kilograms, whole numbers from a hundred, one decimal below.
 * The schedule and the shopping list's need say the same number.
 */
function amount(v: number, unit: string): string {
  if (unit === "g" && v >= 1000) [v, unit] = [v / 1000, "kg"];
  if (unit === "ml" && v >= 1000) [v, unit] = [v / 1000, "l"];
  const n = v >= 100 ? Math.round(v) : Math.round(v * 10) / 10;
  const plural = n !== 1;
  const name: Record<string, string> = {
    t: plural ? "tonnes" : "tonne",
    l: plural ? "litres" : "litre",
    piece: plural ? "pieces" : "piece",
  };
  return `${n.toLocaleString("en-IN")} ${name[unit] ?? unit}`;
}

/** What to buy of an item: the packs, else the quantity. */
function buy(it: Item): string {
  if (it.packs && it.pack) return `${it.packs} × ${it.pack}`;
  // What is made on the farm or got locally is not sold in steps: what it needs.
  return amount(it.make ? it.need : it.qty, it.unit);
}

const months = [
  "",
  "January",
  "February",
  "March",
  "April",
  "May",
  "June",
  "July",
  "August",
  "September",
  "October",
  "November",
  "December",
];

function day(d: string): string {
  const t = new Date(d + "T00:00:00");
  if (Number.isNaN(t.getTime())) return d;
  const year = t.getFullYear() !== new Date().getFullYear() ? "numeric" : undefined;
  return t.toLocaleDateString("en-IN", { day: "numeric", month: "short", year });
}

function Section({
  title,
  summary,
  open,
  onToggle,
  children,
}: {
  title: string;
  summary: string;
  open: boolean;
  onToggle: () => void;
  children: ReactNode;
}) {
  return (
    <div className="border-t">
      <button
        className="flex min-h-12 w-full items-center gap-2 py-2 text-left"
        aria-expanded={open}
        onClick={onToggle}
      >
        {open ? (
          <ChevronDown className="size-4 shrink-0" aria-hidden="true" />
        ) : (
          <ChevronRight className="size-4 shrink-0" aria-hidden="true" />
        )}
        <span className="flex min-w-0 flex-col">
          <span className="text-sm font-medium">{title}</span>
          <span className="truncate text-xs text-muted-foreground">{summary}</span>
        </span>
      </button>
      {open && <div className="flex flex-col gap-3 pb-4 pl-6">{children}</div>}
    </div>
  );
}

/** The starter-values and follow-the-label notes, wherever doses show. */
function Notes({ notes }: { notes: string[] }) {
  return (
    <ul className="flex flex-col gap-1 rounded-lg bg-ak-bg p-2.5 text-xs text-muted-foreground">
      {notes.map((n) => (
        <li key={n} className="flex items-start gap-1.5">
          <Info className="mt-0.5 size-3.5 shrink-0" aria-hidden="true" /> {n}
        </li>
      ))}
    </ul>
  );
}

function fieldLabel(p: InputsPlan, i: number): string {
  const f = p.fields[i];
  if (!f || p.fields.length < 2) return "";
  return `Area ${f.area + 1}, ${f.crop_name}: `;
}

function Items({ items, when }: { items: Item[]; when: string }) {
  return (
    <ul className="flex flex-col divide-y rounded-lg border">
      {items.map((it) => (
        <li key={it.product} className="flex items-baseline justify-between gap-3 px-3 py-2 text-sm">
          <span className="min-w-0">
            {it.name}
            {it.make && <span className="text-xs text-muted-foreground"> · make or get locally</span>}
            <span className="block text-xs text-muted-foreground">
              {when} {day(it.first)}
              {amount(it.need, it.unit) !== buy(it) && ` · ${amount(it.need, it.unit)} needed`}
            </span>
          </span>
          <span className="shrink-0 font-medium tabular-nums">{buy(it)}</span>
        </li>
      ))}
    </ul>
  );
}

function Buy({ p, sown }: { p: InputsPlan; sown?: boolean }) {
  return (
    <>
      <p className="text-sm">
        For {(Math.round(p.acres * 10) / 10).toLocaleString("en-IN")} acres,{" "}
        {p.method === "organic" ? "organic" : "with fertiliser"}, {p.water === "rainfed" ? "on rain only" : "irrigated"}
        .
      </p>
      <Notes notes={p.notes} />
      {p.buy_before.length > 0 &&
        (sown ? (
          // Sown: what was needed before is behind the farmer, one tap away.
          <details className="text-sm">
            <summary className="min-h-11 cursor-pointer py-2 text-muted-foreground">
              Before sowing: {p.buy_before.length} {p.buy_before.length === 1 ? "item" : "items"}, used by now
            </summary>
            <Items items={p.buy_before} when="Needed" />
          </details>
        ) : (
          <div className="flex flex-col gap-1.5">
            <h4 className="text-xs font-medium tracking-wide text-muted-foreground uppercase">Before sowing</h4>
            <Items items={p.buy_before} when="Needed" />
          </div>
        ))}
      {p.buy_later.length > 0 && (
        <div className="flex flex-col gap-1.5">
          <h4 className="text-xs font-medium tracking-wide text-muted-foreground uppercase">Later</h4>
          <Items items={p.buy_later} when="From" />
        </div>
      )}
      {p.if_seen.length > 0 && (
        <div className="flex flex-col gap-1.5">
          <h4 className="text-xs font-medium tracking-wide text-muted-foreground uppercase">
            Only if you see… (do not buy ahead)
          </h4>
          <ul className="flex flex-col gap-1.5">
            {p.if_seen.map((w) => (
              <li key={`${w.field}-${w.problem}`} className="rounded-lg border">
                <details>
                  <summary className="min-h-11 cursor-pointer px-3 py-2.5 text-sm">
                    {fieldLabel(p, w.field)}
                    {w.name}
                    {w.from && w.to && (
                      <span className="text-xs text-muted-foreground">
                        {" "}
                        · {day(w.from)} to {day(w.to)}
                      </span>
                    )}
                  </summary>
                  <div className="flex flex-col gap-1.5 px-3 pb-3 text-sm">
                    <p>{w.signs}</p>
                    {w.threshold && <p className="text-xs">Act at: {w.threshold}</p>}
                    {w.prevent?.map((x) => (
                      <p key={x} className="text-xs text-muted-foreground">
                        Prevent: {x}
                      </p>
                    ))}
                    <ul className="flex flex-col gap-1">
                      {w.options.map((o, i) => (
                        <li key={i} className="rounded-md bg-ak-bg px-2 py-1.5 text-xs">
                          {o.name ? (
                            <>
                              <span className="font-medium">{o.name}</span>
                              {o.rate && ` at ${o.rate}`}
                              {o.qty ? ` · ${amount(o.qty, o.unit ?? "")} for one spray of the field` : ""}
                              {o.water_per_acre && ` · in ${o.water_per_acre} of water an acre`}
                              {o.phi_days !== undefined && ` · wait ${o.phi_days} days before harvest`}
                              {o.last_spray && ` · not after ${day(o.last_spray)}`}
                            </>
                          ) : (
                            o.text
                          )}
                        </li>
                      ))}
                    </ul>
                  </div>
                </details>
              </li>
            ))}
          </ul>
        </div>
      )}
    </>
  );
}

function Schedule({ p }: { p: InputsPlan }) {
  return (
    <>
      <Notes notes={p.notes} />
      <ol className="flex flex-col gap-2">
        {p.schedule.map((t) => (
          <li key={`${t.field}-${t.key}`} className="rounded-lg border px-3 py-2 text-sm">
            <p className="flex flex-wrap items-baseline justify-between gap-x-3">
              <span className="font-medium">
                {fieldLabel(p, t.field)}
                {t.title}
              </span>
              <span className="text-xs text-muted-foreground tabular-nums">
                {day(t.date)} ·{" "}
                {t.das === 0
                  ? "sowing day"
                  : t.das === -1
                    ? "the day before sowing"
                    : t.das < 0
                      ? `${-t.das} days before sowing`
                      : `${t.das} days after sowing`}
              </span>
            </p>
            <ul className="mt-1 flex flex-col gap-0.5">
              {t.doses.map((d, i) => (
                <li key={i}>
                  {d.name}
                  {d.qty > 0 && (
                    <span className="tabular-nums">
                      {": "}
                      {amount(d.qty, d.unit)}
                      {d.per_plant
                        ? ` (${amount(d.per_plant, d.unit)} a plant)`
                        : d.per_acre
                          ? ` (${amount(d.per_acre, d.unit)} an acre)`
                          : ""}
                    </span>
                  )}
                  {d.qty === 0 && d.rate && ` at ${d.rate}`}
                </li>
              ))}
            </ul>
            {(t.water_per_acre || t.phi_days !== undefined) && (
              <p className="mt-1 text-xs">
                {t.water_per_acre && `In ${t.water_per_acre} of water an acre. `}
                {t.phi_days !== undefined && `Wait ${t.phi_days} days after it before harvest.`}
              </p>
            )}
            {t.cautions.length > 0 && (
              <ul className="mt-1 list-disc pl-4 text-xs text-muted-foreground">
                {t.cautions.map((c) => (
                  <li key={c}>{c}</li>
                ))}
              </ul>
            )}
          </li>
        ))}
      </ol>
    </>
  );
}

function Sell({ t }: { t: Timing }) {
  return (
    <>
      {t.sowing && (
        <div className="text-sm">
          <p className="font-medium">
            {t.sowing.shift
              ? `Sow on ${day(t.sowing.sow)} for a better harvest month (about ${t.sowing.gain_pct > 0 ? "+" : ""}${t.sowing.gain_pct.toFixed(0)}%)`
              : "Sow at the best date for the crop: no other date pays better for price."}
          </p>
          {t.sowing.reasons.slice(0, 2).map((r) => (
            <p key={r} className="text-xs text-muted-foreground">
              {r}
            </p>
          ))}
        </div>
      )}
      {t.selling && (
        <div className="text-sm">
          <p className="font-medium">
            {t.selling.sell_now
              ? "Sell when it is ready: holding does not pay here."
              : `Hold until ${months[t.selling.month]}: about +${t.selling.gain_pct.toFixed(0)}% after storage loss and cost`}
          </p>
          {t.selling.reasons.slice(0, 3).map((r) => (
            <p key={r} className="text-xs text-muted-foreground">
              {r}
            </p>
          ))}
        </div>
      )}
      <p className="text-xs text-muted-foreground">
        From the usual months of {t.commodity} prices{t.area ? ` at ${t.area}` : ""}, tested on past years. Prices can
        break the pattern: it is a guide, not a promise.
      </p>
    </>
  );
}

/** The optional parts of the current cycle, each collapsed until tapped. */
export default function Extras({ addons, sown }: { addons?: Record<string, unknown>; sown?: boolean }) {
  const [open, setOpen] = useState("");
  const inputs = addons?.inputs as InputsPlan | undefined;
  const timing = addons?.timing as Timing | undefined;
  const advice = timing && (timing.selling || timing.sowing);
  if (!inputs && !timing) return null;
  const toggle = (id: string) => () => setOpen((o) => (o === id ? "" : id));
  const sprays = inputs?.schedule.filter((t) => t.kind === "spray" || t.kind === "prevent").length ?? 0;
  return (
    <div className="mt-3" aria-label="More about this cycle">
      {inputs && (
        <Section
          title="What to buy"
          summary={[
            sown ? "" : `${inputs.buy_before.length} before sowing`,
            sown && !inputs.buy_later.length
              ? "nothing more to buy"
              : sown || inputs.buy_later.length
                ? `${inputs.buy_later.length} ${sown ? "still to buy" : "later"}`
                : "",
            inputs.if_seen.length ? `${inputs.if_seen.length} only if you see them` : "",
          ]
            .filter(Boolean)
            .join(" · ")}
          open={open === "buy"}
          onToggle={toggle("buy")}
        >
          <Buy p={inputs} sown={sown} />
        </Section>
      )}
      {inputs && inputs.schedule.length > 0 && (
        <Section
          title="Feeding and sprays"
          summary={`${inputs.schedule.length} dated steps${sprays ? `, ${sprays} for protection` : ""}`}
          open={open === "feed"}
          onToggle={toggle("feed")}
        >
          <Schedule p={inputs} />
        </Section>
      )}
      {advice && timing ? (
        <Section
          title="When to sell"
          summary={
            timing.selling
              ? timing.selling.sell_now
                ? "Sell when it is ready"
                : `Hold until ${months[timing.selling.month]}`
              : "When to sow for the price"
          }
          open={open === "sell"}
          onToggle={toggle("sell")}
        >
          <Sell t={timing} />
        </Section>
      ) : (
        timing && <p className="border-t pt-2 text-xs text-muted-foreground">When to sell: {noAdvice(timing)}</p>
      )}
    </div>
  );
}
