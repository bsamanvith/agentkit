// The agent builder, /agents/new and /agents/{name}/edit (T077, docs/UI.md
// "Agent builder"): a full-width page with a section nav and the form
// (agent-form.tsx). Save draft makes a new version (POST
// /v1/agents/{name}/versions with if_latest, so nobody's version is
// overwritten); Publish… confirms, saving first when there are changes. An
// agent of the config file opens read-only, with Duplicate to edit.
// /agents/new?from={name} starts from another agent's definition.

import {
  ApiError,
  type AgentClient,
  type AgentVersion,
  type AgentVersionList,
  type FieldError,
  type ToolList,
} from "@agentkit/agent-ui/api";
import { Button } from "@agentkit/ui/components/button";
import { cn } from "@agentkit/ui/lib/utils";
import { CircleAlert } from "lucide-react";
import { useEffect, useEffectEvent, useMemo, useRef, useState } from "react";
import { Link, useBlocker, useLocation, useNavigate, useParams, useSearchParams } from "react-router";
import { useApi, useResource } from "../api";
import { modHint, modKey, useLayout } from "../layout";
import { listToolsets, useLists } from "../lists";
import {
  changedLines,
  check,
  definitionOf,
  draftOf,
  draftOfAgent,
  emptyDraft,
  fieldId,
  knownModels,
  landing,
  sectionOf,
  sections,
  sectionTitles,
  type Draft,
} from "./agent-def";
import {
  AboutSection,
  InstructionsSection,
  InteractionSection,
  LimitsSection,
  ModelsSection,
  ToolsSection,
  type FormProps,
} from "./agent-form";
import { DefinitionDiff } from "./definition-diff";
import { useForbidden } from "./forbidden";
import { Loaded, Page } from "./page";
import { Callout, Forbidden, Modal } from "./parts";

/** Where the builder starts: the draft, and the versions it builds on. */
type Start = {
  draft: Draft;
  /** The agent's versions; null for a new agent. */
  list: AgentVersionList | null;
  /** The version the draft is read from; null for a new agent or a config one. */
  base: AgentVersion | null;
  /** An agent of the config file: shown, not saved. */
  config: boolean;
};

const newest = (l: AgentVersionList | null) =>
  l?.versions.reduce<AgentVersion | null>((a, v) => (!a || v.version > a.version ? v : a), null) ?? null;

/** Another agent's definition to start a new one from, named "{name}-copy". */
async function copyOf(c: AgentClient, from: string, signal: AbortSignal): Promise<Draft> {
  const list = await c.listAgentVersions(from, { signal }).catch((e: unknown) => {
    if (e instanceof ApiError && e.status === 404) return null; // the server keeps no agents
    throw e;
  });
  const v = newest(list);
  const draft = v ? draftOf(v.definition) : draftOfAgent(await c.getAgent(from, { signal }));
  return { ...draft, name: `${from}-copy` };
}

async function loadStart(
  c: AgentClient,
  name: string | undefined,
  from: string | null,
  signal: AbortSignal,
): Promise<Start> {
  if (name === undefined) {
    const draft = from ? await copyOf(c, from, signal) : emptyDraft();
    return { draft, list: null, base: null, config: false };
  }
  const list = await c.listAgentVersions(name, { signal });
  if (list.read_only) {
    return { draft: { ...draftOfAgent(await c.getAgent(name, { signal })), name }, list, base: null, config: true };
  }
  const want = from ? Number(from) : NaN;
  const base = list.versions.find((v) => v.version === want) ?? newest(list);
  return { draft: { ...(base ? draftOf(base.definition) : emptyDraft()), name }, list, base, config: false };
}

/** The tools of each toolset named, listed once each as they are first named. */
function useToolLists(names: string[]): Map<string, ToolList | "failed"> {
  const { client } = useApi();
  const [lists, setLists] = useState(() => new Map<string, ToolList | "failed">());
  const asked = useRef(new Set<string>());
  const key = names.join("\n");
  useEffect(() => {
    for (const n of key === "" ? [] : key.split("\n")) {
      if (asked.current.has(n)) continue;
      asked.current.add(n);
      client.listTools(n).then(
        (l) => setLists((m) => new Map(m).set(n, l)),
        () => setLists((m) => new Map(m).set(n, "failed")),
      );
    }
  }, [client, key]);
  return lists;
}

