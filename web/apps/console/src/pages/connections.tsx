// Connectors (docs/UI.md "Connectors"): everyone's page of the keys and
// accounts the server's connectors need, as cards grouped by provider. A key is set in a dialog and sent once
// (PUT /v1/credentials/{slot}); an account is connected through the
// provider in a popup (POST /v1/credentials/{slot}/connect), which ends at
// /connections/done and reports back here. Values never reach the page:
// the API sends none, and a typed key leaves the DOM with its dialog.

import {
  ApiError,
  NetworkError,
  type Agent,
  type AgentClient,
  type CredentialInfo,
  type Toolset,
} from "@agentkit/agent-ui/api";
import { Status } from "@agentkit/ui";
import { Button } from "@agentkit/ui/components/button";
import { Input } from "@agentkit/ui/components/input";
import { CircleAlert, KeyRound } from "lucide-react";
import { useEffect, useEffectEvent, useRef, useState, type FormEvent } from "react";
import { useLocation, useNavigate } from "react-router";
import { useApi, useResource } from "../api";
import { canSetTenantCredentials } from "../access";
import {
  connectChannel,
  connectionRows,
  connectPopupKey,
  connectReturnKey,
  connectWindow,
  inPopup,
  isLocalPath,
  matches,
  resultText,
  type Action,
  type ConnectResult,
  type Row,
  type Slot,
} from "./connections-model";
import { groups } from "./connectors-catalog";
import { Empty, Loaded, Page } from "./page";
import { Modal } from "./parts";

type Data = { credentials: CredentialInfo[] | null; toolsets: Toolset[]; agents: Agent[] };

/** The three lists the page is made of; credentials null when the server keeps none (404). */
async function loadConnections(c: AgentClient, signal: AbortSignal): Promise<Data> {
  const [credentials, toolsets, agents] = await Promise.all([
    c.listCredentials({ signal }).catch((e: unknown) => {
      if (e instanceof ApiError && e.status === 404 && e.code === "not_found") return null;
      throw e;
    }),
    c.listToolsets({ signal }),
    c.listAgents({ signal }),
  ]);
  return { credentials, toolsets, agents };
}

/** The note a result leaves under its slot's rows. */
function noteFor(r: ConnectResult): Record<string, { text: string; ok: boolean }> {
  const t = resultText(r);
  return { [`slot:${r.slot}:user`]: t, [`slot:${r.slot}:tenant`]: t };
}

/** An error to show in a dialog or under a row: the server's message, never a value we sent. */
function errorText(e: unknown, what: string): string {
  if (e instanceof ApiError) return `${what}: ${e.message} (${e.status} ${e.code}).`;
  if (e instanceof NetworkError) return `${what}: couldn't reach the server.`;
  return `${what}.`;
}

