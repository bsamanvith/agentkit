// Add an MCP server, /toolsets/new (docs/UI.md "Adding an MCP server", the
// mockup's mcpAddView): four steps, "1 Server · 2 Sign-in · 3 Review tools ·
// 4 Use it".
//
// 1. A URL (pasted, or picked from a registry search) and a name. Connect
//    probes it: POST /v1/toolsets connects, lists its tools and stores the
//    toolset with every tool off. Nothing is stored if the probe fails.
// 2. Sign-in, when the server refused agentkit (422 auth_required) or the
//    registry says it takes headers: a header whose value is a ${VAR} of
//    the server's environment, never a secret; or, when the 422 says
//    auth "oauth", each person signs in with MCP OAuth (scope user, T095).
// 3. Review: every tool needs Approve or Reject (POST …/review). An OAuth
//    server lists its tools through a connection: the admin connects their
//    own account first (POST /v1/credentials/{name}/connect, in a popup).
// 4. Use it: stored agents get a new version with the toolset in their
//    runtime_toolsets, a draft unless the admin publishes it at once.

import {
  ApiError,
  type AgentVersion,
  type RegistryHeader,
  type RegistrySearch,
  type RegistryServer,
  type ServerToolset,
} from "@agentkit/agent-ui/api";
import { Button } from "@agentkit/ui/components/button";
import { Input } from "@agentkit/ui/components/input";
import { cn } from "@agentkit/ui/lib/utils";
import { Check, KeyRound, Search, TriangleAlert } from "lucide-react";
import { useEffect, useEffectEvent, useId, useRef, useState, type FormEvent, type ReactNode } from "react";
import { Link, useNavigate } from "react-router";
import { useApi, useResource } from "../api";
import { listAgents, useLists } from "../lists";
import { Loaded, Page } from "./page";
import { useForbidden } from "./forbidden";
import { Callout, Forbidden } from "./parts";
import {
  addError,
  decisions,
  fillsTemplate,
  headerProblem,
  initialChoices,
  reviewErrorText,
  undecided,
  type Choices,
  nameFromURL,
  nameProblem,
  needsDecision,
  urlProblem,
  urlWarning,
  type FormError,
} from "./mcp-servers";
import { ErrorLine, ReviewCards } from "./mcp-review";
import { connectChannel, connectPopupKey, connectWindow, resultText } from "./connections-model";

const stepNames = ["Server", "Sign-in", "Review tools", "Use it"];

type Header = { name: string; value: string };

type Wizard = {
  step: 1 | 2 | 3 | 4 | 5;
  url: string;
  name: string;
  /** The admin typed a name: the URL no longer sets it. */
  named: boolean;
  transport: string;
  /** The registry remote's URL picked, with its {variables}; "" for a pasted one. */
  template: string;
  headers: Header[];
  /** The server's 401, as the API put it; null when it didn't ask. */
  asked: string | null;
  /** The 401 named MCP OAuth (the 422's auth "oauth"). */
  oauth: boolean;
  /** The registry's word on headers the server takes. */
  hinted: RegistryHeader[];
  ts: ServerToolset | null;
};

const fresh = (): Wizard => ({
  step: 1,
  url: "",
  name: "",
  named: false,
  transport: "auto",
  template: "",
  headers: [],
  asked: null,
  oauth: false,
  hinted: [],
  ts: null,
});

/** A field with its label, help and error, linked for screen readers (docs/UI.md "Forms"). */
function Field({
  id,
  label,
  help,
  error,
  warn,
  children,
}: {
  id: string;
  label: string;
  help?: ReactNode;
  error?: string;
  warn?: string;
  children: (describedBy: string, invalid: boolean) => ReactNode;
}) {
  const ids = [error ? `${id}-err` : "", warn ? `${id}-warn` : "", help ? `${id}-help` : ""].filter(Boolean).join(" ");
  return (
    <div className="flex flex-col gap-1.5">
      <label htmlFor={id} className="text-(length:--ak-text-sm) font-medium text-ak-text">
        {label}
      </label>
      {children(ids, !!error)}
      {error ? (
        <p id={`${id}-err`} className="flex items-start gap-1.5 text-(length:--ak-text-sm) text-ak-bad">
          <TriangleAlert aria-hidden="true" className="mt-0.5 size-4 flex-none" />
          <span className="min-w-0 break-words">{error}</span>
        </p>
      ) : null}
      {warn && !error ? (
        <p id={`${id}-warn`} className="text-xs text-ak-warn">
          {warn}
        </p>
      ) : null}
      {help ? (
        <p id={`${id}-help`} className="text-xs text-ak-muted">
          {help}
        </p>
      ) : null}
    </div>
  );
}

