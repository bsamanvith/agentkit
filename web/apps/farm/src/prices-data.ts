import type { Farm } from "./types";

// The price tab is a lazy chunk: its types and calls live here, not in
// api.ts and types.ts, so the first load does not grow.

export interface Quote {
  date: string;
  state: string;
  district: string;
  market: string;
  commodity: string;
  variety?: string;
  grade?: string;
  min: number;
  max: number;
  modal: number;
  source: string;
}

export interface SupportPrice {
  commodity: string;
  variety?: string;
  season: string;
  price: number;
  source: string;
}

export interface TodayAnswer {
  answer: {
    query: { commodity?: string; state?: string; district?: string };
    markets: Quote[];
    as_of: string;
    days_old: number;
    source: string;
    widened?: string;
    elsewhere?: Quote[];
  };
  msp?: SupportPrice[];
}

export interface MonthPrice {
  month: string;
  median_modal: number;
  low: number;
  high: number;
  quotes: number;
}

export interface Range {
  low: number;
  likely: number;
  high: number;
}

export interface Estimate {
  commodity: string;
  sell_month: number;
  area: string;
  low: number;
  likely: number;
  high: number;
  now: number;
  as_of?: string;
  trend_pct: number;
  seasonal?: number[];
  confidence: "good" | "fair" | "low";
  basis: { quotes: number; markets: number; months: number; from?: string; to?: string; sources?: string };
  msp?: SupportPrice[];
  revenue?: { yield_q_per_acre: number; per_acre: Range; acres?: number; total?: Range };
  notes: string[];
}

async function get<T>(path: string, init?: RequestInit): Promise<T> {
  const resp = await fetch(path, init);
  const data = (await resp.json().catch(() => ({}))) as { error?: string };
  if (!resp.ok) throw new Error(data.error ?? `HTTP ${resp.status}`);
  return data as T;
}

const qs = (o: Record<string, string | number | undefined>) =>
  Object.entries(o)
    .filter(([, v]) => v !== undefined && v !== "")
    .map(([k, v]) => `${k}=${encodeURIComponent(String(v))}`)
    .join("&");

export const pricesApi = {
  today: (farm: string, commodity: string, signal?: AbortSignal) =>
    get<TodayAnswer>(`/api/prices?${qs({ farm, commodity })}`, { signal }),
  history: (farm: string, commodity: string, days: number, signal?: AbortSignal) =>
    get<{ months: MonthPrice[]; quotes: number }>(`/api/prices/history?${qs({ farm, commodity, days })}`, { signal }),
  estimate: (
    body: { farm: string; commodity: string; sell_month: number; yield_q_per_acre?: number },
    signal?: AbortSignal,
  ) =>
    get<Estimate>("/api/prices/estimate", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
      signal,
    }),
};

/** The crops a farmer is likely to ask about first. */
export const commonCrops: { id: string; label: string }[] = [
  { id: "tomato", label: "Tomato" },
  { id: "onion", label: "Onion" },
  { id: "rice", label: "Paddy" },
  { id: "maize", label: "Maize" },
  { id: "cotton", label: "Cotton" },
  { id: "groundnut", label: "Groundnut" },
  { id: "pigeon_pea", label: "Tur (red gram)" },
  { id: "green_gram", label: "Moong (green gram)" },
  { id: "black_gram", label: "Urad (black gram)" },
  { id: "chickpea", label: "Chana (Bengal gram)" },
  { id: "finger_millet", label: "Ragi" },
  { id: "sorghum", label: "Jowar" },
  { id: "chilli", label: "Dry chilli" },
  { id: "turmeric", label: "Turmeric" },
  { id: "brinjal", label: "Brinjal" },
  { id: "okra", label: "Bhindi (okra)" },
  { id: "potato", label: "Potato" },
  { id: "banana", label: "Banana" },
  { id: "mango", label: "Mango" },
  { id: "coconut", label: "Coconut" },
];

export const monthNames = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"];

/** ₹ in the Indian grouping: ₹1,23,456. */
export function rupees(v: number): string {
  return "₹" + Math.round(v).toLocaleString("en-IN");
}

/** The crops of a farm's plan, then the common ones, without repeats. */
export function cropChoices(farm: Farm): { id: string; label: string }[] {
  const planned = [...new Set((farm.plan?.zones ?? []).flatMap((z) => [z.crop, ...(z.with ? [z.with] : [])]))];
  const out = planned.map((id) => commonCrops.find((c) => c.id === id) ?? { id, label: id.replace(/_/g, " ") });
  for (const c of commonCrops) if (!out.some((o) => o.id === c.id)) out.push(c);
  return out;
}
