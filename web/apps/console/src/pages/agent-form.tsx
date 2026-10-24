// The agent builder's form (T077, docs/UI.md "Agent builder"): a section
// per part of the definition. It edits a Draft (agent-def.ts); the page
// (agent-builder.tsx) loads, checks and saves it. Every field's id is
// fieldId(its path in the definition), where the server's field errors and
// the error summary's links point.

import type { Tool, ToolList, Toolset } from "@agentkit/agent-ui/api";
import { Status } from "@agentkit/ui";
import { Button } from "@agentkit/ui/components/button";
import { Input } from "@agentkit/ui/components/input";
import { Textarea } from "@agentkit/ui/components/textarea";
import { cn } from "@agentkit/ui/lib/utils";
import { Plus, TriangleAlert, X } from "lucide-react";
import { useState, type ReactNode } from "react";
import type { Resource } from "../api";
import { toolsetStatus } from "../lists";
import {
  canonical,
  efforts,
  emptyVariant,
  fieldId,
  knownTools,
  matches,
  patternPrefix,
  sectionTitles,
  toolName,
  type Draft,
  type Section,
  type Variant,
} from "./agent-def";
import { Loading } from "./page";

/** What a section's fields need from the page. */
export type FormProps = {
  draft: Draft;
  set: (patch: Partial<Draft>) => void;
  /** The error to show on the field at path, if any. */
  errorFor: (path: string) => string | undefined;
  isNew: boolean;
  /** The models the server knows, by name. */
  known: string[];
  toolsets: Resource<Toolset[]>;
  /** The tools of each toolset used, as they load. */
  tools: Map<string, ToolList | "failed">;
};

const label = "text-(length:--ak-text-sm) font-medium text-ak-text";
const help = "text-xs text-ak-muted";

/** A labelled field: its error (which it is described by), a warning, and help. */
export function Field({
  path,
  label: text,
  help: helpText,
  error,
  children,
}: {
  path: string;
  label: string;
  help?: ReactNode;
  error?: string;
  children: (props: { id: string; "aria-describedby"?: string; "aria-invalid"?: true }) => ReactNode;
}) {
  const id = fieldId(path);
  const ids = [error ? `${id}-err` : "", helpText ? `${id}-help` : ""].filter(Boolean).join(" ");
  return (
    <div className="flex flex-col gap-1.5">
      <label htmlFor={id} className={label}>
        {text}
      </label>
      {children({ id, "aria-describedby": ids || undefined, "aria-invalid": error ? true : undefined })}
      {error ? <FieldError id={`${id}-err`}>{error}</FieldError> : null}
      {helpText ? (
        <p id={`${id}-help`} className={help}>
          {helpText}
        </p>
      ) : null}
    </div>
  );
}

function FieldError({ id, children }: { id: string; children: ReactNode }) {
  return (
    <p id={id} className="flex items-start gap-1.5 text-(length:--ak-text-sm) text-ak-bad">
      <TriangleAlert aria-hidden="true" className="mt-0.5 size-4 flex-none" />
      <span className="min-w-0 break-words">{children}</span>
    </p>
  );
}

/** A section of the form: its h2, which the section nav links to. */
export function FormSection({ id, children, extra }: { id: Section; children: ReactNode; extra?: ReactNode }) {
  return (
    <section id={`sec-${id}`} aria-labelledby={`sec-${id}-h`} className="flex scroll-mt-4 flex-col gap-4">
      <div className="flex items-center gap-2 border-b pb-1.5">
        <h2 id={`sec-${id}-h`} className="flex-1 text-(length:--ak-text-md) font-semibold text-ak-text">
          {sectionTitles[id]}
        </h2>
        {extra}
      </div>
      {children}
    </section>
  );
}

const select =
  "h-9 w-full rounded-md border border-input bg-transparent px-2 text-(length:--ak-text-sm) shadow-xs outline-none focus-visible:border-ring focus-visible:ring-[3px] focus-visible:ring-ring/50 dark:bg-input/30";