function Steps({ step }: { step: number }) {
  return (
    <ol aria-label="Steps" className="flex flex-wrap gap-x-4 gap-y-1 text-(length:--ak-text-sm)">
      {stepNames.map((label, k) => {
        const n = k + 1;
        const done = n < step;
        return (
          <li
            key={label}
            aria-current={n === step ? "step" : undefined}
            className={cn("flex items-center gap-1.5", n === step ? "font-medium text-ak-text" : "text-ak-muted")}
          >
            <span
              aria-hidden="true"
              className={cn(
                "grid size-5 place-items-center rounded-full border text-xs",
                n === step && "border-ak-text bg-ak-text text-ak-bg",
              )}
            >
              {done ? <Check className="size-3" /> : n}
            </span>
            <span>
              {label}
              {done ? <span className="sr-only">, done</span> : null}
            </span>
          </li>
        );
      })}
    </ol>
  );
}

/**
 * Find a server in the MCP Registry (T097): picking a remote fills in the
 * URL, the name and the headers it takes. Hidden when the server searches
 * no registry (404); when the registry is down, it says so and the URL
 * field still works. An entry is never shown as verified.
 */
function RegistryBox({ onPick }: { onPick: (s: RegistryServer, remote: number) => void }) {
  const { client } = useApi();
  const [q, setQ] = useState("");
  const [state, setState] = useState<
    | { kind: "idle" }
    | { kind: "loading" }
    | { kind: "found"; res: RegistrySearch }
    | { kind: "down" }
    | { kind: "failed"; message: string }
    | { kind: "none" }
  >({ kind: "idle" });
  const id = useId();
  useEffect(() => {
    const query = q.trim();
    if (query.length < 2) return;
    const ctl = new AbortController();
    const timer = setTimeout(() => {
      setState({ kind: "loading" });
      client.searchMCPRegistry(query, { signal: ctl.signal, limit: 10 }).then(
        (res) => setState({ kind: "found", res }),
        (e: unknown) => {
          if (ctl.signal.aborted) return;
          if (e instanceof ApiError && e.status === 404) setState({ kind: "none" });
          else if (e instanceof ApiError && e.status === 502) setState({ kind: "down" });
          else setState({ kind: "failed", message: e instanceof Error ? e.message : String(e) });
        },
      );
    }, 300);
    return () => {
      clearTimeout(timer);
      ctl.abort();
    };
  }, [q, client]);
  if (state.kind === "none") return null;
  const shown = q.trim().length >= 2 ? state : { kind: "idle" as const };
  return (
    <div role="search" aria-label="MCP Registry" className="flex flex-col gap-2">
      <label htmlFor={`${id}-q`} className="text-(length:--ak-text-sm) font-medium text-ak-text">
        Find a server
      </label>
      <div className="relative">
        <Search aria-hidden="true" className="absolute top-2.5 left-2.5 size-4 text-ak-muted" />
        <Input
          id={`${id}-q`}
          type="search"
          value={q}
          onChange={(e) => setQ(e.target.value)}
          placeholder="Search the MCP Registry, or paste a URL below"
          autoComplete="off"
          spellCheck={false}
          className="pl-8"
        />
      </div>
      <div aria-live="polite" className="flex flex-col gap-2">
        {shown.kind === "loading" ? <p className="text-xs text-ak-muted">Searching…</p> : null}
        {shown.kind === "down" ? (
          <p role="alert" className="text-(length:--ak-text-sm) text-ak-warn">
            The registry can&apos;t be reached; paste the URL instead.
          </p>
        ) : null}
        {shown.kind === "failed" ? (
          <p role="alert" className="text-(length:--ak-text-sm) text-ak-warn">
            The search failed ({shown.message}); paste the URL instead.
          </p>
        ) : null}
        {shown.kind === "found" ? (
          shown.res.servers.length === 0 ? (
            <p className="text-xs text-ak-muted">No server with a URL to add matches “{shown.res.query}”.</p>
          ) : (
            <>
              <ul aria-label="Servers found" className="flex flex-col divide-y rounded-md border">
                {shown.res.servers.map((s) => (
                  <li key={s.name} className="flex flex-col gap-1.5 px-3 py-2.5">
                    <span className="flex flex-wrap items-baseline gap-x-2">
                      <span className="font-medium break-words text-ak-text">{s.title || s.name}</span>
                      <span className="font-mono text-xs break-all text-ak-muted">{s.name}</span>
                      {s.status === "deprecated" ? <span className="text-xs text-ak-warn">deprecated</span> : null}
                    </span>
                    {s.description ? (
                      <span className="line-clamp-2 text-(length:--ak-text-sm) break-words text-ak-muted">
                        {s.description}
                      </span>
                    ) : null}
                    {s.remotes.map((r, i) => (
                      <span key={`${r.url}\n${i}`} className="flex flex-wrap items-center gap-2">
                        <Button
                          type="button"
                          variant="outline"
                          size="xs"
                          disabled={!r.allowed}
                          aria-label={`Use ${s.name} at ${r.url}`}
                          onClick={() => onPick(s, i)}
                        >
                          Use
                        </Button>
                        <code className="min-w-0 font-mono text-xs break-all text-ak-text-2">{r.url}</code>
                        <span className="text-xs text-ak-muted">
                          {r.transport === "sse" ? "SSE" : "Streamable HTTP"}
                        </span>
                        {!r.allowed ? (
                          <span className="text-xs text-ak-warn">not allowed in this workspace</span>
                        ) : null}
                      </span>
                    ))}
                  </li>
                ))}
              </ul>
              <p className="text-xs text-ak-muted">
                A registry listing isn&apos;t a check: agentkit probes the server, and you review every tool.
                {shown.res.restricted ? " This workspace limits the URLs admins may add." : ""}
              </p>
            </>
          )
        ) : null}
      </div>
    </div>
  );
}