function KeyDialog({
  row,
  slot,
  replacing,
  onClose,
  onSaved,
}: {
  row: Row;
  slot: Slot;
  replacing: boolean;
  onClose: () => void;
  onSaved: () => void;
}) {
  const { client } = useApi();
  const [shown, setShown] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const input = useRef<HTMLInputElement>(null);
  const mine = slot.scope === "user";
  const title = replacing
    ? `Replace ${mine ? "your" : "the tenant's"} ${row.title} key`
    : `Connect ${row.title}${mine ? "" : " for everyone"}`;
  const submit = (e: FormEvent) => {
    e.preventDefault();
    const key = input.current?.value.trim() ?? "";
    if (!key) {
      setError("Paste a key first.");
      input.current?.focus();
      return;
    }
    setBusy(true);
    setError("");
    client.putCredential(slot.name, { scope: slot.scope, kind: "api_key", api_key: key }).then(
      () => {
        if (input.current) input.current.value = ""; // out of the DOM before the dialog goes
        onSaved();
      },
      (err: unknown) => {
        setBusy(false);
        setError(errorText(err, `The server didn't save the key`));
        input.current?.focus();
      },
    );
  };
  return (
    <Modal title={title} onClose={onClose}>
      <form noValidate onSubmit={submit} className="flex flex-col gap-3">
        <label htmlFor="conn-key" className="text-(length:--ak-text-sm) font-medium">
          API key
        </label>
        <div className="flex gap-2">
          <Input
            ref={input}
            id="conn-key"
            name="api-key"
            type={shown ? "text" : "password"}
            autoComplete="off"
            spellCheck={false}
            autoCapitalize="off"
            readOnly={busy}
            data-autofocus=""
            aria-invalid={error ? true : undefined}
            aria-describedby={error ? "conn-key-err conn-key-help" : "conn-key-help"}
            className="font-mono"
          />
          <Button
            type="button"
            variant="outline"
            aria-pressed={shown}
            aria-label={shown ? "Hide key" : "Show key"}
            onClick={() => setShown(!shown)}
          >
            {shown ? "Hide" : "Show"}
          </Button>
        </div>
        {error ? (
          <p id="conn-key-err" role="alert" className="flex items-start gap-1.5 text-(length:--ak-text-sm) text-ak-bad">
            <CircleAlert aria-hidden="true" className="mt-0.5 size-4 flex-none" />
            <span>{error}</span>
          </p>
        ) : null}
        <p id="conn-key-help" className="text-xs text-ak-muted">
          {row.keyHelp ? `Get a key at ${row.keyHelp}. ` : ""}
          {mine
            ? "Used only for your runs. It's sent once and never shown again, not even in part."
            : "Shared with everyone in the tenant. It's sent once and never shown again, not even in part."}
        </p>
        <div className="flex justify-end gap-2">
          <Button type="button" variant="outline" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" disabled={busy}>
            {busy ? "Saving…" : "Save"}
          </Button>
        </div>
      </form>
    </Modal>
  );
}

function DisconnectDialog({
  row,
  slot,
  onClose,
  onDone,
}: {
  row: Row;
  slot: Slot;
  onClose: () => void;
  onDone: () => void;
}) {
  const { client } = useApi();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const mine = slot.scope === "user";
  const go = () => {
    setBusy(true);
    client.deleteCredential(slot.name, slot.scope).then(onDone, (err: unknown) => {
      setBusy(false);
      setError(errorText(err, "It wasn't disconnected"));
    });
  };
  return (
    <Modal title={`Disconnect ${row.title}?`} onClose={onClose}>
      <p className="text-(length:--ak-text-sm) text-ak-text-2">
        {mine
          ? `${row.title} stops working for you until you connect again.`
          : `${row.title} stops working for everyone who uses the tenant's until it's set again.`}{" "}
        {row.usedBy.length > 0 ? `Used by ${row.usedBy.join(", ")}. ` : ""}It isn&apos;t revoked at the provider.
      </p>
      {error ? (
        <p role="alert" className="mt-2 text-(length:--ak-text-sm) text-ak-bad">
          {error}
        </p>
      ) : null}
      <div className="mt-4 flex justify-end gap-2">
        <Button type="button" variant="outline" data-autofocus="" onClick={onClose}>
          Cancel
        </Button>
        <Button type="button" disabled={busy} onClick={go}>
          Disconnect
        </Button>
      </div>
    </Modal>
  );
}

const actionWords: Record<Action, string> = {
  connect: "Connect",
  replace: "Replace key",
  reconnect: "Reconnect",
  disconnect: "Disconnect",
};

type Dialog = { row: Row; slot: Slot; kind: "key" | "replace" | "disconnect" } | null;
type Waiting = { rowId: string; popup: Window | null } | null;