export function AboutSection({ draft, set, errorFor, isNew }: FormProps) {
  return (
    <FormSection id="about">
      <Field
        path="name"
        label="Name"
        error={errorFor("name")}
        help={
          isNew
            ? "Fixed once created: chats, tasks and versions refer to it."
            : "Fixed: chats, tasks and versions refer to it."
        }
      >
        {(p) => (
          <Input
            {...p}
            value={draft.name}
            onChange={(e) => set({ name: e.target.value })}
            readOnly={!isNew}
            autoComplete="off"
            spellCheck={false}
            className="font-mono"
          />
        )}
      </Field>
      <Field path="description" label="Description" error={errorFor("description")} help="Shown in the agent picker.">
        {(p) => <Input {...p} value={draft.description} onChange={(e) => set({ description: e.target.value })} />}
      </Field>
    </FormSection>
  );
}

/** A text field with Add, and what it added as chips with Remove. */
function Chips({
  path,
  label: text,
  values,
  onChange,
  known,
  error,
  helpText,
}: {
  path: string;
  label: string;
  values: string[];
  onChange: (v: string[]) => void;
  known: string[];
  error?: string;
  helpText: string;
}) {
  const [entry, setEntry] = useState("");
  const add = () => {
    const v = entry.trim();
    if (v === "") return;
    if (!values.some((x) => canonical(x) === canonical(v))) onChange([...values, v]);
    setEntry("");
  };
  const list = `${fieldId(path)}-known`;
  return (
    <Field path={path} label={text} error={error} help={helpText}>
      {(p) => (
        <div className="flex flex-col gap-2">
          {values.length > 0 ? (
            <ul aria-label={text} className="flex flex-wrap gap-1.5">
              {values.map((v) => (
                <li
                  key={v}
                  className="inline-flex items-center gap-1 rounded-md border bg-ak-surface-2 py-0.5 pr-0.5 pl-2 font-mono text-xs"
                >
                  {v}
                  <button
                    type="button"
                    aria-label={`Remove ${v}`}
                    onClick={() => onChange(values.filter((x) => x !== v))}
                    className="grid size-5 place-items-center rounded-sm text-ak-muted hover:bg-ak-hover hover:text-ak-text"
                  >
                    <X aria-hidden="true" className="size-3" />
                  </button>
                </li>
              ))}
            </ul>
          ) : null}
          <div className="flex gap-2">
            <Input
              {...p}
              list={list}
              value={entry}
              onChange={(e) => setEntry(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter") {
                  e.preventDefault();
                  add();
                }
              }}
              placeholder="anthropic/claude-sonnet-5-5"
              autoComplete="off"
              spellCheck={false}
              className="font-mono"
            />
            <Button type="button" variant="outline" onClick={add} aria-label={`Add to ${text.toLowerCase()}`}>
              Add
            </Button>
            <datalist id={list}>
              {known
                .filter((m) => !values.some((x) => canonical(x) === m))
                .map((m) => (
                  <option key={m} value={m} />
                ))}
            </datalist>
          </div>
        </div>
      )}
    </Field>
  );
}

function EffortSelect({
  path,
  value,
  onChange,
  inherit,
  error,
}: {
  path: string;
  value: string;
  onChange: (v: string) => void;
  inherit: string;
  error?: string;
}) {
  return (
    <Field path={path} label="Effort" error={error}>
      {(p) => (
        <select {...p} value={value} onChange={(e) => onChange(e.target.value)} className={select}>
          <option value="">{inherit}</option>
          {efforts.map((e) => (
            <option key={e} value={e}>
              {e}
            </option>
          ))}
        </select>
      )}
    </Field>
  );
}

