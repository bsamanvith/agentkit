import { Button } from "@agentkit/ui/components/button";
import { Input } from "@agentkit/ui/components/input";
import { Status, type StatusTone } from "@agentkit/ui";
import { useEffect, useState } from "react";
import { call } from "./api";
import { formatDate, localName } from "./plan";
import type { Farm } from "./types";

// The idea check: usual crops, new ones, ones that need a growing method
// and popular ideas that do not work on this plot, each with a plain
// verdict, the reason, a price where the app has one and the ways to sell.

type Note = { id: string; message: string };
type Fit = { score: number; sow: string; harvest: string; good_weeks: number; risks?: Note[] };
type Cost = { sqm: number; total: number; subsidy: number; farmer: number; ref?: { source: string; where: string } };
type MethodFit = Fit & {
  method: string;
  name: string;
  what: string;
  skill: "low" | "medium" | "high";
  gain?: { weeks: number; from: string; to: string };
  cost?: Cost;
};
type Quote = { date: string; market: string; modal: number };
type Price = {
  commodity: string;
  likely?: number;
  low?: number;
  high?: number;
  area?: string;
  confidence?: string;
  msp?: number;
  msp_season?: string;
  markets?: Quote[];
};
type Route = { id: string; name: string; how: string };
export type Idea = {
  crop: string;
  name: string;
  names?: Record<string, string>;
  common?: string[];
  kind: string;
  experimental?: boolean;
  verdict: "realistic" | "stretch" | "unrealistic";
  band: "common" | "new" | "method" | "unrealistic";
  why: string;
  open: Fit;
  with?: MethodFit;
  price?: Price;
  sell: Route[];
};
export type IdeasReport = { farm: string; today: string; ideas: Idea[]; notes: Note[] };

const bands: { id: Idea["band"]; title: string; about: string }[] = [
  { id: "common", title: "Usual crops here", about: "What this place and season already grow, and the plot suits." },
  {
    id: "new",
    title: "New things that could work",
    about: "They fit the plot but are not usual here: try small, find the buyer first.",
  },
  {
    id: "method",
    title: "Possible with a growing method",
    about: "A shade net, a polyhouse, a tunnel or mulch with drip makes these work, or opens an off-season crop.",
  },
  {
    id: "unrealistic",
    title: "Not realistic here",
    about: "Ideas people ask about that do not fit this plot, and why.",
  },
];

const verdicts: Record<Idea["verdict"], { label: string; tone: StatusTone }> = {
  realistic: { label: "Realistic", tone: "ok" },
  stretch: { label: "A stretch", tone: "warn" },
  unrealistic: { label: "Unrealistic", tone: "bad" },
};

const rupees = (v: number) => "₹" + Math.round(v).toLocaleString("en-IN");
const area = (sqm: number) =>
  sqm >= 4000 ? `${(sqm / 4046.86).toFixed(1)} acres` : `${Math.round(sqm).toLocaleString("en-IN")} m²`;