/** Step 1: where the server is, and its name. */
function ServerStep({
  w,
  set,
  taken,
  onAdded,
}: {
  w: Wizard;
  set: (p: Partial<Wizard>) => void;
  taken: string[];
  onAdded: (ts: ServerToolset) => void;
}) {
  const { client } = useApi();
  const [errors, setErrors] = useState<{ url?: string; name?: string; form?: string }>({});
  const [busy, setBusy] = useState(false);
  const urlRef = useRef<HTMLInputElement>(null);
  const nameRef = useRef<HTMLInputElement>(null);
  const connect = (e: FormEvent) => {
    e.preventDefault();
    const name = w.name || nameFromURL(w.url);
    const url = urlProblem(w.url);
    const nm = url ? "" : nameProblem(name, taken);
    setErrors({ url, name: nm });
    if (url) return urlRef.current?.focus();
    if (nm) {
      set({ name });
      return nameRef.current?.focus();
    }
    set({ name });
    // The registry says it takes headers: ask for them before probing.
    if (w.hinted.some((h) => h.required)) return set({ name, step: 2 });
    setBusy(true);
    client.addToolset({ type: "mcp", name, url: w.url.trim(), transport: w.transport }).then(
      (ts) => onAdded(ts),
      (err: unknown) => {
        setBusy(false);
        const f = addError(err);
        if (f.authRequired) return set({ name, step: 2, asked: f.message, oauth: !!f.oauth });
        setErrors(
          f.field === "name" ? { name: f.message } : f.field === "url" ? { url: f.message } : { form: f.message },
        );
        (f.field === "name" ? nameRef : urlRef).current?.focus();
      },
    );
  };
  return (
    <>
      <RegistryBox
        onPick={(s, i) => {
          const r = s.remotes[i];
          if (!r) return;
          const hinted = r.headers ?? [];
          set({
            url: r.url,
            template: r.url,
            transport: r.transport === "sse" ? "sse" : "http",
            name: s.suggested_name && !taken.includes(s.suggested_name) ? s.suggested_name : w.name,
            named: false,
            hinted,
            headers: hinted.map((h) => ({ name: h.name, value: h.secret ? `\${${envName(h.name)}}` : "" })),
          });
          setErrors({});
          urlRef.current?.focus();
        }}
      />
      <form noValidate onSubmit={connect} aria-busy={busy} className="flex flex-col gap-4">
        <Field
          id="mcp-url"
          label="Server URL"
          error={errors.url}
          warn={errors.url ? undefined : urlWarning(w.url)}
          help="Streamable HTTP or SSE. agentkit connects from the server, over https, to public addresses only, unless the operator allows the host."
        >
          {(describedBy, invalid) => (
            <Input
              ref={urlRef}
              id="mcp-url"
              type="url"
              value={w.url}
              onChange={(e) => {
                const url = e.target.value;
                if (errors.url) setErrors({ ...errors, url: undefined }); // a new URL, a new answer
                // Filling in a registry remote's {variables} keeps what the registry said of it.
                const filling = w.template !== "" && fillsTemplate(w.template, url);
                set({
                  url,
                  ...(filling ? {} : { template: "", transport: "auto", hinted: [], headers: [] }),
                  ...(w.named || filling ? {} : { name: nameFromURL(url) }),
                });
              }}
              placeholder="https://mcp.example.com/mcp"
              autoComplete="off"
              spellCheck={false}
              aria-invalid={invalid || undefined}
              aria-describedby={describedBy}
              readOnly={busy}
              className="font-mono"
              data-autofocus=""
            />
          )}
        </Field>
        <Field
          id="mcp-name"
          label="Name"
          error={errors.name}
          help={`Its tools are named ${w.name || "name"}__tool. Letters, digits, - and _.`}
        >
          {(describedBy, invalid) => (
            <Input
              ref={nameRef}
              id="mcp-name"
              value={w.name}
              onChange={(e) => set({ name: e.target.value, named: e.target.value !== "" })}
              placeholder="from the URL"
              autoComplete="off"
              spellCheck={false}
              aria-invalid={invalid || undefined}
              aria-describedby={describedBy}
              readOnly={busy}
              className="font-mono"
            />
          )}
        </Field>
        {errors.form ? <ErrorLine>{errors.form}</ErrorLine> : null}
        <div className="flex flex-wrap gap-2">
          <Button type="submit" disabled={busy}>
            {busy ? "Connecting…" : "Connect"}
          </Button>
          <Button asChild variant="outline">
            <Link to="/toolsets">Cancel</Link>
          </Button>
        </div>
        <p className="text-xs text-ak-muted">
          Connect lists the server&apos;s tools and saves it with every tool off. Nothing is saved if it can&apos;t be
          reached.
        </p>
      </form>
    </>
  );
}