/** One connector's card: what it is, its status, and what you can do. */
function ConnectorCard({
  row,
  waiting,
  note,
  onAct,
  onCancel,
}: {
  row: Row;
  waiting: boolean;
  note?: { text: string; ok: boolean };
  onAct: (a: Action) => void;
  onCancel: () => void;
}) {
  const id = `card-${row.id.replaceAll(":", "-")}`;
  return (
    <li data-row={row.id}>
      <article
        aria-labelledby={id}
        className="flex h-full flex-col gap-2 rounded-md border bg-ak-surface p-3 text-(length:--ak-text-sm)"
      >
        <div className="flex items-baseline justify-between gap-2">
          <h3 id={id} className="font-medium text-ak-text">
            {row.title}
            {row.title !== row.name ? (
              <span className="ml-1.5 font-mono text-xs font-normal text-ak-muted">{row.name}</span>
            ) : null}
          </h3>
          <span className="text-xs text-ak-muted">{[row.kind, row.owner].filter(Boolean).join(" · ")}</span>
        </div>
        {row.does ? <p className="text-ak-text-2">{row.does}</p> : null}
        <div>
          {waiting ? (
            <Status tone="neutral">Waiting for {row.title}… Finish in the window that opened.</Status>
          ) : (
            <Status tone={row.tone}>{row.word}</Status>
          )}
          {row.detail && !waiting ? <span className="mt-0.5 block text-xs text-ak-muted">{row.detail}</span> : null}
          {note?.text && !note.ok ? (
            <span role="alert" className="mt-1 block text-xs text-ak-bad">
              {note.text}
            </span>
          ) : null}
        </div>
        <p className="text-xs text-ak-muted">
          {row.usedBy.length > 0 ? (
            <>
              Used by <span className="font-mono">{row.usedBy.join(", ")}</span>
            </>
          ) : (
            "No agent uses it yet"
          )}
        </p>
        <div className="flex flex-wrap gap-2">
          {waiting ? (
            <Button
              type="button"
              variant="outline"
              size="sm"
              aria-label={`Cancel connecting ${row.title}`}
              onClick={onCancel}
            >
              Cancel
            </Button>
          ) : (
            row.actions.map((a) => (
              <Button
                key={a}
                type="button"
                size="sm"
                variant={a === "disconnect" ? "ghost" : row.needsYou ? "default" : "outline"}
                aria-haspopup={a === "disconnect" || !row.slot?.oauth ? "dialog" : undefined}
                aria-label={`${actionWords[a]} ${row.title}`}
                onClick={() => onAct(a)}
              >
                {actionWords[a]}
              </Button>
            ))
          )}
        </div>
      </article>
    </li>
  );
}