export function ModelsSection(props: FormProps) {
  const { draft, set, errorFor, known } = props;
  const list = `${fieldId("model")}-known`;
  const allowed = [draft.model, ...draft.allow].filter((m) => m.trim() !== "").map(canonical);
  return (
    <FormSection id="models">
      <div className="grid gap-4 sm:grid-cols-[1fr_180px]">
        <Field
          path="model"
          label="Default model"
          error={errorFor("model")}
          help="New chats and tasks run it. A name is provider/id; an id alone is Anthropic's."
        >
          {(p) => (
            <>
              <Input
                {...p}
                list={list}
                value={draft.model}
                onChange={(e) => set({ model: e.target.value })}
                placeholder="anthropic/claude-sonnet-5-5"
                autoComplete="off"
                spellCheck={false}
                className="font-mono"
              />
              <datalist id={list}>
                {known.map((m) => (
                  <option key={m} value={m} />
                ))}
              </datalist>
            </>
          )}
        </Field>
        <EffortSelect
          path="model.effort"
          value={draft.effort}
          onChange={(effort) => set({ effort })}
          inherit="The model's default"
          error={errorFor("model.effort")}
        />
      </div>
      <Chips
        path="models.allow"
        label="Also allowed"
        values={draft.allow}
        onChange={(allow) => set({ allow })}
        known={known.filter((m) => m !== canonical(draft.model))}
        error={errorFor("models.allow")}
        helpText="Models a chat or task may pick instead of the default, by name."
      />
      <Patterns {...props} />
      <Variants {...props} allowed={allowed} />
    </FormSection>
  );
}

function Patterns({ draft, set, errorFor, known }: FormProps) {
  const setAt = (i: number, v: string) => set({ patterns: draft.patterns.map((p, j) => (j === i ? v : p)) });
  return (
    <fieldset className="flex flex-col gap-2">
      <legend className={cn(label, "mb-1.5")}>Allowed by pattern</legend>
      <p className={help}>
        Every model whose whole name a pattern matches (RE2, such as{" "}
        <code className="font-mono">anthropic/claude-.*</code>
        ). The chat offers the ones this server knows.
      </p>
      {draft.patterns.map((p, i) => {
        const path = `models.allow_patterns[${i}]`;
        const error = errorFor(path);
        const found = error ? [] : matches(p, known);
        const id = fieldId(path);
        return (
          <div key={i} className="flex flex-col gap-1">
            <div className="flex gap-2">
              <Input
                id={id}
                aria-label={`Pattern ${i + 1}`}
                aria-describedby={`${id}-m`}
                aria-invalid={error ? true : undefined}
                value={p}
                onChange={(e) => setAt(i, e.target.value)}
                autoComplete="off"
                spellCheck={false}
                className="font-mono"
              />
              <Button
                type="button"
                variant="ghost"
                size="icon"
                aria-label={`Remove pattern ${i + 1}`}
                onClick={() => set({ patterns: draft.patterns.filter((_, j) => j !== i) })}
              >
                <X aria-hidden="true" />
              </Button>
            </div>
            {error ? (
              <FieldError id={`${id}-m`}>{error}</FieldError>
            ) : (
              <p id={`${id}-m`} className={help}>
                {p.trim() === ""
                  ? "Write a pattern."
                  : found.length > 0
                    ? `Matches: ${found.join(", ")}`
                    : "Matches none of the models this server knows (it may allow others)."}
              </p>
            )}
          </div>
        );
      })}
      <div>
        <Button type="button" variant="outline" size="sm" onClick={() => set({ patterns: [...draft.patterns, ""] })}>
          <Plus aria-hidden="true" /> Add a pattern
        </Button>
      </div>
    </fieldset>
  );
}

/**
 * Description overrides for tools picked by name (a variant's, and the
 * agent's for tools no used toolset lists): a description each, and Remove.
 */