/** A header name as a variable name: Authorization is AUTHORIZATION, x-api-key X_API_KEY. */
function envName(header: string): string {
  return (
    header
      .toUpperCase()
      .replace(/[^A-Z0-9]+/g, "_")
      .replace(/^_+|_+$/g, "") || "API_KEY"
  );
}

/** Step 2: how agentkit signs in to the server. */
function SignInStep({
  w,
  set,
  onAdded,
}: {
  w: Wizard;
  set: (p: Partial<Wizard>) => void;
  onAdded: (ts: ServerToolset) => void;
}) {
  const { client } = useApi();
  const [mode, setMode] = useState<"none" | "headers" | "user">(
    w.oauth ? "user" : w.asked || w.hinted.some((h) => h.required) ? "headers" : "none",
  );
  const [rowErrors, setRowErrors] = useState<string[]>([]);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const headers = w.headers.length > 0 ? w.headers : [{ name: "Authorization", value: "Bearer ${API_KEY}" }];
  const setHeaders = (h: Header[]) => set({ headers: h });
  const oauth = w.asked !== null && w.oauth;
  const submit = (e: FormEvent) => {
    e.preventDefault();
    setError("");
    let sent: Record<string, string> | undefined;
    if (mode === "headers") {
      const problems = headers.map((h) => headerProblem(h.name, h.value));
      setRowErrors(problems);
      if (problems.some(Boolean)) {
        document.getElementById(`mcp-hdr-${problems.findIndex(Boolean)}-value`)?.focus();
        return;
      }
      sent = Object.fromEntries(headers.map((h) => [h.name.trim(), h.value.trim()]));
    }
    setBusy(true);
    client
      .addToolset({
        type: "mcp",
        name: w.name,
        url: w.url.trim(),
        transport: w.transport,
        auth: mode === "user" ? "oauth" : sent ? "headers" : "none",
        ...(sent ? { headers: sent } : {}),
        ...(mode === "user" ? { scope: "user" } : {}),
      })
      .then(
        (ts) => onAdded(ts),
        (err: unknown) => {
          setBusy(false);
          const f: FormError = addError(err);
          setError(f.authRequired ? `The server still refused agentkit. ${f.message}` : f.message);
        },
      );
  };
  return (
    <form noValidate onSubmit={submit} aria-busy={busy} className="flex flex-col gap-4">
      {w.asked ? (
        <Callout icon={KeyRound} title="The server asked for sign-in (401).">
          {w.asked} Nothing is saved yet.
        </Callout>
      ) : (
        <Callout icon={KeyRound} tone="info" title="The registry says this server takes headers.">
          {w.hinted.map((h) => `${h.name}${h.required ? " (required)" : ""}${h.secret ? ", a secret" : ""}`).join("; ")}
          . Nothing is saved yet.
        </Callout>
      )}
      {oauth ? (
        <Callout tone="info" title="It signs people in with MCP OAuth.">
          With Each person signs in, everyone connects their own account on Connections, and the agents use it only for
          their runs. You connect yours next, to list its tools.
        </Callout>
      ) : null}
      <fieldset className="flex flex-col gap-2">
        <legend className="mb-1.5 text-(length:--ak-text-sm) font-medium text-ak-text">Sign-in</legend>
        {(
          [
            [
              "none",
              "No sign-in",
              w.asked ? "The server refused a request without one." : "Anyone who can reach it can use it.",
              !!w.asked,
            ],
            [
              "headers",
              "A header, from a server variable",
              "The value comes from an environment variable of the agentkit server that the operator allows. The console never sees it.",
              false,
            ],
            [
              "user",
              "Each person signs in",
              oauth
                ? "MCP OAuth: each person connects their own account on Connections. Someone who hasn't gets “not connected” from its tools, never someone else's account."
                : "For a server that signs people in with MCP OAuth; this one didn't ask for it.",
              !oauth,
            ],
          ] as const
        ).map(([value, label, desc, disabled]) => (
          <label
            key={value}
            className={cn(
              "flex items-start gap-2.5 rounded-md border px-3 py-2.5 text-(length:--ak-text-sm)",
              disabled ? "opacity-60" : "cursor-pointer hover:bg-ak-surface-hover",
            )}
          >
            <input
              type="radio"
              name="mcp-auth"
              value={value}
              checked={mode === value}
              disabled={disabled}
              onChange={() => setMode(value)}
              className="mt-1"
            />
            <span className="flex flex-col gap-0.5">
              <span className="font-medium text-ak-text">{label}</span>
              <span className="text-ak-muted">{desc}</span>
            </span>
          </label>
        ))}
      </fieldset>
      {mode === "headers" ? (
        <div className="flex flex-col gap-3">
          {headers.map((h, i) => (
            <div key={i} className="grid gap-2 md:grid-cols-[minmax(0,12rem)_minmax(0,1fr)_auto] md:items-start">
              <Field id={`mcp-hdr-${i}-name`} label="Header">
                {(describedBy) => (
                  <Input
                    id={`mcp-hdr-${i}-name`}
                    value={h.name}
                    onChange={(e) => setHeaders(headers.map((x, k) => (k === i ? { ...x, name: e.target.value } : x)))}
                    autoComplete="off"
                    spellCheck={false}
                    aria-describedby={describedBy || undefined}
                    className="font-mono"
                  />
                )}
              </Field>
              <Field
                id={`mcp-hdr-${i}-value`}
                label="Value"
                error={rowErrors[i] || undefined}
                help={
                  i === headers.length - 1
                    ? "${NAME} is a variable the operator allows in runtime_toolsets.header_env."
                    : undefined
                }
              >
                {(describedBy, invalid) => (
                  <Input
                    id={`mcp-hdr-${i}-value`}
                    value={h.value}
                    onChange={(e) => setHeaders(headers.map((x, k) => (k === i ? { ...x, value: e.target.value } : x)))}
                    autoComplete="off"
                    spellCheck={false}
                    aria-invalid={invalid || undefined}
                    aria-describedby={describedBy || undefined}
                    className="font-mono"
                  />
                )}
              </Field>
              {headers.length > 1 ? (
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  className="md:mt-7"
                  onClick={() => setHeaders(headers.filter((_, k) => k !== i))}
                >
                  Remove
                </Button>
              ) : null}
            </div>
          ))}
          <Button
            type="button"
            variant="link"
            size="sm"
            className="h-auto self-start px-0"
            onClick={() => setHeaders([...headers, { name: "", value: "" }])}
          >
            Add a header
          </Button>
        </div>
      ) : null}
      {error ? <ErrorLine>{error}</ErrorLine> : null}
      <div className="flex flex-wrap gap-2">
        <Button type="submit" disabled={busy}>
          {busy
            ? mode === "user"
              ? "Saving…"
              : "Listing tools…"
            : mode === "user"
              ? "Save"
              : "Connect and list tools"}
        </Button>
        <Button type="button" variant="outline" onClick={() => set({ step: 1, asked: null, oauth: false })}>
          Back
        </Button>
      </div>
    </form>
  );
}

