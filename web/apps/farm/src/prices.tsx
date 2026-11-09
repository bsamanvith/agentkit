import { Button } from "@agentkit/ui/components/button";
import { Status } from "@agentkit/ui";
import { AlertTriangle, IndianRupee, RefreshCw } from "lucide-react";
import { useEffect, useMemo, useState, type ReactNode } from "react";
import {
  cropChoices,
  monthNames,
  pricesApi,
  rupees,
  type Estimate,
  type MonthPrice,
  type Quote,
  type TodayAnswer,
} from "./prices-data";
import type { Farm } from "./types";

// The price tab is a lazy chunk: its calls and types are in
// prices-data.ts, imported only from here, so the first load does not grow.

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

/** Monthly median prices as bars, with each month's low to high. */
export function PriceChart({ months }: { months: MonthPrice[] }) {
  if (months.length === 0) return <p className="text-sm text-muted-foreground">No saved history yet.</p>;
  // The axis starts near the lowest price, so a month's swing shows.
  const top = Math.max(...months.map((m) => m.high)) * 1.03;
  const floor = Math.min(...months.map((m) => m.low)) * 0.9;
  const w = 32;
  const h = 140;
  const left = 52;
  const y = (v: number) => h - ((v - floor) / (top - floor)) * h + 4;
  const ticks = [floor, (floor + top) / 2, top].map((v) => Math.ceil(v / 100) * 100).filter((v) => v <= top);
  return (
    <div className="overflow-x-auto">
      <svg
        viewBox={`0 0 ${left + months.length * w + 8} ${h + 34}`}
        className="h-48 max-w-full min-w-[28rem]"
        role="img"
        aria-label="Median price by month"
      >
        {ticks.map((v) => (
          <g key={v}>
            <line
              x1={left - 4}
              x2={left + months.length * w}
              y1={y(v)}
              y2={y(v)}
              stroke="currentColor"
              opacity={0.12}
            />
            <text x={left - 8} y={y(v) + 3} textAnchor="end" fontSize={10} fill="currentColor" opacity={0.6}>
              {rupees(v)}
            </text>
          </g>
        ))}
        {months.map((m, i) => {
          const x = left + i * w;
          return (
            <g key={m.month}>
              <title>{`${m.month}: median ${rupees(m.median_modal)}, ${rupees(m.low)} to ${rupees(m.high)} (${m.quotes} prices)`}</title>
              <rect
                x={x + 6}
                y={y(m.median_modal)}
                width={w - 12}
                height={h + 4 - y(m.median_modal)}
                rx={3}
                fill="#4d7c0f"
                opacity={0.85}
              />
              <line
                x1={x + w / 2}
                x2={x + w / 2}
                y1={y(m.high)}
                y2={y(m.low)}
                stroke="currentColor"
                opacity={0.35}
                strokeWidth={2}
              />
              <text x={x + w / 2} y={h + 20} textAnchor="middle" fontSize={10} fill="currentColor" opacity={0.7}>
                {monthNames[Number(m.month.slice(5)) - 1]}
              </text>
              {(i === 0 || m.month.endsWith("-01")) && (
                <text x={x + w / 2} y={h + 31} textAnchor="middle" fontSize={9} fill="currentColor" opacity={0.5}>
                  {m.month.slice(0, 4)}
                </text>
              )}
            </g>
          );
        })}
      </svg>
    </div>
  );
}

function marketRow(q: Quote) {
  return (
    <tr key={`${q.market}|${q.variety}|${q.grade}`} className="border-b last:border-0">
      <td className="py-1.5 pr-3">
        {q.market}
        <span className="block text-xs text-muted-foreground">{q.district}</span>
      </td>
      <td className="py-1.5 pr-3">{q.variety || "—"}</td>
      <td className="py-1.5 pr-3 text-right font-medium tabular-nums">{rupees(q.modal)}</td>
      <td className="py-1.5 pr-3 text-right text-muted-foreground tabular-nums">
        {q.min ? `${rupees(q.min)} to ${rupees(q.max)}` : "—"}
      </td>
      <td className="py-1.5 text-muted-foreground tabular-nums">{q.date}</td>
    </tr>
  );
}

/** An answer with the request it answers. */
type Got<T> = { key: string; data?: T; error?: string } | undefined;

const confidenceTone = { good: "ok", fair: "warn", low: "bad" } as const;

/**
 * Crop prices at a plot: today's mandi prices near it, the year's prices
 * month by month, and what the crop may fetch in the month the farmer
 * plans to sell, with the MSP beside it.
 */