function IdeaCard({ idea }: { idea: Idea }) {
  const v = verdicts[idea.verdict];
  const local = localName(idea.names);
  const m = idea.with;
  const p = idea.price;
  return (
    <li className="flex flex-col gap-1.5 rounded-lg border p-3 text-sm">
      <div className="flex items-start justify-between gap-2">
        <span className="font-medium">
          {idea.name}
          {local && <span className="font-normal text-muted-foreground"> · {local}</span>}
          {idea.experimental && <span className="ml-2 rounded bg-accent px-1.5 py-0.5 text-xs">experimental</span>}
          {idea.common?.length ? (
            <span className="block font-normal text-muted-foreground">{idea.common.slice(0, 3).join(", ")}</span>
          ) : null}
        </span>
        <Status tone={v.tone}>{v.label}</Status>
      </div>
      <p>{idea.why}</p>
      <p className="text-muted-foreground">
        Open field: {idea.open.score}/100
        {idea.verdict !== "unrealistic" && <>, sow about {formatDate(idea.open.sow)}</>}
      </p>
      {m && (
        <div className="rounded bg-muted/50 p-2">
          <p>
            <span className="font-medium">{m.name}</span>: {m.score}/100
            {m.gain && <>, {m.gain.weeks} more weeks of sowing</>}. {m.what}
          </p>
          {m.cost ? (
            <p className="text-muted-foreground">
              Scheme cost norm for {area(m.cost.sqm)}: {rupees(m.cost.total)}. Subsidy up to {rupees(m.cost.subsidy)},
              so about {rupees(m.cost.farmer)} from you. A norm, not a quotation.
            </p>
          ) : (
            <p className="text-muted-foreground">Mostly your own labour: no scheme cost norm.</p>
          )}
        </div>
      )}
      {p ? (
        <p>
          {p.likely ? (
            <>
              About {rupees(p.likely)} a quintal ({p.area}, {p.confidence} confidence).{" "}
            </>
          ) : null}
          {p.msp ? (
            <>
              Support price {rupees(p.msp)} a quintal ({p.msp_season}).{" "}
            </>
          ) : null}
          {p.markets?.length ? (
            <>Near you: {p.markets.map((q) => `${q.market} ${rupees(q.modal)}`).join(", ")}.</>
          ) : null}
        </p>
      ) : (
        <p className="text-muted-foreground">No price in the app for this one: ask the buyer before you plant.</p>
      )}
      {idea.verdict !== "unrealistic" && idea.sell.length > 0 && (
        <details>
          <summary className="cursor-pointer text-muted-foreground">Where to sell ({idea.sell.length} ways)</summary>
          <ul className="mt-1 flex list-disc flex-col gap-1 pl-5">
            {idea.sell.map((r) => (
              <li key={r.id}>
                <span className="font-medium">{r.name}.</span> {r.how}
              </li>
            ))}
          </ul>
        </details>
      )}
    </li>
  );
}

/** The plot's idea check. */
export default function Ideas({ farm }: { farm: Farm }) {
  const [text, setText] = useState("");
  const [search, setSearch] = useState("");
  const key = `${farm.id}|${search}`;
  const [got, setGot] = useState<{ key: string; data?: IdeasReport; error?: string }>();

  useEffect(() => {
    const ctl = new AbortController();
    const q = search ? `?search=${encodeURIComponent(search)}` : "";
    call<IdeasReport>("GET", `/api/farms/${farm.id}/ideas${q}`, undefined, ctl.signal).then(
      (data) => setGot({ key, data }),
      (e: unknown) => {
        if (!ctl.signal.aborted) setGot({ key, error: e instanceof Error ? e.message : String(e) });
      },
    );
    return () => ctl.abort();
  }, [farm.id, search, key]);

  const now = got?.key === key ? got : undefined;
  return (
    <div className="flex flex-col gap-4">
      <form
        className="flex flex-wrap items-end gap-2"
        onSubmit={(e) => {
          e.preventDefault();
          setSearch(text.trim());
        }}
      >
        <label className="flex min-w-0 flex-1 flex-col gap-1 text-sm">
          Have an idea? Check a crop by name
          <Input
            value={text}
            onChange={(e) => setText(e.target.value)}
            placeholder="saffron, strawberry, dragon fruit…"
          />
        </label>
        <Button type="submit">Check</Button>
        {search && (
          <Button
            type="button"
            variant="outline"
            onClick={() => {
              setText("");
              setSearch("");
            }}
          >
            Show all ideas
          </Button>
        )}
      </form>
      {!now && <p className="text-sm text-muted-foreground">Checking ideas for this plot…</p>}
      {now?.error && <p className="text-sm text-destructive">{now.error}</p>}
      {now?.data &&
        bands.map((b) => {
          const ideas = (now.data?.ideas ?? []).filter((i) => i.band === b.id);
          if (ideas.length === 0) return null;
          return (
            <section key={b.id} aria-label={b.title} className="flex flex-col gap-2">
              <h3 className="font-medium">{b.title}</h3>
              <p className="text-sm text-muted-foreground">{b.about}</p>
              <ul className="grid gap-2 lg:grid-cols-2">
                {ideas.map((i) => (
                  <IdeaCard key={`${i.band}|${i.crop}`} idea={i} />
                ))}
              </ul>
            </section>
          );
        })}
      {now?.data && (
        <ul className="flex list-disc flex-col gap-1 pl-5 text-xs text-muted-foreground">
          {now.data.notes.map((n) => (
            <li key={n.id}>{n.message}</li>
          ))}
        </ul>
      )}
    </div>
  );
}