/** Step 3: every tool needs a decision. */
function ReviewStep({
  ts,
  onReviewed,
  onFresh,
}: {
  ts: ServerToolset;
  onReviewed: (ts: ServerToolset) => void;
  onFresh: (ts: ServerToolset) => void;
}) {
  const { client } = useApi();
  const { reloadToolsets } = useLists();
  const navigate = useNavigate();
  const [choices, setChoices] = useState<Choices>(() => initialChoices(ts.tools));
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [current, setCurrent] = useState(ts);
  /** A conflict's fresh review couldn't be read: what is shown is not what the server lists. */
  const [stale, setStale] = useState(false);
  const asked = current.tools.filter(needsDecision);
  const left = undecided(current.tools, choices).length;
  const unconnected = current.auth === "oauth" && current.connection !== "connected";
  const reload = () =>
    client.getToolsetReview(current.name).then(
      (fresh) => {
        setError("");
        setCurrent(fresh);
        setChoices(initialChoices(fresh.tools));
        onFresh(fresh);
      },
      (e: unknown) => setError(e instanceof ApiError ? `${e.message} (${e.code})` : "Couldn't reach agentkit."),
    );
  const save = () => {
    setBusy(true);
    setError("");
    client.reviewToolset(current.name, decisions(current.tools, choices)).then(
      (done) => {
        reloadToolsets();
        onReviewed(done);
      },
      (e: unknown) => {
        setError(reviewErrorText(e));
        if (!(e instanceof ApiError && e.status === 409)) return setBusy(false);
        // Stay busy until the fresh review replaces the one shown: until then
        // only decisions on what the server no longer lists could be sent.
        client.getToolsetReview(current.name).then(
          (fresh) => {
            setCurrent(fresh);
            setChoices(initialChoices(fresh.tools));
            setBusy(false);
          },
          () => {
            setStale(true);
            setBusy(false);
            setError(
              "The server changed a tool while you were reviewing it, and the new list couldn't be read. Open the toolset and review it there.",
            );
          },
        );
      },
    );
  };
  const remove = () => {
    setBusy(true);
    client.deleteToolset(current.name).then(
      () => {
        reloadToolsets();
        void navigate("/toolsets");
      },
      (e: unknown) => {
        setBusy(false);
        setError(e instanceof ApiError ? `It wasn't removed: ${e.message} (${e.code}).` : "It wasn't removed.");
      },
    );
  };
  if (unconnected) {
    return (
      <>
        <ConnectBox name={current.name} status={current.connection} onConnected={() => void reload()} />
        {error ? <ErrorLine>{error}</ErrorLine> : null}
        <div className="flex flex-wrap items-center gap-2">
          <Button type="button" variant="outline" disabled={busy} onClick={remove}>
            Cancel and remove it
          </Button>
        </div>
        <p className="text-xs text-ak-muted">
          {current.name} is saved with every tool off. Leaving this page keeps it so: its page offers Review once you
          have connected.
        </p>
      </>
    );
  }
  return (
    <>
      <p className="text-(length:--ak-text-sm) text-ak-text-2">
        {current.server.name ? (
          <>
            It says it is <span className="font-mono">{current.server.name}</span>
            {current.server.version ? ` ${current.server.version}` : ""}
            {current.auth === "none" ? ", and it answered without sign-in. " : ". "}
          </>
        ) : null}
        Nothing runs until you approve it. Approved tools are pinned as they are now: if the server changes one later,
        that tool turns off until someone reviews it.
      </p>
      {asked.length === 0 ? <p className="text-(length:--ak-text-sm) text-ak-muted">It lists no tools.</p> : null}
      <ReviewCards ts={current} choices={choices} setChoices={setChoices} only={asked} />
      {error ? <ErrorLine>{error}</ErrorLine> : null}
      <div className="flex flex-wrap items-center gap-2">
        <Button type="button" disabled={busy || stale || left > 0 || asked.length === 0} onClick={save}>
          {busy ? "Saving…" : "Save and continue"}
        </Button>
        <Button type="button" variant="outline" disabled={busy} onClick={remove}>
          Cancel and remove it
        </Button>
        {left > 0 ? <span className="text-xs text-ak-muted">{left} still to decide</span> : null}
      </div>
      <p className="text-xs text-ak-muted">
        {current.name} is saved with every tool off. Leaving this page keeps it so: its page offers Review.
      </p>
    </>
  );
}