function OverrideList({
  path,
  legend,
  values,
  onChange,
  names,
  errorFor,
}: {
  path: string;
  legend: string;
  values: Record<string, string>;
  onChange: (v: Record<string, string>) => void;
  names: string[];
  errorFor: (path: string) => string | undefined;
}) {
  const [pick, setPick] = useState("");
  const free = names.filter((n) => !(n in values));
  const pickId = `${fieldId(path)}-pick`;
  return (
    <fieldset className="flex flex-col gap-2">
      <legend className={cn(label, "mb-1.5")}>{legend}</legend>
      {Object.entries(values).map(([name, text]) => (
        <div key={name} className="flex flex-col gap-1">
          <div className="flex items-center gap-2">
            <label htmlFor={fieldId(`${path}.${name}`)} className="flex-1 font-mono text-xs text-ak-text">
              {name}
            </label>
            <Button
              type="button"
              variant="ghost"
              size="xs"
              onClick={() => onChange(Object.fromEntries(Object.entries(values).filter(([n]) => n !== name)))}
              aria-label={`Remove the override of ${name}`}
            >
              Remove
            </Button>
          </div>
          <Textarea
            id={fieldId(`${path}.${name}`)}
            value={text}
            aria-invalid={errorFor(`${path}.${name}`) ? true : undefined}
            onChange={(e) => onChange({ ...values, [name]: e.target.value })}
            className="font-mono text-xs"
          />
          {errorFor(`${path}.${name}`) ? (
            <FieldError id={`${fieldId(`${path}.${name}`)}-err`}>{errorFor(`${path}.${name}`)}</FieldError>
          ) : null}
        </div>
      ))}
      {free.length > 0 ? (
        <div className="flex gap-2">
          <label htmlFor={pickId} className="sr-only">
            Tool to describe
          </label>
          <select
            id={pickId}
            value={pick}
            onChange={(e) => setPick(e.target.value)}
            className={cn(select, "font-mono")}
          >
            <option value="">Pick a tool</option>
            {free.map((n) => (
              <option key={n} value={n}>
                {n}
              </option>
            ))}
          </select>
          <Button
            type="button"
            variant="outline"
            disabled={pick === ""}
            onClick={() => {
              onChange({ ...values, [pick]: "" });
              setPick("");
            }}
          >
            Override
          </Button>
        </div>
      ) : Object.keys(values).length === 0 ? (
        <p className={help}>Use a toolset first: its tools are listed here.</p>
      ) : null}
    </fieldset>
  );
}

function Variants(props: FormProps & { allowed: string[] }) {
  const { draft, set, errorFor, allowed } = props;
  const names = knownTools(props.draft, props.toolsets, props.tools).map((t) => t.name);
  const setAt = (i: number, patch: Partial<Variant>) =>
    set({ variants: draft.variants.map((v, j) => (j === i ? { ...v, ...patch } : v)) });
  return (
    <div className="flex flex-col gap-3">
      <div>
        <h3 className={label}>Variants</h3>
        <p className={help}>
          How the agent differs on some models: its instructions, effort, tokens and tool descriptions. The first that
          matches the model applies.
        </p>
      </div>
      {draft.variants.map((v, i) => {
        const at = `models.variants[${i}]`;
        const list = `${fieldId(at)}-models`;
        return (
          <fieldset key={i} className="flex flex-col gap-3 rounded-md border p-3">
            <legend className="px-1 text-xs font-medium text-ak-text-2">Variant {i + 1}</legend>
            <div className="grid gap-3 sm:grid-cols-[1fr_160px_140px]">
              <Field
                path={`${at}.match`}
                label="On"
                error={errorFor(`${at}.match`)}
                help={`A model the agent allows, or ${patternPrefix} and a pattern.`}
              >
                {(p) => (
                  <>
                    <Input
                      {...p}
                      list={list}
                      value={v.match}
                      onChange={(e) => setAt(i, { match: e.target.value })}
                      autoComplete="off"
                      spellCheck={false}
                      className="font-mono"
                    />
                    <datalist id={list}>
                      {allowed.map((m) => (
                        <option key={m} value={m} />
                      ))}
                    </datalist>
                  </>
                )}
              </Field>
              <EffortSelect
                path={`${at}.effort`}
                value={v.effort}
                onChange={(effort) => setAt(i, { effort })}
                inherit="The agent's"
                error={errorFor(`${at}.effort`)}
              />
              <Field path={`${at}.max_tokens`} label="Max tokens" error={errorFor(`${at}.max_tokens`)}>
                {(p) => (
                  <Input
                    {...p}
                    inputMode="numeric"
                    value={v.maxTokens}
                    onChange={(e) => setAt(i, { maxTokens: e.target.value })}
                    placeholder="The agent's"
                  />
                )}
              </Field>
            </div>
            <fieldset className="flex flex-col gap-1.5">
              <legend className={cn(label, "mb-1")}>Instructions on this model</legend>
              <div className="flex gap-4 text-(length:--ak-text-sm)">
                {(["append", "replace"] as const).map((mode) => (
                  <label key={mode} className="flex items-center gap-1.5">
                    <input
                      type="radio"
                      name={`${at}-mode`}
                      checked={v.mode === mode}
                      onChange={() => setAt(i, { mode })}
                    />
                    {mode === "append" ? "Added after the agent's" : "Instead of the agent's"}
                  </label>
                ))}
              </div>
              <label htmlFor={fieldId(`${at}.system`)} className="sr-only">
                Variant {i + 1} instructions
              </label>
              <Textarea
                id={fieldId(`${at}.system`)}
                value={v.system}
                aria-invalid={errorFor(`${at}.system`) ? true : undefined}
                onChange={(e) => setAt(i, { system: e.target.value })}
                className="font-mono text-xs"
              />
              {errorFor(`${at}.system`) ? (
                <FieldError id={`${fieldId(`${at}.system`)}-err`}>{errorFor(`${at}.system`)}</FieldError>
              ) : null}
            </fieldset>
            <OverrideList
              path={`${at}.tool_descriptions`}
              legend="Tool descriptions on this model"
              values={v.toolDescriptions}
              onChange={(toolDescriptions) => setAt(i, { toolDescriptions })}
              names={names}
              errorFor={errorFor}
            />
            <div>
              <Button
                type="button"
                variant="ghost"
                size="sm"
                onClick={() => set({ variants: draft.variants.filter((_, j) => j !== i) })}
              >
                Remove variant {i + 1}
              </Button>
            </div>
          </fieldset>
        );
      })}
      <div>
        <Button
          type="button"
          variant="outline"
          size="sm"
          onClick={() => set({ variants: [...draft.variants, emptyVariant()] })}
        >
          <Plus aria-hidden="true" /> Add a variant
        </Button>
      </div>
    </div>
  );
}