/** Every field path of the draft's form: where an error can land. */
function fieldPaths(d: Draft): Set<string> {
  const paths = [
    "name",
    "description",
    "model",
    "model.effort",
    "models.allow",
    "system",
    "toolsets",
    "interaction",
    "max_turns",
    "max_total_tokens",
    ...d.patterns.map((_, i) => `models.allow_patterns[${i}]`),
    ...Object.keys(d.toolDescriptions).map((n) => `tool_descriptions.${n}`),
    ...d.variants.flatMap((v, i) => [
      ...["match", "effort", "max_tokens", "system"].map((f) => `models.variants[${i}].${f}`),
      ...Object.keys(v.toolDescriptions).map((n) => `models.variants[${i}].tool_descriptions.${n}`),
    ]),
  ];
  return new Set(paths);
}

type Problem = { path: string; message: string };

/** Moves focus to the element of id, scrolled into view. */
function focusOn(id: string) {
  const el = document.getElementById(id);
  el?.scrollIntoView?.({ block: "center" });
  el?.focus({ preventScroll: true });
}

type Dialog = { kind: "publish" } | { kind: "conflict"; list: AgentVersionList } | null;

function Builder({ start, isNew }: { start: Start; isNew: boolean }) {
  const { client } = useApi();
  const { reloadAgents } = useLists();
  const { phone } = useLayout();
  const navigate = useNavigate();
  const [draft, setDraft] = useState(start.draft);
  // The definition as last saved (as loaded); for a new agent, an empty one,
  // so that a duplicate counts as changes.
  const [saved, setSaved] = useState(() => JSON.stringify(definitionOf(isNew ? emptyDraft() : start.draft)));
  const [list, setList] = useState(start.list);
  const [base, setBase] = useState(start.base);
  const [attempted, setAttempted] = useState(false);
  const [server, setServer] = useState<FieldError[]>([]);
  const [formError, setFormError] = useState("");
  const [notice, setNotice] = useState("");
  const [busy, setBusy] = useState(false);
  const [dialog, setDialog] = useState<Dialog>(null);
  const summary = useRef<HTMLDivElement>(null);
  const leaving = useRef(false);

  const readOnly = start.config || phone;
  const name = draft.name.trim();
  const def = useMemo(() => definitionOf(draft), [draft]);
  const dirty = !readOnly && JSON.stringify(def) !== saved;
  // Whether Save has something to save: a new agent always does.
  const changed = !readOnly && (isNew || dirty);

  const toolsets = useResource(listToolsets, "toolsets");
  const models = useResource(async (c, signal) => c.listModels({ signal }).catch(() => null), "models");
  const known = knownModels(models.state === "ready" ? models.data : null);
  const tools = useToolLists([...draft.toolsets, ...draft.runtimeToolsets]);

  // Inline checks once a save was tried; a pattern's at once (it shows its matches).
  const local = check(draft, isNew);
  const shown = new Map([...local].filter(([p]) => attempted || p.startsWith("models.allow_patterns")));
  const paths = fieldPaths(draft);
  const problems: Problem[] = [
    ...[...shown].map(([path, message]) => ({ path, message })),
    ...server.map((f) => ({ path: f.field ?? "", message: f.message })),
  ];
  const errorFor = (path: string) => {
    const at = problems.filter((p) => landing(p.path, paths) === path).map((p) => p.message);
    return at.length > 0 ? at.join(" ") : undefined;
  };
  const set = (patch: Partial<Draft>) => {
    setDraft((d) => ({ ...d, ...patch }));
    setNotice("");
  };

  const blocker = useBlocker(
    ({ currentLocation, nextLocation }) =>
      dirty && !leaving.current && currentLocation.pathname !== nextLocation.pathname,
  );
  useEffect(() => {
    if (!dirty) return;
    const onUnload = (e: BeforeUnloadEvent) => e.preventDefault();
    window.addEventListener("beforeunload", onUnload);
    return () => window.removeEventListener("beforeunload", onUnload);
  }, [dirty]);

  const showSummary = () => requestAnimationFrame(() => summary.current?.focus());

  /** Saves the draft as a new version on top of ifLatest; the version, or null when it wasn't saved. */
  const save = async (publish: boolean, ifLatest = list?.latest ?? 0): Promise<AgentVersion | null> => {
    setAttempted(true);
    setServer([]);
    setFormError("");
    setNotice("");
    if (local.size > 0) {
      showSummary();
      return null;
    }
    setBusy(true);
    try {
      const v = await client.saveAgentVersion(name, { definition: def, if_latest: ifLatest, publish });
      setSaved(JSON.stringify(def));
      setBase(v);
      setList(await client.listAgentVersions(name).catch(() => list));
      reloadAgents();
      setNotice(publish ? `Published v${v.version}.` : `Saved v${v.version} as a draft. It runs once published.`);
      if (isNew) {
        leaving.current = true;
        navigate(`/agents/${encodeURIComponent(name)}/edit`, { replace: true });
      }
      return v;
    } catch (e) {
      if (e instanceof ApiError && e.code === "invalid_definition") {
        setServer(e.fields.length > 0 ? e.fields : [{ message: e.message }]);
        showSummary();
      } else if (e instanceof ApiError && e.status === 409 && isNew) {
        setServer([
          {
            field: "name",
            message:
              e.code === "read_only"
                ? "An agent of the server's config file has this name: pick another."
                : "An agent of this name exists: pick another, or edit that one.",
          },
        ]);
        showSummary();
      } else if (e instanceof ApiError && e.code === "conflict") {
        setDialog({ kind: "conflict", list: await client.listAgentVersions(name) });
      } else {
        setFormError(
          e instanceof ApiError
            ? `${e.message} (${e.status} ${e.code})`
            : "Couldn't reach the server. Nothing was saved.",
        );
      }
      return null;
    } finally {
      setBusy(false);
    }
  };

  const latest = newest(list);
  const publishedNow = list?.published ?? 0;
  // What Publish publishes: a new version of the changes, or the newest draft.
  const toPublish = changed ? (list?.latest ?? 0) + 1 : latest && latest.version !== publishedNow ? latest.version : 0;

  const publish = async () => {
    setDialog(null);
    if (changed) {
      await save(true);
      return;
    }
    setBusy(true);
    try {
      setList(await client.publishAgent(name, toPublish));
      reloadAgents();
      setNotice(`Published v${toPublish}.`);
    } catch (e) {
      setFormError(e instanceof ApiError ? `${e.message} (${e.status} ${e.code})` : "Couldn't reach the server.");
    } finally {
      setBusy(false);
    }
  };

  // Mod+S saves a draft.
  const saveDraft = useEffectEvent(() => {
    if (!busy && changed) void save(false);
  });
  useEffect(() => {
    if (readOnly) return;
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && !e.shiftKey && e.key.toLowerCase() === "s") {
        e.preventDefault();
        saveDraft();
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [readOnly]);

  const state = start.config
    ? "From the server config"
    : [
        isNew ? "New" : publishedNow > 0 ? `Published v${publishedNow}` : "Not published",
        latest && latest.version !== publishedNow ? `draft v${latest.version}` : "",
        dirty ? "Unsaved changes" : "",
      ]
        .filter(Boolean)
        .join(" · ");

  const props: FormProps = { draft, set, errorFor, isNew, known, toolsets, tools };
  const bySection = new Set(problems.map((p) => sectionOf(p.path)));
  const title = isNew ? "New agent" : start.config ? start.draft.name : `Edit ${start.draft.name}`;

  return (
    <Page
      title={title}
      mono={false}
      back={{
        to: isNew ? "/agents" : `/agents/${encodeURIComponent(start.draft.name)}`,
        label: isNew ? "Agents" : start.draft.name,
      }}
      wrap
      aside={
        <div className="flex flex-none flex-wrap items-center gap-2">
          <span className="text-xs text-ak-muted">{state}</span>
          {!isNew && !start.config ? (
            <Button asChild variant="ghost" size="sm">
              <Link to={`/agents/${encodeURIComponent(name)}/versions`}>Versions</Link>
            </Button>
          ) : null}
          {readOnly ? null : (
            <>
              <Button
                variant="outline"
                size="sm"
                disabled={busy || !changed}
                onClick={() => void save(false)}
                aria-keyshortcuts={`${modKey}+S`}
                title={`${modHint}S`}
              >
                Save draft
              </Button>
              <Button size="sm" disabled={busy || toPublish === 0} onClick={() => setDialog({ kind: "publish" })}>
                Publish…
              </Button>
            </>
          )}
        </div>
      }
      body="bare"
    >
      <div className="flex min-h-0 flex-1">
        {phone ? null : (
          <nav aria-label="On this page" className="w-[172px] flex-none overflow-auto border-r p-3">
            <ul className="flex flex-col gap-0.5">
              {sections.map((s) => (
                <li key={s}>
                  <a
                    href={`#sec-${s}`}
                    onClick={(e) => {
                      e.preventDefault();
                      document.getElementById(`sec-${s}`)?.scrollIntoView?.({ block: "start" });
                    }}
                    className="flex items-center gap-2 rounded-md px-2 py-1 text-(length:--ak-text-sm) text-ak-text-2 hover:bg-ak-hover"
                  >
                    <span className="flex-1">{sectionTitles[s]}</span>
                    {bySection.has(s) ? (
                      <span className="size-1.5 rounded-full bg-ak-bad">
                        <span className="sr-only">, needs a fix</span>
                      </span>
                    ) : null}
                  </a>
                </li>
              ))}
            </ul>
          </nav>
        )}
        <div className="min-h-0 flex-1 overflow-auto">
          <div className={cn("flex max-w-[760px] flex-col gap-8", phone ? "p-4" : "p-6")}>
            {start.config ? (
              <Callout title={`${start.draft.name} comes from the server config file, so it can't be changed here.`}>
                <div className="mt-2 flex flex-wrap items-center gap-2">
                  <Button asChild size="sm">
                    <Link to={`/agents/new?from=${encodeURIComponent(start.draft.name)}`}>Duplicate to edit</Link>
                  </Button>
                  <span className="text-xs text-ak-muted">
                    A duplicate copies what the API shows: not its variants, skills, policy or memory.
                  </span>
                </div>
              </Callout>
            ) : phone ? (
              <Callout title="Editing an agent needs a wider screen.">This is the version the builder opened.</Callout>
            ) : null}
            {problems.length > 0 && (attempted || server.length > 0) ? (
              <div
                ref={summary}
                tabIndex={-1}
                role="alert"
                aria-labelledby="problems-h"
                className="flex flex-col gap-2 rounded-md border border-ak-bad/40 bg-ak-bad/5 p-3 outline-none"
              >
                <p id="problems-h" className="flex items-center gap-1.5 font-medium text-ak-text">
                  <CircleAlert aria-hidden="true" className="size-4 text-ak-bad" />
                  The draft wasn&apos;t saved: {problems.length} {problems.length === 1 ? "field needs" : "fields need"}{" "}
                  a fix.
                </p>
                <ul className="flex list-disc flex-col gap-1 pl-6 text-(length:--ak-text-sm)">
                  {problems.map((p, i) => {
                    const at = landing(p.path, paths);
                    const target = at ? fieldId(at) : `sec-${sectionOf(p.path)}`;
                    return (
                      <li key={i}>
                        <a
                          href={`#${target}`}
                          onClick={(e) => {
                            e.preventDefault();
                            focusOn(target);
                          }}
                          className="text-ak-text underline underline-offset-2"
                        >
                          {sectionTitles[sectionOf(p.path)]}
                          {p.path ? <span className="font-mono"> ({p.path})</span> : null}
                        </a>
                        : {p.message}
                      </li>
                    );
                  })}
                </ul>
              </div>
            ) : null}
            {formError ? (
              <p role="alert" className="flex items-start gap-1.5 text-(length:--ak-text-sm) text-ak-bad">
                <CircleAlert aria-hidden="true" className="mt-0.5 size-4 flex-none" />
                {formError}
              </p>
            ) : null}
            <p role="status" className={notice ? "text-(length:--ak-text-sm) text-ak-text-2" : "sr-only"}>
              {notice}
            </p>
            <form
              aria-label={title}
              onSubmit={(e) => {
                e.preventDefault();
                void save(false);
              }}
            >
              <fieldset disabled={readOnly || busy} className="flex flex-col gap-8">
                <AboutSection {...props} />
                <ModelsSection {...props} />
                <InstructionsSection {...props} />
                <ToolsSection {...props} />
                <InteractionSection {...props} />
                <LimitsSection {...props} />
              </fieldset>
            </form>
          </div>
        </div>
      </div>
      {dialog?.kind === "publish" ? (
        <PublishDialog
          name={name}
          version={toPublish}
          to={changed ? def : (latest?.definition ?? def)}
          list={list}
          saving={changed}
          onConfirm={() => void publish()}
          onClose={() => setDialog(null)}
        />
      ) : null}
      {dialog?.kind === "conflict" ? (
        <ConflictDialog
          list={dialog.list}
          base={base}
          onSaveMine={() => {
            setDialog(null);
            setList(dialog.list);
            void save(false, dialog.list.latest);
          }}
          onLoadTheirs={(v) => {
            setDialog(null);
            const d = { ...draftOf(v.definition), name };
            setDraft(d);
            setSaved(JSON.stringify(definitionOf(d)));
            setList(dialog.list);
            setBase(v);
            setServer([]);
            setAttempted(false);
          }}
          onClose={() => setDialog(null)}
        />
      ) : null}
      {blocker.state === "blocked" ? (
        <Modal title="Leave without saving?" onClose={() => blocker.reset()}>
          <p className="text-(length:--ak-text-sm) text-ak-text-2">
            Your changes to {name || "the new agent"} aren&apos;t saved.
          </p>
          <div className="mt-4 flex justify-end gap-2">
            <Button variant="outline" onClick={() => blocker.reset()} data-autofocus="">
              Stay
            </Button>
            <Button variant="destructive" onClick={() => blocker.proceed()}>
              Leave
            </Button>
          </div>
        </Modal>
      ) : null}
    </Page>
  );
}

function PublishDialog({
  name,
  version,
  to,
  list,
  saving,
  onConfirm,
  onClose,
}: {
  name: string;
  version: number;
  to: Record<string, unknown>;
  list: AgentVersionList | null;
  saving: boolean;
  onConfirm: () => void;
  onClose: () => void;
}) {
  const published = list?.versions.find((v) => v.version === list.published) ?? null;
  const lines = published ? changedLines(published.definition, to) : 0;
  return (
    <Modal title={`Publish v${version} of ${name}?`} onClose={onClose}>
      <div className="flex flex-col gap-2 text-(length:--ak-text-sm) text-ak-text-2">
        <p>
          {published
            ? `${lines} ${lines === 1 ? "line differs" : "lines differ"} from v${published.version}, which is published now.`
            : "No version is published now."}{" "}
          New chats and tasks use v{version}. Runs already going keep the version they started with.
        </p>
        {saving ? <p>Your changes are saved as v{version} first.</p> : null}
      </div>
      <div className="mt-4 flex justify-end gap-2">
        <Button variant="outline" onClick={onClose}>
          Cancel
        </Button>
        <Button onClick={onConfirm} data-autofocus="">
          Publish v{version}
        </Button>
      </div>
    </Modal>
  );
}

function ConflictDialog({
  list,
  base,
  onSaveMine,
  onLoadTheirs,
  onClose,
}: {
  list: AgentVersionList;
  base: AgentVersion | null;
  onSaveMine: () => void;
  onLoadTheirs: (v: AgentVersion) => void;
  onClose: () => void;
}) {
  const theirs = newest(list);
  return (
    <Modal title="Someone saved a version meanwhile" onClose={onClose}>
      <div className="flex flex-col gap-3 text-(length:--ak-text-sm) text-ak-text-2">
        <p>
          {theirs
            ? `${theirs.author || "Someone"} saved v${theirs.version} while you were editing. Your draft isn't saved.`
            : "The agent's versions changed while you were editing. Your draft isn't saved."}
        </p>
        {theirs && base ? (
          <details>
            <summary className="cursor-pointer text-ak-text">What changed in v{theirs.version}</summary>
            <div className="mt-2">
              <DefinitionDiff
                from={base.definition}
                to={theirs.definition}
                label={`v${base.version} to v${theirs.version}`}
              />
            </div>
          </details>
        ) : null}
      </div>
      <div className="mt-4 flex flex-wrap justify-end gap-2">
        {theirs ? (
          <Button variant="outline" onClick={() => onLoadTheirs(theirs)}>
            Discard mine, load v{theirs.version}
          </Button>
        ) : null}
        <Button onClick={onSaveMine} data-autofocus="">
          Save mine as v{list.latest + 1}
        </Button>
      </div>
    </Modal>
  );
}

export function AgentBuilderPage() {
  const { name } = useParams();
  const [search] = useSearchParams();
  const { pathname } = useLocation();
  const from = search.get("from");
  const res = useResource((c, signal) => loadStart(c, name, from, signal), `builder\n${name ?? ""}\n${from ?? ""}`);
  const forbidden = useForbidden(res);
  if (forbidden) return <Forbidden />;
  if (res.state !== "ready") {
    return (
      <Page title={name ? `Edit ${name}` : "New agent"}>
        <Loaded res={res} what={name ? `${name}'s versions` : "the agent to copy"}>
          {() => null}
        </Loaded>
      </Page>
    );
  }
  return <Builder key={`${pathname}?${from ?? ""}`} start={res.data} isNew={name === undefined} />;
}