/**
 * Connects the admin's own account to an MCP OAuth server, in a popup
 * (POST /v1/credentials/{name}/connect, back at /connections/done, which
 * reports on the connect channel), so its tools can be listed. "Check
 * again" reads the review once more, for a popup that couldn't report.
 */
function ConnectBox({
  name,
  status,
  onConnected,
}: {
  name: string;
  status: string | undefined;
  onConnected: () => void;
}) {
  const { client } = useApi();
  const [note, setNote] = useState("");
  const [busy, setBusy] = useState(false);
  // A popup's result, from /connections/done.
  const onResult = useEffectEvent((e: MessageEvent<unknown>) => {
    const d = e.data as { slot?: unknown; result?: unknown } | null;
    if (d?.slot !== name || typeof d.result !== "string") return;
    setBusy(false);
    if (d.result === "connected") {
      setNote("");
      onConnected();
    } else setNote(resultText({ slot: name, result: d.result }).text);
  });
  useEffect(() => {
    if (typeof BroadcastChannel === "undefined") return;
    const ch = new BroadcastChannel(connectChannel);
    ch.onmessage = (e: MessageEvent<unknown>) => onResult(e);
    return () => ch.close();
  }, []);
  const connect = () => {
    setNote("");
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
    setBusy(true);
    client.connectCredential(name, { scope: "user", return_url: `${window.location.origin}/connections/done` }).then(
      ({ authorize_url }) => {
        if (popup && !popup.closed) {
          popup.location.href = authorize_url;
          return;
        }
        setBusy(false);
        setNote("The sign-in window was blocked. Allow popups, or connect on Connections, then Check again.");
      },
      (e: unknown) => {
        popup?.close();
        setBusy(false);
        setNote(
          e instanceof ApiError ? `Couldn't start connecting: ${e.message} (${e.code}).` : "Couldn't reach agentkit.",
        );
      },
    );
  };
  return (
    <>
      <Callout icon={KeyRound} tone="info" title="Connect your account to list its tools.">
        {status === "needs_reconnect"
          ? "Your connection to it was refused; connect again. "
          : "agentkit reads the tools through a connection, and it has none of yours yet. "}
        You sign in at the server&apos;s own sign-in page; agentkit keeps the token, and the console never sees it.
      </Callout>
      <div className="flex flex-wrap items-center gap-2">
        <Button type="button" disabled={busy} onClick={connect}>
          {busy ? "Waiting for sign-in…" : `Connect ${name}`}
        </Button>
        <Button type="button" variant="outline" onClick={onConnected}>
          Check again
        </Button>
      </div>
      <div role="status" aria-live="polite" className="text-(length:--ak-text-sm) text-ak-warn">
        {note}
      </div>
    </>
  );
}