export function ConnectionsPage() {
  const { client, me, epoch } = useApi();
  const location = useLocation();
  const navigate = useNavigate();
  const res = useResource(loadConnections, "connections");
  // The lists read again after a change, shown in place of the first load
  // (nothing shifts while they come); only for the credential they were
  // read with.
  const [fresh, setFresh] = useState<{ epoch: number; data: Data } | null>(null);
  // A result handed over by /connections/done after a same-tab connect.
  const [handed] = useState(() => (location.state as { connect?: ConnectResult } | null)?.connect);
  const [dialog, setDialog] = useState<Dialog>(null);
  const [waiting, setWaiting] = useState<Waiting>(null);
  const [notes, setNotes] = useState<Record<string, { text: string; ok: boolean }>>(() =>
    handed ? noteFor(handed) : {},
  );
  const [said, setSaid] = useState(() => (handed ? resultText(handed).text : ""));
  const [filter, setFilter] = useState("");
  const refresh = () => {
    const at = epoch;
    loadConnections(client, new AbortController().signal).then(
      (data) => setFresh({ epoch: at, data }),
      () => {
        // Keep what is shown; the next visit loads again.
      },
    );
  };

  const finish = (r: ConnectResult) => {
    setWaiting(null);
    setSaid(resultText(r).text);
    setNotes((n) => ({ ...n, ...noteFor(r) }));
    refresh();
  };

  // The hand-over is read once: a reload doesn't say it again.
  useEffect(() => {
    if (handed) navigate(location.pathname, { replace: true, state: null });
    // eslint-disable-next-line react-hooks/exhaustive-deps -- once
  }, []);

  // A popup's result, from /connections/done. An effect event: it reloads
  // with the credential of now, not the one of the first render (signed in
  // since).
  const onResult = useEffectEvent((e: MessageEvent<unknown>) => {
    const d = e.data as Partial<ConnectResult> | null;
    if (typeof d?.slot === "string" && typeof d.result === "string") finish({ slot: d.slot, result: d.result });
  });
  useEffect(() => {
    if (typeof BroadcastChannel === "undefined") return;
    const ch = new BroadcastChannel(connectChannel);
    ch.onmessage = (e: MessageEvent<unknown>) => onResult(e);
    return () => ch.close();
  }, []);

  // A popup closed before it finished: back to the status as it was, nothing said.
  useEffect(() => {
    const popup = waiting?.popup;
    if (!popup) return;
    const id = setInterval(() => {
      if (!popup.closed) return;
      clearInterval(id);
      // The result may still be on its way from the page that closed it.
      setTimeout(() => setWaiting((w) => (w?.popup === popup ? null : w)), 600);
    }, 400);
    return () => clearInterval(id);
  }, [waiting]);

  const startConnect = (row: Row, slot: Slot) => {
    waiting?.popup?.close(); // one provider window at a time
    // Opened now, in the click, so a popup blocker lets it through; it gets its URL below.
    let popup: Window | null;
    try {
      sessionStorage.setItem(connectPopupKey, "1");
    } catch {
      // Storage blocked: the popup knows itself by its opener or name.
    }
    try {
      popup = window.open("", connectWindow, "popup,width=520,height=680");
    } finally {
      try {
        sessionStorage.removeItem(connectPopupKey);
      } catch {
        // As above.
      }
    }
    setWaiting({ rowId: row.id, popup });
    setNotes((n) => ({ ...n, [row.id]: { text: "", ok: true } }));
    client
      .connectCredential(slot.name, { scope: slot.scope, return_url: `${window.location.origin}/connections/done` })
      .then(
        ({ authorize_url }) => {
          if (popup && !popup.closed) {
            popup.location.href = authorize_url;
            return;
          }
          // Blocked (or a phone): this tab goes to the provider and comes back.
          try {
            sessionStorage.setItem(connectReturnKey, location.pathname);
          } catch {
            // Storage blocked: /connections/done goes to /connections.
          }
          window.location.assign(authorize_url);
        },
        (err: unknown) => {
          popup?.close();
          setWaiting(null);
          setNotes((n) => ({ ...n, [row.id]: { text: errorText(err, "Couldn't start connecting"), ok: false } }));
        },
      );
  };

  const act = (row: Row, a: Action) => {
    const slot = row.slot;
    if (!slot) return; // a server key: no actions
    if (a === "disconnect") setDialog({ row, slot, kind: "disconnect" });
    else if (slot.oauth) startConnect(row, slot);
    else setDialog({ row, slot, kind: a === "replace" ? "replace" : "key" });
  };

  const changed = (text: string) => {
    setDialog(null);
    setSaid(text);
    refresh();
  };

  const data = fresh?.epoch === epoch ? fresh.data : res.state === "ready" ? res.data : null;
  const rows = data ? connectionRows(data.credentials, data.toolsets, data.agents, canSetTenantCredentials(me)) : [];

  const shown = rows.filter((r) => matches(r, filter));
  return (
    <Page title="Connectors">
      <p className="text-ak-text-2">
        Connect your own accounts and keys for the services your agents use. Yours are used only for your runs, and
        nobody else sees them, admins included. In a chat, the Tools menu picks which of them the agent may use.
      </p>
      <div role="status" className="sr-only">
        {said}
      </div>
      {data && data.credentials === null ? (
        <p className="text-(length:--ak-text-sm) text-ak-muted">
          This server keeps no credentials, so keys and accounts can&apos;t be connected here: an admin sets
          AGENTKIT_CREDENTIALS_KEY and restarts. Server keys still work.
        </p>
      ) : null}
      {data ? (
        rows.length === 0 ? (
          <Empty icon={KeyRound} title="Nothing to connect">
            <p className="text-ak-muted">None of this server&apos;s connectors needs a key or an account.</p>
          </Empty>
        ) : (
          <>
            {rows.length > 8 ? (
              <Input
                type="search"
                aria-label="Filter connectors"
                placeholder="Filter connectors"
                value={filter}
                onChange={(e) => setFilter(e.target.value)}
                className="max-w-80"
              />
            ) : null}
            {shown.length === 0 ? <p className="text-ak-muted">No connector matches “{filter}”.</p> : null}
            {groups
              .filter((g) => shown.some((r) => r.group === g))
              .map((g) => (
                <section key={g} aria-labelledby={`group-${g}`} className="flex flex-col gap-2">
                  <h2 id={`group-${g}`} className="text-(length:--ak-text-sm) font-medium text-ak-muted">
                    {g}
                  </h2>
                  <ul className="grid gap-3 md:grid-cols-2">
                    {shown
                      .filter((r) => r.group === g)
                      .map((row) => (
                        <ConnectorCard
                          key={row.id}
                          row={row}
                          waiting={waiting?.rowId === row.id}
                          note={notes[row.id]}
                          onAct={(a) => act(row, a)}
                          onCancel={() => {
                            waiting?.popup?.close();
                            setWaiting(null);
                          }}
                        />
                      ))}
                  </ul>
                </section>
              ))}
          </>
        )
      ) : (
        <Loaded res={res} what="connectors">
          {() => null}
        </Loaded>
      )}
      {dialog?.kind === "disconnect" ? (
        <DisconnectDialog
          row={dialog.row}
          slot={dialog.slot}
          onClose={() => setDialog(null)}
          onDone={() => changed(`${dialog.row.title} disconnected.`)}
        />
      ) : dialog ? (
        <KeyDialog
          row={dialog.row}
          slot={dialog.slot}
          replacing={dialog.kind === "replace"}
          onClose={() => setDialog(null)}
          onSaved={() => changed(`${dialog.row.title} connected.`)}
        />
      ) : null}
    </Page>
  );
}