export function InstructionsSection({ draft, set, errorFor }: FormProps) {
  return (
    <FormSection id="instructions">
      <Field
        path="system"
        label="System prompt"
        error={errorFor("system")}
        help={`${draft.system.length.toLocaleString()} characters`}
      >
        {(p) => (
          <Textarea
            {...p}
            value={draft.system}
            onChange={(e) => set({ system: e.target.value })}
            className="min-h-40 font-mono text-xs"
          />
        )}
      </Field>
    </FormSection>
  );
}

/** One tool of a used toolset: its description, and an override of it. */
function ToolRow({
  name,
  tool,
  value,
  onChange,
  error,
}: {
  name: string;
  tool: Tool;
  value: string | undefined;
  onChange: (v: string | undefined) => void;
  error?: string;
}) {
  const id = fieldId(`tool_descriptions.${name}`);
  return (
    <li className="flex flex-col gap-1.5 py-2">
      <div className="flex items-start gap-2">
        <div className="flex min-w-0 flex-1 flex-col">
          <span className="font-mono text-xs text-ak-text">{name}</span>
          <span id={`${id}-own`} className={cn("text-xs text-ak-muted", value === undefined ? "line-clamp-2" : "")}>
            {tool.description || "No description."}
          </span>
        </div>
        {value === undefined ? (
          <Button
            type="button"
            variant="ghost"
            size="xs"
            onClick={() => onChange(tool.description)}
            aria-label={`Override the description of ${name}`}
          >
            Override description
          </Button>
        ) : (
          <Button
            type="button"
            variant="ghost"
            size="xs"
            onClick={() => onChange(undefined)}
            aria-label={`Use the own description of ${name}`}
          >
            Use its own
          </Button>
        )}
      </div>
      {value !== undefined ? (
        <>
          <label htmlFor={id} className="sr-only">
            The description {name} gets
          </label>
          <Textarea
            id={id}
            value={value}
            aria-invalid={error ? true : undefined}
            aria-describedby={[error ? `${id}-err` : "", `${id}-own`].filter(Boolean).join(" ")}
            onChange={(e) => onChange(e.target.value)}
            className="font-mono text-xs"
          />
          {error ? <FieldError id={`${id}-err`}>{error}</FieldError> : null}
        </>
      ) : null}
    </li>
  );
}