type Result =
  { agent: string; ok: true; version: AgentVersion; heldBack?: number } | { agent: string; ok: false; error: string };

/**
 * Adds the toolset to a stored agent's runtime_toolsets, as a new version on
 * top of its newest one, saved only if no one saved another meanwhile
 * (if_latest: the server's latest number, which a deleted draft keeps). It
 * publishes only on top of the published version: on top of someone's
 * unpublished draft it saves a draft (heldBack), so publishing here never
 * publishes their changes unseen.
 */
async function addTo(
  client: ReturnType<typeof useApi>["client"],
  agent: string,
  toolset: string,
  publish: boolean,
): Promise<Result> {
  try {
    const list = await client.listAgentVersions(agent);
    const newest = list.versions.reduce<AgentVersion | undefined>(
      (a, v) => (!a || v.version > a.version ? v : a),
      undefined,
    );
    if (!newest) return { agent, ok: false, error: "it has no version to start from" };
    const def = { ...newest.definition };
    const rt = Array.isArray(def.runtime_toolsets) ? (def.runtime_toolsets as unknown[]).map(String) : [];
    def.runtime_toolsets = rt.includes(toolset) ? rt : [...rt, toolset];
    const onPublished = newest.published || newest.version === list.published;
    const version = await client.saveAgentVersion(agent, {
      definition: def,
      message: `Add the toolset ${toolset}`,
      if_latest: list.latest,
      publish: publish && onPublished,
    });
    return publish && !onPublished
      ? { agent, ok: true, version, heldBack: newest.version }
      : { agent, ok: true, version };
  } catch (e) {
    return {
      agent,
      ok: false,
      error:
        e instanceof ApiError && e.status === 409
          ? "someone saved a version meanwhile; add it in the agent's own version"
          : e instanceof ApiError
            ? `${e.message} (${e.code})`
            : "couldn't reach agentkit",
    };
  }
}

/** Step 4: which stored agents use it. */
function UseStep({ ts, onDone }: { ts: ServerToolset; onDone: (r: Result[]) => void }) {
  const { client } = useApi();
  const agents = useResource(listAgents, "agents");
  const [picked, setPicked] = useState<string[]>([]);
  const [publish, setPublish] = useState(false);
  const [busy, setBusy] = useState(false);
  const ok = ts.tools.filter((t) => t.status === "approved");
  const finish = () => {
    setBusy(true);
    void Promise.all(picked.map((a) => addTo(client, a, ts.name, publish))).then(onDone);
  };
  return (
    <>
      <p className="text-(length:--ak-text-sm) text-ak-text-2">
        {ok.length === 0
          ? "No tool is approved, so agents get nothing from it yet."
          : `${ok.length} approved ${ok.length === 1 ? "tool" : "tools"}: ${ok.map((t) => t.name).join(", ")}.`}{" "}
        Each agent you pick gets a new version with {ts.name} in its runtime toolsets.
      </p>
      <Loaded res={agents} what="the agents">
        {(list) => {
          const stored = list.filter((a) => a.version !== undefined && !a.read_only);
          return (
            <fieldset className="flex flex-col gap-1.5">
              <legend className="mb-1.5 text-(length:--ak-text-sm) font-medium text-ak-text">Agents</legend>
              {list.map((a) => {
                const config = a.version === undefined || a.read_only === true;
                return (
                  <label
                    key={a.name}
                    className={cn("flex items-center gap-2 text-(length:--ak-text-sm)", config && "text-ak-muted")}
                  >
                    <input
                      type="checkbox"
                      disabled={config || busy}
                      checked={picked.includes(a.name)}
                      onChange={(e) =>
                        setPicked(e.target.checked ? [...picked, a.name] : picked.filter((x) => x !== a.name))
                      }
                    />
                    <span className="font-mono">{a.name}</span>
                    {config ? <span className="text-xs">(config: name it in runtime_toolsets there)</span> : null}
                  </label>
                );
              })}
              {stored.length === 0 ? (
                <p className="text-xs text-ak-muted">
                  No stored agent to add it to. An agent of the server config uses it once its runtime_toolsets names{" "}
                  {ts.name}.
                </p>
              ) : null}
            </fieldset>
          );
        }}
      </Loaded>
      <label className="flex items-start gap-2 text-(length:--ak-text-sm)">
        <input
          type="checkbox"
          className="mt-1"
          checked={publish}
          disabled={busy}
          onChange={(e) => setPublish(e.target.checked)}
        />
        <span className="flex flex-col">
          <span className="text-ak-text">Publish the new versions now</span>
          <span className="text-xs text-ak-muted">
            Off, each agent gets a draft, which changes nothing until it is published.
          </span>
        </span>
      </label>
      <div className="flex flex-wrap gap-2">
        <Button type="button" disabled={busy} onClick={finish}>
          {busy
            ? "Saving…"
            : picked.length
              ? `Add to ${picked.length} ${picked.length === 1 ? "agent" : "agents"}`
              : "Finish"}
        </Button>
      </div>
    </>
  );
}