export function Prices({ farm }: { farm: Farm }) {
  const choices = useMemo(() => cropChoices(farm), [farm]);
  const [crop, setCrop] = useState(choices[0]?.id ?? "tomato");
  const [sellMonth, setSellMonth] = useState(new Date().getMonth() + 1);
  const [yieldQ, setYieldQ] = useState("");
  const [reload, setReload] = useState(0);
  // Each answer is kept with the request it answers; one for another
  // request is not shown, so changing the crop shows "loading" at once.
  const key = `${farm.id}|${crop}|${reload}`;
  const estKey = `${key}|${sellMonth}|${yieldQ}`;
  const [todayGot, setTodayGot] = useState<Got<TodayAnswer>>();
  const [monthsGot, setMonthsGot] = useState<Got<MonthPrice[]>>();
  const [estimateGot, setEstimateGot] = useState<Got<Estimate>>();

  useEffect(() => {
    const ctl = new AbortController();
    pricesApi.today(farm.id, crop, ctl.signal).then(
      (data) => setTodayGot({ key, data }),
      (e: unknown) => {
        if (!ctl.signal.aborted) setTodayGot({ key, error: message(e) });
      },
    );
    pricesApi.history(farm.id, crop, 365, ctl.signal).then(
      (r) => setMonthsGot({ key, data: r.months }),
      () => {
        if (!ctl.signal.aborted) setMonthsGot({ key, data: [] });
      },
    );
    return () => ctl.abort();
  }, [farm.id, crop, key]);

  useEffect(() => {
    const ctl = new AbortController();
    const y = Number(yieldQ);
    pricesApi
      .estimate(
        { farm: farm.id, commodity: crop, sell_month: sellMonth, yield_q_per_acre: y > 0 ? y : undefined },
        ctl.signal,
      )
      .then(
        (data) => setEstimateGot({ key: estKey, data }),
        (e: unknown) => {
          if (!ctl.signal.aborted) setEstimateGot({ key: estKey, error: message(e) });
        },
      );
    return () => ctl.abort();
  }, [farm.id, crop, sellMonth, yieldQ, estKey]);

  const today = todayGot?.key === key ? (todayGot.data ?? null) : null;
  const todayError = todayGot?.key === key ? (todayGot.error ?? "") : "";
  const months = monthsGot?.key === key ? (monthsGot.data ?? null) : null;
  const estimate = estimateGot?.key === estKey ? (estimateGot.data ?? null) : null;
  const estimateError = estimateGot?.key === estKey ? (estimateGot.error ?? "") : "";

  const where = farm.report.location?.district ?? farm.report.location?.state ?? "this plot";
  const msp = today?.msp ?? estimate?.msp ?? [];
  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-end gap-3">
        <label className="flex flex-col gap-1 text-sm">
          <span className="text-muted-foreground">Crop</span>
          <select
            className="h-9 rounded-md border bg-ak-surface px-2"
            value={crop}
            onChange={(e) => setCrop(e.target.value)}
            aria-label="Crop"
          >
            {choices.map((c) => (
              <option key={c.id} value={c.id}>
                {c.label}
              </option>
            ))}
          </select>
        </label>
        <Button variant="outline" size="sm" onClick={() => setReload((n) => n + 1)}>
          <RefreshCw /> Refresh
        </Button>
        {!farm.report.location?.state && (
          <p className="text-sm text-ak-warn">
            The plot's state is not known: check its location to see prices near it.
          </p>
        )}
      </div>

      <Card
        title={`Mandi prices near ${where}`}
        aside={
          today && (
            <span className="text-xs text-muted-foreground">
              {today.answer.as_of}
              {today.answer.days_old > 1 ? ` (${today.answer.days_old} days ago)` : ""} · {today.answer.source}
            </span>
          )
        }
      >
        {todayError ? (
          <p className="flex items-center gap-2 text-sm text-muted-foreground">
            <AlertTriangle className="size-4 text-ak-warn" aria-hidden="true" /> {todayError}
          </p>
        ) : !today ? (
          <p className="text-sm text-muted-foreground">Looking up prices…</p>
        ) : (
          <>
            {today.answer.widened && <p className="mb-2 text-sm text-ak-warn">{today.answer.widened}</p>}
            <div className="overflow-x-auto">
              <table className="w-full text-sm">
                <thead>
                  <tr className="border-b text-left text-xs text-muted-foreground">
                    <th className="py-1.5 pr-3 font-medium">Market</th>
                    <th className="py-1.5 pr-3 font-medium">Variety</th>
                    <th className="py-1.5 pr-3 text-right font-medium">Modal</th>
                    <th className="py-1.5 pr-3 text-right font-medium">Min to max</th>
                    <th className="py-1.5 font-medium">Day</th>
                  </tr>
                </thead>
                <tbody>{today.answer.markets.slice(0, 15).map(marketRow)}</tbody>
                {(today.answer.elsewhere?.length ?? 0) > 0 && (
                  <tbody>
                    <tr>
                      <th colSpan={5} className="pt-4 pb-1 text-left text-xs font-medium text-muted-foreground">
                        Elsewhere in {today.answer.query.state}
                      </th>
                    </tr>
                    {today.answer.elsewhere?.map(marketRow)}
                  </tbody>
                )}
              </table>
            </div>
            <p className="mt-2 text-xs text-muted-foreground">
              Rupees a quintal (100 kg). Modal is the most common price of the day.
            </p>
          </>
        )}
      </Card>

      <Card title="The last year, month by month">
        {months === null ? <p className="text-sm text-muted-foreground">Loading…</p> : <PriceChart months={months} />}
      </Card>

      <Card
        title="What it may fetch"
        aside={
          estimate &&
          estimate.likely > 0 && (
            <Status tone={confidenceTone[estimate.confidence]}>{estimate.confidence} confidence</Status>
          )
        }
      >
        <div className="mb-3 flex flex-wrap gap-3">
          <label className="flex flex-col gap-1 text-sm">
            <span className="text-muted-foreground">Selling in</span>
            <select
              className="h-9 rounded-md border bg-ak-surface px-2"
              value={sellMonth}
              onChange={(e) => setSellMonth(Number(e.target.value))}
              aria-label="Selling month"
            >
              {monthNames.map((m, i) => (
                <option key={m} value={i + 1}>
                  {m}
                </option>
              ))}
            </select>
          </label>
          <label className="flex flex-col gap-1 text-sm">
            <span className="text-muted-foreground">Expected yield (quintals an acre)</span>
            <input
              className="h-9 w-40 rounded-md border bg-ak-surface px-2"
              inputMode="decimal"
              value={yieldQ}
              onChange={(e) => setYieldQ(e.target.value.replace(/[^0-9.]/g, ""))}
              placeholder="optional"
              aria-label="Expected yield in quintals an acre"
            />
          </label>
        </div>
        {estimateError ? (
          <p className="text-sm text-muted-foreground">{estimateError}</p>
        ) : !estimate ? (
          <p className="text-sm text-muted-foreground">Estimating…</p>
        ) : (
          <EstimateView e={estimate} />
        )}
      </Card>

      {msp.length > 0 && (
        <Card title="Minimum support price">
          <ul className="text-sm">
            {msp.map((p) => (
              <li key={`${p.variety}${p.season}`} className="flex justify-between gap-3 border-b py-1.5 last:border-0">
                <span>
                  {p.variety || p.commodity} <span className="text-muted-foreground">· {p.season}</span>
                </span>
                <span className="font-medium tabular-nums">{rupees(p.price)}</span>
              </li>
            ))}
          </ul>
          <p className="mt-2 text-xs text-muted-foreground">
            Paid where the government procures, not at every mandi. {msp[0]?.source}.
          </p>
        </Card>
      )}
    </div>
  );
}