/**
 * /connections/done, where a connect flow comes back from the provider
 * with result and slot in its query. In the popup it tells the page that
 * opened it (a same-origin BroadcastChannel) and closes; in the same tab it
 * goes back to where the connect started, with the result.
 */
export function ConnectDonePage() {
  const navigate = useNavigate();
  const { search } = useLocation();
  const q = new URLSearchParams(search);
  const r: ConnectResult = { slot: q.get("slot") ?? "", result: q.get("result") ?? "" };
  const [popup] = useState(inPopup);
  useEffect(() => {
    if (popup) {
      if (typeof BroadcastChannel !== "undefined" && r.slot) {
        const ch = new BroadcastChannel(connectChannel);
        ch.postMessage(r);
        ch.close();
      }
      const id = setTimeout(() => window.close(), 50);
      return () => clearTimeout(id);
    }
    let back = "/connections";
    try {
      back = sessionStorage.getItem(connectReturnKey) ?? back;
      sessionStorage.removeItem(connectReturnKey);
    } catch {
      // Storage blocked: back to Connections.
    }
    if (!isLocalPath(back)) back = "/connections";
    navigate(back, { replace: true, state: r.slot ? { connect: r } : null });
    // eslint-disable-next-line react-hooks/exhaustive-deps -- once
  }, []);
  const t = resultText(r);
  return (
    <main className="flex h-svh items-center justify-center bg-ak-bg p-4 text-ak-text">
      <p role="status">{popup && r.slot ? `${t.text} You can close this window.` : "Going back…"}</p>
    </main>
  );
}