function DoneStep({ ts, results }: { ts: ServerToolset; results: Result[] }) {
  const ok = ts.tools.filter((t) => t.status === "approved").length;
  return (
    <>
      <p role="status" className="text-(length:--ak-text-sm) text-ak-text">
        {ts.name} is added, with {ok} approved {ok === 1 ? "tool" : "tools"}.
      </p>
      {results.length > 0 ? (
        <ul aria-label="Agents" className="flex flex-col gap-1 text-(length:--ak-text-sm)">
          {results.map((r) => (
            <li key={r.agent} className={cn(!r.ok && "text-ak-bad")}>
              <span className="font-mono">{r.agent}</span>
              {r.ok
                ? r.version.published
                  ? `: published v${r.version.version}, which uses it from its next run.`
                  : r.heldBack !== undefined
                    ? `: draft v${r.version.version}, not published: it builds on v${r.heldBack}, a draft no one has published. Publish it to use ${ts.name}.`
                    : `: draft v${r.version.version}. Publish it to use ${ts.name}.`
                : `: not changed, ${r.error}.`}
            </li>
          ))}
        </ul>
      ) : null}
      <div className="flex flex-wrap gap-2">
        <Button asChild>
          <Link to={`/toolsets/${encodeURIComponent(ts.name)}`}>Open {ts.name}</Link>
        </Button>
        {results
          .filter((r) => r.ok && r.version.published)
          .map((r) => (
            <Button key={r.agent} asChild variant="outline">
              <Link to={`/new?agent=${encodeURIComponent(r.agent)}`}>Chat with {r.agent}</Link>
            </Button>
          ))}
      </div>
    </>
  );
}

const headings = ["Where is the server?", "How does agentkit sign in?", "", "Which agents use it?", ""];

/** /toolsets/new. */
export function McpAddPage() {
  const lists = useLists();
  const [w, setW] = useState<Wizard>(fresh);
  const [results, setResults] = useState<Result[]>([]);
  const set = (p: Partial<Wizard>) => setW((old) => ({ ...old, ...p }));
  const heading = useRef<HTMLHeadingElement>(null);
  const first = useRef(true);
  useEffect(() => {
    if (first.current) {
      first.current = false;
      return;
    }
    heading.current?.focus();
  }, [w.step]);
  if (useForbidden(lists.toolsets)) return <Forbidden />;
  const taken = lists.toolsets?.state === "ready" ? lists.toolsets.data.map((t) => t.name) : [];
  const added = (ts: ServerToolset) => {
    lists.reloadToolsets();
    set({ ts, step: 3 });
  };
  const title =
    w.step === 3 && w.ts && w.ts.auth === "oauth" && w.ts.connection !== "connected"
      ? `Connect ${w.ts.name}`
      : w.step === 3 && w.ts
        ? `${w.ts.name} offers ${w.ts.tools.length} ${w.ts.tools.length === 1 ? "tool" : "tools"}`
        : w.step === 5 && w.ts
          ? `${w.ts.name} is ready`
          : (headings[w.step - 1] ?? "");
  return (
    <Page title="Add an MCP server" back={{ to: "/toolsets", label: "toolsets" }}>
      <Steps step={Math.min(w.step, 4)} />
      <h2 ref={heading} tabIndex={-1} className="text-(length:--ak-text-md) font-semibold text-ak-text outline-none">
        {title}
      </h2>
      {w.step === 1 ? <ServerStep w={w} set={set} taken={taken} onAdded={added} /> : null}
      {w.step === 2 ? <SignInStep w={w} set={set} onAdded={added} /> : null}
      {w.step === 3 && w.ts ? (
        <ReviewStep ts={w.ts} onReviewed={(ts) => set({ ts, step: 4 })} onFresh={(ts) => set({ ts })} />
      ) : null}
      {w.step === 4 && w.ts ? (
        <UseStep
          ts={w.ts}
          onDone={(r) => {
            setResults(r);
            set({ step: 5 });
          }}
        />
      ) : null}
      {w.step === 5 && w.ts ? <DoneStep ts={w.ts} results={results} /> : null}
    </Page>
  );
}