export function EstimateView({ e }: { e: Estimate }) {
  return (
    <div className="flex flex-col gap-3">
      {e.likely > 0 && (
        <div className="grid grid-cols-3 gap-2">
          {(
            [
              ["Low", e.low],
              ["Likely", e.likely],
              ["High", e.high],
            ] as const
          ).map(([label, v]) => (
            <div key={label} className={`rounded-lg border p-3 ${label === "Likely" ? "border-[#4d7c0f]" : ""}`}>
              <p className="text-xs text-muted-foreground">{label}</p>
              <p className="flex items-center text-lg font-semibold tabular-nums sm:text-xl">
                <IndianRupee className="size-4" aria-hidden="true" />
                {Math.round(v).toLocaleString("en-IN")}
              </p>
            </div>
          ))}
        </div>
      )}
      {e.revenue && (
        <p className="text-sm">
          At {e.revenue.yield_q_per_acre} quintals an acre: {rupees(e.revenue.per_acre.likely)} an acre
          {e.revenue.total && e.revenue.acres
            ? `, ${rupees(e.revenue.total.likely)} for the plot's ${e.revenue.acres} acres`
            : ""}{" "}
          ({rupees((e.revenue.total ?? e.revenue.per_acre).low)} to{" "}
          {rupees((e.revenue.total ?? e.revenue.per_acre).high)}), before costs.
        </p>
      )}
      <ul className="list-disc space-y-1 pl-5 text-sm text-muted-foreground">
        {e.notes.map((n) => (
          <li key={n}>{n}</li>
        ))}
      </ul>
      {e.basis.quotes > 0 && (
        <p className="text-xs text-muted-foreground">
          From {e.basis.quotes.toLocaleString("en-IN")} prices in {e.basis.markets} market
          {e.basis.markets === 1 ? "" : "s"} ({e.area}), {e.basis.from} to {e.basis.to}; {e.basis.sources}.
        </p>
      )}
    </div>
  );
}
