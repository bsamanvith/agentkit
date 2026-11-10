import { Button } from "@agentkit/ui/components/button";
import { Input } from "@agentkit/ui/components/input";
import { Bell, Pencil, Plus, Send, Smartphone, Trash2, Users } from "lucide-react";
import { useCallback, useEffect, useState, type ReactNode } from "react";
import { notifyApi as api } from "./messages-api";
import { currentPush, pushUnsupported, subscribePush } from "./push";
import { byLaneOrder, describeRoutes, e164, languages, laneLabel, receiptWords, rupees } from "./messages-data";
import type { Farm, Lane, NotifyStatus, Receipt, Recipient, RecipientDraft } from "./types";

function message(e: unknown): string {
  return e instanceof Error ? e.message : String(e);
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

/**
 * Messages: who gets the plots' alerts and on which lanes (app alerts,
 * Telegram, email, WhatsApp, SMS), what each lane costs and spent, and a
 * test button. Each person agrees to each lane; a STOP from them turns it
 * off.
 */
export function MessagesPage({ farms }: { farms: Farm[] }) {
  const [status, setStatus] = useState<NotifyStatus>();
  const [people, setPeople] = useState<Recipient[]>();
  const [adding, setAdding] = useState(false);
  const [error, setError] = useState("");
  const [here, setHere] = useState<string>();

  const load = useCallback(() => {
    api.notify().then(setStatus, (e: unknown) => setError(message(e)));
    api.recipients().then(
      (ps) => setPeople([...ps].sort((a, b) => a.created_at.localeCompare(b.created_at))),
      (e: unknown) => setError(message(e)),
    );
    currentPush().then(setHere, () => setHere(undefined));
  }, []);
  useEffect(load, [load]);

  const put = (r: Recipient) => setPeople((ps) => (ps ?? []).map((p) => (p.id === r.id ? r : p)));
  const lanes = [...(status?.lanes ?? [])].sort(byLaneOrder);

  return (
    <>
      <div className="flex flex-col gap-1">
        <h1 className="text-2xl font-semibold">Messages</h1>
        <p className="max-w-2xl text-sm text-muted-foreground">
          Who gets the plots&apos; alerts, and how: in the app on their phone, on Telegram, by email, on WhatsApp or by
          SMS. Each person says yes to each one, and can reply STOP at any time. Alerts wait until morning unless they
          are urgent.
        </p>
      </div>
      {error && (
        <p role="alert" className="text-sm text-ak-bad">
          {error}
        </p>
      )}
      <div className="grid gap-4 lg:grid-cols-[minmax(0,3fr)_minmax(0,2fr)]">
        <Section title="People" icon={<Users />}>
          {!people && !error && <p className="text-sm text-muted-foreground">Loading…</p>}
          {people?.length === 0 && !adding && (
            <p className="text-sm text-muted-foreground">
              No one gets alerts outside the app yet. Add yourself first, then anyone who works the plots with you.
            </p>
          )}
          {people?.map((r) => (
            <Person
              key={r.id}
              r={r}
              lanes={lanes}
              status={status}
              farms={farms}
              here={here}
              onChanged={put}
              onHere={setHere}
              onRemoved={() => setPeople((ps) => ps?.filter((p) => p.id !== r.id))}
            />
          ))}
          {adding ? (
            <PersonForm
              lanes={lanes}
              farms={farms}
              onCancel={() => setAdding(false)}
              onSave={async (d) => {
                const r = await api.addRecipient(d);
                setPeople((ps) => [...(ps ?? []), r]);
                setAdding(false);
              }}
            />
          ) : (
            <Button variant="outline" size="sm" className="self-start" onClick={() => setAdding(true)}>
              <Plus /> Add a person
            </Button>
          )}
        </Section>
        <div className="flex flex-col gap-4">
          <Section title="Lanes" icon={<Send />}>
            {!status && !error && <p className="text-sm text-muted-foreground">Loading…</p>}
            <ul className="flex flex-col gap-2">
              {lanes
                .filter((l) => l.address !== "none")
                .map((l) => (
                  <LaneRow key={l.name} lane={l} />
                ))}
            </ul>
          </Section>
          {status && (
            <Section title="Which alerts go where" icon={<Bell />}>
              <dl className="flex flex-col gap-1.5 text-sm">
                {describeRoutes(status.routes).map((r) => (
                  <div key={r.key}>
                    <dt className="font-medium">{r.label}</dt>
                    <dd className="text-muted-foreground">{r.tree}</dd>
                  </div>
                ))}
              </dl>
              <p className="text-xs text-muted-foreground">
                &quot;Else&quot; means the next one is tried only when the first did not get through. Each alert goes
                out once on each lane, however many times it is tried.
              </p>
            </Section>
          )}
        </div>
      </div>
    </>
  );
}

function LaneRow({ lane: l }: { lane: Lane }) {
  const caps: string[] = [];
  if (l.cap.day) caps.push(`${l.today_count} of ${l.cap.day} today`);
  if (l.cap.month) caps.push(`${l.month_count} of ${l.cap.month} this month`);
  if (l.cap.month_cost) caps.push(`${rupees(l.month_cost)} of ${rupees(l.cap.month_cost)} this month`);
  return (
    <li className="flex flex-col gap-0.5 rounded-lg bg-ak-bg p-2.5 text-sm" aria-label={laneLabel(l.name)}>
      <div className="flex items-center gap-2">
        <span
          className={`size-2 shrink-0 rounded-full ${l.on ? "bg-ak-ok" : "bg-muted-foreground/40"}`}
          aria-hidden="true"
        />
        <span className="font-medium">{laneLabel(l.name)}</span>
        <span className={`text-xs ${l.paid ? "text-ak-warn" : "text-ak-ok"}`}>
          {l.paid ? `Paid${l.cost ? `, about ${rupees(l.cost)} a message` : ""}` : "Free"}
        </span>
        <span className="ml-auto text-xs text-muted-foreground">{l.on ? "On" : "Off"}</span>
      </div>
      {!l.on && l.why && <p className="text-xs text-muted-foreground">{l.why}</p>}
      {l.on && caps.length > 0 && <p className="text-xs text-muted-foreground tabular-nums">{caps.join(" · ")}</p>}
    </li>
  );
}

/** The lanes a person can agree to on the form: those whose address they gave. */
function formLanes(lanes: Lane[], phone: string, email: string): Lane[] {
  return lanes.filter((l) => (l.address === "phone" && phone) || (l.address === "email" && email));
}

function PersonForm({
  r,
  lanes,
  farms,
  onSave,
  onCancel,
}: {
  r?: Recipient;
  lanes: Lane[];
  farms: Farm[];
  onSave: (d: RecipientDraft) => Promise<void>;
  onCancel: () => void;
}) {
  const [name, setName] = useState(r?.name ?? "");
  const [phone, setPhone] = useState(r?.phone ?? "");
  const [email, setEmail] = useState(r?.email ?? "");
  const [lang, setLang] = useState(r?.lang ?? "te");
  const [plots, setPlots] = useState<string[]>(r?.farms ?? []);
  const [agreed, setAgreed] = useState<string[]>([]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const offered = r ? [] : formLanes(lanes, phone.trim(), email.trim());
  const toggle = (xs: string[], x: string) => (xs.includes(x) ? xs.filter((y) => y !== x) : [...xs, x]);
  return (
    <form
      aria-label={r ? `Edit ${r.name}` : "Add a person"}
      className="flex flex-col gap-3 rounded-lg border p-3"
      onSubmit={(e) => {
        e.preventDefault();
        setBusy(true);
        setError("");
        const d: RecipientDraft = {
          name: name.trim(),
          phone: phone.trim() ? e164(phone) : undefined,
          email: email.trim() || undefined,
          lang,
          farms: plots.length ? plots : undefined,
        };
        if (!r) d.consent = agreed.filter((a) => offered.some((l) => l.name === a));
        onSave(d)
          .catch((e: unknown) => setError(message(e)))
          .finally(() => setBusy(false));
      }}
    >
      <div className="grid gap-3 sm:grid-cols-2">
        <label className="flex flex-col gap-1 text-sm">
          Name
          <Input required value={name} onChange={(e) => setName(e.target.value)} autoComplete="name" />
        </label>
        <label className="flex flex-col gap-1 text-sm">
          Language
          <select
            className="h-9 rounded-md border bg-transparent px-2 text-sm"
            value={lang}
            onChange={(e) => setLang(e.target.value)}
          >
            {languages.map(([v, l]) => (
              <option key={v} value={v}>
                {l}
              </option>
            ))}
          </select>
        </label>
        <label className="flex flex-col gap-1 text-sm">
          Mobile number
          <Input
            type="tel"
            inputMode="tel"
            placeholder="98765 43210"
            value={phone}
            onChange={(e) => setPhone(e.target.value)}
            autoComplete="tel"
          />
        </label>
        <label className="flex flex-col gap-1 text-sm">
          Email
          <Input type="email" value={email} onChange={(e) => setEmail(e.target.value)} autoComplete="email" />
        </label>
      </div>
      {r && (r.phone !== (phone.trim() ? e164(phone) : undefined) || (r.email ?? "") !== email.trim()) && (
        <p className="text-xs text-muted-foreground">
          A new number or email has not agreed to anything yet: tick its lanes again after saving.
        </p>
      )}
      {farms.length > 1 && (
        <fieldset className="flex flex-col gap-1 text-sm">
          <legend className="mb-1">Alerts for which plots? (none ticked: all)</legend>
          <div className="flex flex-wrap gap-x-4 gap-y-1">
            {farms.map((f) => (
              <label key={f.id} className="flex items-center gap-1.5">
                <input
                  type="checkbox"
                  checked={plots.includes(f.id)}
                  onChange={() => setPlots((ps) => toggle(ps, f.id))}
                />
                {f.name}
              </label>
            ))}
          </div>
        </fieldset>
      )}
      {offered.length > 0 && (
        <fieldset className="flex flex-col gap-1 text-sm">
          <legend className="mb-1">They agreed to get alerts by</legend>
          <div className="flex flex-wrap gap-x-4 gap-y-1">
            {offered.map((l) => (
              <label key={l.name} className="flex items-center gap-1.5">
                <input
                  type="checkbox"
                  checked={agreed.includes(l.name)}
                  onChange={() => setAgreed((a) => toggle(a, l.name))}
                />
                {laneLabel(l.name)}
                {l.paid && <span className="text-xs text-muted-foreground">(paid)</span>}
              </label>
            ))}
          </div>
        </fieldset>
      )}
      {error && (
        <p role="alert" className="text-sm text-ak-bad">
          {error}
        </p>
      )}
      <div className="flex gap-2">
        <Button type="submit" size="sm" disabled={busy || !name.trim()}>
          {r ? "Save" : "Add"}
        </Button>
        <Button type="button" variant="ghost" size="sm" onClick={onCancel}>
          Cancel
        </Button>
      </div>
    </form>
  );
}

function Person({
  r,
  lanes,
  status,
  farms,
  here,
  onChanged,
  onHere,
  onRemoved,
}: {
  r: Recipient;
  lanes: Lane[];
  status?: NotifyStatus;
  farms: Farm[];
  here?: string;
  onChanged: (r: Recipient) => void;
  onHere: (endpoint?: string) => void;
  onRemoved: () => void;
}) {
  const [editing, setEditing] = useState(false);
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");
  const [link, setLink] = useState<{ code: string; telegram_url?: string }>();
  const [test, setTest] = useState<Receipt[]>();
  const [removing, setRemoving] = useState(false);

  const run = <T,>(what: string, p: () => Promise<T>, then?: (v: T) => void) => {
    setBusy(what);
    setError("");
    p()
      .then(
        (v) => then?.(v),
        (e: unknown) => setError(message(e)),
      )
      .finally(() => setBusy(""));
  };

  if (editing)
    return (
      <PersonForm
        r={r}
        lanes={lanes}
        farms={farms}
        onCancel={() => setEditing(false)}
        onSave={async (d) => {
          onChanged(await api.updateRecipient(r.id, d));
          setEditing(false);
        }}
      />
    );

  const onHerePhone = !!here && (r.push ?? []).some((p) => p.endpoint === here);
  const plots = (r.farms ?? []).map((id) => farms.find((f) => f.id === id)?.name ?? id);
  const pushLane = lanes.find((l) => l.name === "push");
  const telegram = lanes.find((l) => l.name === "telegram");

  return (
    <article className="flex flex-col gap-3 rounded-lg border p-3" aria-label={r.name}>
      <div className="flex flex-wrap items-start gap-2">
        <div className="flex min-w-0 flex-col">
          <h3 className="font-medium">{r.name}</h3>
          <p className="text-xs text-muted-foreground">
            {[r.phone, r.email, plots.length ? `Plots: ${plots.join(", ")}` : "All plots"].filter(Boolean).join(" · ")}
          </p>
        </div>
        <div className="ml-auto flex gap-1">
          <Button variant="ghost" size="sm" aria-label={`Edit ${r.name}`} onClick={() => setEditing(true)}>
            <Pencil />
          </Button>
          {removing ? (
            <Button
              variant="outline"
              size="sm"
              disabled={busy === "remove"}
              onClick={() => run("remove", () => api.removeRecipient(r.id), onRemoved)}
            >
              Remove {r.name}?
            </Button>
          ) : (
            <Button variant="ghost" size="sm" aria-label={`Remove ${r.name}`} onClick={() => setRemoving(true)}>
              <Trash2 />
            </Button>
          )}
        </div>
      </div>

      <ul className="flex flex-col gap-1.5" aria-label={`${r.name}'s lanes`}>
        {lanes
          .filter((l) => l.address !== "none")
          .map((l) => {
            const c = r.consent?.[l.name];
            const addr =
              l.address === "phone"
                ? r.phone
                : l.address === "email"
                  ? r.email
                  : l.address === "telegram"
                    ? r.telegram
                    : (r.push?.length ?? 0) > 0
                      ? `${r.push?.length} phone${r.push?.length === 1 ? "" : "s"}`
                      : "";
            const stopped = c && !c.opted_in && c.source !== "settings";
            return (
              <li key={l.name} className="flex flex-wrap items-center gap-x-2 gap-y-1 text-sm">
                <label className="flex items-center gap-1.5">
                  <input
                    type="checkbox"
                    aria-label={`${laneLabel(l.name)} for ${r.name}`}
                    checked={!!c?.opted_in}
                    disabled={!addr || busy !== ""}
                    onChange={(e) => {
                      const on = e.target.checked;
                      run(`consent-${l.name}`, () => api.consent(r.id, l.name, on), onChanged);
                    }}
                  />
                  {laneLabel(l.name)}
                </label>
                <span className="text-xs text-muted-foreground">
                  {!addr
                    ? l.address === "phone"
                      ? "add a mobile number"
                      : l.address === "email"
                        ? "add an email"
                        : l.address === "telegram"
                          ? "not linked yet"
                          : "no phone yet"
                    : stopped
                      ? `stopped by ${r.name}`
                      : !l.on
                        ? "lane is off"
                        : ""}
                </span>
              </li>
            );
          })}
      </ul>

      <div className="flex flex-wrap gap-2">
        {pushLane && (
          <>
            {onHerePhone ? (
              <Button
                variant="outline"
                size="sm"
                disabled={busy !== ""}
                onClick={() =>
                  run(
                    "push",
                    async () => {
                      if (here) await api.dropPush(r.id, here);
                      return api.recipients();
                    },
                    (ps) => {
                      const me = ps.find((p) => p.id === r.id);
                      if (me) onChanged(me);
                    },
                  )
                }
              >
                <Smartphone /> Stop alerts on this phone
              </Button>
            ) : (
              <Button
                variant="outline"
                size="sm"
                disabled={busy !== "" || !status?.push_key || !!pushUnsupported()}
                title={pushUnsupported() || undefined}
                onClick={() =>
                  run(
                    "push",
                    async () => {
                      const sub = await subscribePush(status?.push_key ?? "");
                      onHere(sub.endpoint);
                      return api.addPush(r.id, sub);
                    },
                    onChanged,
                  )
                }
              >
                <Smartphone /> Get alerts on this phone
              </Button>
            )}
          </>
        )}
        {telegram?.on && !r.telegram && (
          <Button
            variant="outline"
            size="sm"
            disabled={busy !== ""}
            onClick={() => run("link", () => api.linkTelegram(r.id), setLink)}
          >
            <Send /> Link Telegram
          </Button>
        )}
        <Button
          variant="outline"
          size="sm"
          disabled={busy !== ""}
          onClick={() =>
            run(
              "test",
              () => api.testMessage(r.id),
              (t) => setTest(t.receipts),
            )
          }
        >
          <Bell /> Send a test
        </Button>
      </div>

      {link && (
        <p className="rounded-md bg-ak-bg p-2 text-sm">
          {link.telegram_url ? (
            <>
              Open{" "}
              <a className="underline" href={link.telegram_url} target="_blank" rel="noreferrer">
                the farm bot on Telegram
              </a>{" "}
              on {r.name}&apos;s phone and press Start.
            </>
          ) : (
            <>Send this to the farm bot on Telegram:</>
          )}{" "}
          The code is <code className="font-mono font-medium">{link.code}</code>.
        </p>
      )}
      {test && (
        <ul className="flex flex-col gap-0.5 rounded-md bg-ak-bg p-2 text-sm" aria-label="Test result">
          {test.length === 0 && <li>No lane took it: tick a lane above first.</li>}
          {test.map((t, i) => (
            <li key={i}>
              {laneLabel(t.lane)}: {receiptWords(t)}
            </li>
          ))}
        </ul>
      )}
      {error && (
        <p role="alert" className="text-sm text-ak-bad">
          {error}
        </p>
      )}
    </article>
  );
}