export function ToolsSection(props: FormProps) {
  const { draft, set, errorFor, toolsets, tools } = props;
  const used = (t: Toolset) => (t.runtime ? draft.runtimeToolsets : draft.toolsets).includes(t.name);
  const toggle = (t: Toolset, on: boolean) => {
    const key = t.runtime ? "runtimeToolsets" : "toolsets";
    const list = draft[key];
    set({ [key]: on ? [...list, t.name] : list.filter((n) => n !== t.name) });
  };
  const setDesc = (name: string, v: string | undefined) => {
    const rest = Object.entries(draft.toolDescriptions).filter(([n]) => n !== name);
    set({ toolDescriptions: Object.fromEntries(v === undefined ? rest : [...rest, [name, v]]) });
  };
  const listed = new Set(knownTools(props.draft, props.toolsets, props.tools).map((t) => t.name));
  const others = Object.fromEntries(Object.entries(draft.toolDescriptions).filter(([n]) => !listed.has(n)));
  const served = toolsets.state === "ready" ? toolsets.data : [];
  const unknown = [...draft.toolsets, ...draft.runtimeToolsets].filter((n) => !served.some((t) => t.name === n));
  return (
    <FormSection id="tools">
      <p className={help}>
        The toolsets it uses, and how it describes their tools to the model: an override says how this agent should use
        a tool. The tool&apos;s schema stays its own.
      </p>
      {errorFor("toolsets") ? <FieldError id={`${fieldId("toolsets")}-err`}>{errorFor("toolsets")}</FieldError> : null}
      {toolsets.state === "loading" ? <Loading /> : null}
      {toolsets.state === "failed" ? (
        <p role="alert" className="text-(length:--ak-text-sm) text-ak-bad">
          Couldn&apos;t load the toolsets.{" "}
          <Button variant="link" size="sm" onClick={toolsets.retry}>
            Retry
          </Button>
        </p>
      ) : null}
      <ul id={fieldId("toolsets")} tabIndex={-1} aria-label="Toolsets" className="flex flex-col gap-2 outline-none">
        {served.map((t) => {
          const on = used(t);
          const s = toolsetStatus(t);
          const l = tools.get(t.name);
          const boxId = `ts-${t.name}`;
          return (
            <li key={t.name} className="rounded-md border">
              <div className="flex items-start gap-2 px-3 py-2">
                <input
                  id={boxId}
                  type="checkbox"
                  className="mt-1"
                  checked={on}
                  onChange={(e) => toggle(t, e.target.checked)}
                />
                <label htmlFor={boxId} className="flex min-w-0 flex-1 flex-col text-(length:--ak-text-sm)">
                  <span className="font-mono text-ak-text">{t.name}</span>
                  <span className="text-xs text-ak-muted">
                    {t.type}
                    {t.runtime ? " · added at run time" : ""} · {t.tool_count} {t.tool_count === 1 ? "tool" : "tools"}
                  </span>
                </label>
                <Status tone={s.tone} className="text-xs">
                  {s.text}
                </Status>
              </div>
              {on ? (
                <div className="border-t px-3">
                  {l === undefined ? (
                    <p className="py-2 text-xs text-ak-muted">Loading its tools…</p>
                  ) : l === "failed" ? (
                    <p className="py-2 text-xs text-ak-warn">Couldn&apos;t list its tools.</p>
                  ) : l.tools.length === 0 ? (
                    <p className="py-2 text-xs text-ak-muted">
                      It lists no tools now{t.reason ? `: ${t.reason}` : "."}
                    </p>
                  ) : (
                    <ul aria-label={`Tools of ${t.name}`} className="divide-y">
                      {l.tools.map((tool) => {
                        const name = toolName(draft, t.name, tool.name, t.runtime === true);
                        return (
                          <ToolRow
                            key={name}
                            name={name}
                            tool={tool}
                            value={draft.toolDescriptions[name]}
                            onChange={(v) => setDesc(name, v)}
                            error={errorFor(`tool_descriptions.${name}`)}
                          />
                        );
                      })}
                    </ul>
                  )}
                </div>
              ) : null}
            </li>
          );
        })}
        {unknown.map((n) => (
          <li key={n} className="flex items-center gap-2 rounded-md border px-3 py-2 text-(length:--ak-text-sm)">
            <span className="flex-1 font-mono">{n}</span>
            <span className="text-xs text-ak-warn">Not a toolset this server serves</span>
            <Button
              type="button"
              variant="ghost"
              size="xs"
              onClick={() =>
                set({
                  toolsets: draft.toolsets.filter((x) => x !== n),
                  runtimeToolsets: draft.runtimeToolsets.filter((x) => x !== n),
                })
              }
            >
              Remove
            </Button>
          </li>
        ))}
      </ul>
      {Object.keys(others).length > 0 ? (
        <OverrideList
          path="tool_descriptions"
          legend="Other description overrides"
          values={others}
          onChange={(v) =>
            set({
              toolDescriptions: {
                ...Object.fromEntries(Object.entries(draft.toolDescriptions).filter(([n]) => listed.has(n))),
                ...v,
              },
            })
          }
          names={[]}
          errorFor={errorFor}
        />
      ) : null}
    </FormSection>
  );
}

const interactionWords: Record<string, [string, string]> = {
  interactive: [
    "Interactive",
    "A person is in the chat: it may ask them (ask_user), and calls the policy wants approved wait for their answer.",
  ],
  supervised: [
    "Supervised",
    "Nobody watches live, but it may wait: someone is notified to answer, as for a supervised task.",
  ],
  autonomous: ["Autonomous", "It never waits: ask_user isn't offered, and a call that needs approval is refused."],
};

export function InteractionSection({ draft, set }: FormProps) {
  const value = draft.interaction || "autonomous";
  return (
    <FormSection id="interaction">
      <fieldset id={fieldId("interaction")} tabIndex={-1} className="grid gap-2 outline-none sm:grid-cols-3">
        <legend className="sr-only">Whether its tool calls may wait for people</legend>
        {Object.entries(interactionWords).map(([k, [title, text]]) => (
          <label
            key={k}
            className={cn(
              "flex cursor-pointer flex-col gap-1 rounded-md border p-3 text-(length:--ak-text-sm)",
              value === k && "border-ak-line-strong bg-ak-select",
            )}
          >
            <span className="flex items-center gap-2 font-medium text-ak-text">
              <input type="radio" name="interaction" checked={value === k} onChange={() => set({ interaction: k })} />
              {title}
            </span>
            <span className="text-xs text-ak-muted">{text}</span>
          </label>
        ))}
      </fieldset>
      {draft.interaction === "" ? <p className={help}>Not set: the server takes autonomous.</p> : null}
    </FormSection>
  );
}

export function LimitsSection({ draft, set, errorFor }: FormProps) {
  return (
    <FormSection id="limits">
      <div className="grid gap-4 sm:grid-cols-2">
        <Field
          path="max_turns"
          label="Turns per run"
          error={errorFor("max_turns")}
          help="Model calls; empty for no limit."
        >
          {(p) => (
            <Input
              {...p}
              inputMode="numeric"
              value={draft.maxTurns}
              onChange={(e) => set({ maxTurns: e.target.value })}
            />
          )}
        </Field>
        <Field
          path="max_total_tokens"
          label="Tokens per run"
          error={errorFor("max_total_tokens")}
          help="Input and output together; empty for no limit."
        >
          {(p) => (
            <Input
              {...p}
              inputMode="numeric"
              value={draft.maxTotalTokens}
              onChange={(e) => set({ maxTotalTokens: e.target.value })}
            />
          )}
        </Field>
      </div>
    </FormSection>
  );
}
