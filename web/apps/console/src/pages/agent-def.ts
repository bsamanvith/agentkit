// The agent builder's model of a definition (T077, docs/UI.md "Agent
// builder"): the form's draft, read from a stored definition and written
// back as one. What the form doesn't edit (skills, triggers, the tool
// policy, memory, …) is kept as it was, in rest, so saving never drops it.
// Also: the inline checks, where the server's field errors land, the
// patterns' live matches, and the text the versions page compares.

import type { Agent, ModelList, Tool, ToolList, Toolset } from "@agentkit/agent-ui/api";
import type { Resource } from "../api";

export const efforts = ["low", "medium", "high", "xhigh", "max"] as const;
export const interactions = ["interactive", "supervised", "autonomous"] as const;
/** Tool names under a prefix: "<toolset>__<tool>" (agentapi.ToolPrefixSeparator). */
export const prefixSeparator = "__";
/** The agent name the server takes (agentapi's validName). */
export const namePattern = /^[a-zA-Z0-9_-]{1,64}$/;
/** A variant's match that is a pattern (modelset.PatternPrefix). */
export const patternPrefix = "re:";
/** The longest description override (cmd/agentkit maxToolDescription). */
export const maxToolDescription = 4 << 10;

export type Variant = {
  match: string;
  /** Whether system replaces the agent's prompt or is added after it. */
  mode: "append" | "replace";
  system: string;
  effort: string;
  maxTokens: string;
  toolDescriptions: Record<string, string>;
  rest: Record<string, unknown>;
};

export type Draft = {
  name: string;
  description: string;
  /** The default model: "<provider>/<id>", or a bare id (Anthropic's). */
  model: string;
  effort: string;
  /** The model's other settings (base_url, tool_search, …), as they were. */
  modelRest: Record<string, unknown>;
  allow: string[];
  patterns: string[];
  variants: Variant[];
  system: string;
  toolsets: string[];
  /** Toolsets added at run time (POST /v1/toolsets): its runtime_toolsets. */
  runtimeToolsets: string[];
  toolDescriptions: Record<string, string>;
  interaction: string;
  maxTurns: string;
  maxTotalTokens: string;
  rest: Record<string, unknown>;
};

export function emptyDraft(): Draft {
  return {
    name: "",
    description: "",
    model: "",
    effort: "",
    modelRest: {},
    allow: [],
    patterns: [],
    variants: [],
    system: "",
    toolsets: [],
    runtimeToolsets: [],
    toolDescriptions: {},
    interaction: "",
    maxTurns: "",
    maxTotalTokens: "",
    rest: {},
  };
}

export function emptyVariant(): Variant {
  return { match: "", mode: "append", system: "", effort: "", maxTokens: "", toolDescriptions: {}, rest: {} };
}

const str = (v: unknown) => (typeof v === "string" ? v : "");
const strs = (v: unknown) => (Array.isArray(v) ? v.filter((x): x is string => typeof x === "string") : []);
const obj = (v: unknown): Record<string, unknown> =>
  v && typeof v === "object" && !Array.isArray(v) ? { ...(v as Record<string, unknown>) } : {};
const num = (v: unknown) => (typeof v === "number" && v > 0 ? String(v) : "");
const descs = (v: unknown): Record<string, string> =>
  Object.fromEntries(Object.entries(obj(v)).filter((e): e is [string, string] => typeof e[1] === "string"));

/** Takes keys out of o: what's left is what the form doesn't edit. */
function without(o: Record<string, unknown>, keys: readonly string[]): Record<string, unknown> {
  return Object.fromEntries(Object.entries(o).filter(([k]) => !keys.includes(k)));
}

const draftKeys = [
  "name",
  "description",
  "model",
  "models",
  "system",
  "toolsets",
  "runtime_toolsets",
  "tool_descriptions",
  "interaction",
  "max_turns",
  "max_total_tokens",
] as const;
const variantKeys = ["match", "system", "system_append", "effort", "max_tokens", "tool_descriptions"] as const;

/** The model's name as the form shows it: "<provider>/<id>", or the bare id. */
export function modelName(m: Record<string, unknown>): string {
  const provider = str(m.provider);
  const id = str(m.id);
  return provider && id ? `${provider}/${id}` : id;
}

/** A stored definition as the form's draft. */
export function draftOf(def: Record<string, unknown>): Draft {
  const model = obj(def.model);
  const models = obj(def.models);
  const variants = Array.isArray(models.variants) ? models.variants.map(obj) : [];
  return {
    name: str(def.name),
    description: str(def.description),
    model: modelName(model),
    effort: str(model.effort),
    modelRest: without(model, ["provider", "id", "effort"]),
    allow: strs(models.allow),
    patterns: strs(models.allow_patterns),
    variants: variants.map((v) => ({
      match: str(v.match),
      mode: str(v.system) !== "" ? "replace" : "append",
      system: str(v.system) || str(v.system_append),
      effort: str(v.effort),
      maxTokens: num(v.max_tokens),
      toolDescriptions: descs(v.tool_descriptions),
      rest: without(v, variantKeys),
    })),
    system: str(def.system),
    toolsets: strs(def.toolsets),
    runtimeToolsets: strs(def.runtime_toolsets),
    toolDescriptions: descs(def.tool_descriptions),
    interaction: str(def.interaction),
    maxTurns: num(def.max_turns),
    maxTotalTokens: num(def.max_total_tokens),
    rest: without(obj(def), draftKeys),
  };
}

/**
 * What GET /v1/agents/{name} tells an admin of a config agent, as a draft to
 * duplicate: the API doesn't show its variants, skills' paths, policy or
 * memory, so those aren't copied.
 */
export function draftOfAgent(a: Agent): Draft {
  const model = modelName(a.model);
  return {
    ...emptyDraft(),
    description: a.description,
    model,
    effort: a.model.effort ?? "",
    allow: (a.models?.allow ?? []).filter((m) => m !== canonical(model)),
    patterns: a.models?.allow_patterns ?? [],
    system: a.system ?? "",
    toolsets: a.toolsets.map((t) => t.name),
    toolDescriptions: { ...a.tool_descriptions },
  };
}

/** Only the keys whose value says something: a definition the server reads as the draft. */
function compact(o: Record<string, unknown>): Record<string, unknown> {
  return Object.fromEntries(
    Object.entries(o).filter(
      ([, v]) =>
        v !== undefined &&
        v !== "" &&
        !(Array.isArray(v) && v.length === 0) &&
        !(v && typeof v === "object" && !Array.isArray(v) && Object.keys(v).length === 0),
    ),
  );
}

const count = (s: string) => (s.trim() === "" ? undefined : Number(s.trim()));

/** The model's name split into the definition's provider and id. */
export function splitModel(name: string): { provider?: string; id: string } {
  const n = name.trim();
  const i = n.indexOf("/");
  return i > 0 ? { provider: n.slice(0, i), id: n.slice(i + 1) } : { id: n };
}

/** The draft as the definition to save. */
export function definitionOf(d: Draft): Record<string, unknown> {
  const models = compact({
    allow: d.allow,
    allow_patterns: d.patterns,
    variants: d.variants.map((v) =>
      compact({
        match: v.match.trim(),
        [v.mode === "replace" ? "system" : "system_append"]: v.system,
        effort: v.effort,
        max_tokens: count(v.maxTokens),
        tool_descriptions: v.toolDescriptions,
        ...v.rest,
      }),
    ),
  });
  return compact({
    name: d.name.trim(),
    description: d.description,
    model: compact({ ...splitModel(d.model), effort: d.effort, ...d.modelRest }),
    models,
    system: d.system,
    toolsets: d.toolsets,
    runtime_toolsets: d.runtimeToolsets,
    tool_descriptions: d.toolDescriptions,
    interaction: d.interaction,
    max_turns: count(d.maxTurns),
    max_total_tokens: count(d.maxTotalTokens),
    ...d.rest,
  });
}

/** Whether the agent names its tools "<toolset>__<tool>". */
export function prefixes(d: Draft): boolean {
  return d.rest.prefix_tool_names === true;
}

/** The name the model calls a toolset's tool by, which overrides are keyed by. */
export function toolName(d: Draft, toolset: string, tool: string, runtime: boolean): string {
  return runtime || prefixes(d) ? `${toolset}${prefixSeparator}${tool}` : tool;
}

/** The tool names the form knows: the used toolsets' tools, as the model calls them. */
export function knownTools(
  draft: Draft,
  toolsets: Resource<Toolset[]>,
  tools: Map<string, ToolList | "failed">,
): { name: string; tool: Tool }[] {
  const runtime = new Set(toolsets.state === "ready" ? toolsets.data.filter((t) => t.runtime).map((t) => t.name) : []);
  return [...draft.toolsets, ...draft.runtimeToolsets].flatMap((ts) => {
    const l = tools.get(ts);
    if (!l || l === "failed") return [];
    return l.tools.map((tool) => ({ name: toolName(draft, ts, tool.name, runtime.has(ts)), tool }));
  });
}

// Field ids: a field's id in the page is its path in the definition, which
// is where the server's field errors point ("models.allow[1]").

/** The DOM id of the field at path. */
export function fieldId(path: string): string {
  return `f-${path.replace(/[^a-zA-Z0-9_-]/g, (c) => `_${c.charCodeAt(0).toString(16)}`)}`;
}

/**
 * Where a server's field error lands: the field at its path, or the nearest
 * one above it ("model.id" lands on "model"); "" for none.
 */
export function landing(path: string, fields: ReadonlySet<string>): string {
  let p = path;
  while (p !== "") {
    if (fields.has(p)) return p;
    const cut = Math.max(p.lastIndexOf("."), p.lastIndexOf("["));
    p = cut > 0 ? p.slice(0, cut) : "";
  }
  return "";
}

/** The section of the form a path is in, for the section nav's dot. */
export function sectionOf(path: string): Section {
  const top = path.split(/[.[]/)[0] ?? "";
  if (top === "model" || top === "models") return "models";
  if (top === "system") return "instructions";
  if (top === "toolsets" || top === "runtime_toolsets" || top === "tool_descriptions") return "tools";
  if (top === "interaction") return "interaction";
  if (top === "max_turns" || top === "max_total_tokens") return "limits";
  return "about";
}

export const sections = ["about", "models", "instructions", "tools", "interaction", "limits"] as const;
export type Section = (typeof sections)[number];
export const sectionTitles: Record<Section, string> = {
  about: "Name and description",
  models: "Models",
  instructions: "Instructions",
  tools: "Tools",
  interaction: "Interaction",
  limits: "Limits",
};

/** A pattern as the server reads it: RE2, anchored at both ends. null when it isn't one. */
export function patternOf(p: string): RegExp | null {
  try {
    return new RegExp(`^(?:${p})$`);
  } catch {
    return null;
  }
}

/** The models the server knows, by name ("anthropic/claude-sonnet-5-5"; GET /v1/models sends them so). */
export function knownModels(list: ModelList | null): string[] {
  return (list?.providers ?? []).flatMap((p) => p.models.map((m) => (m.includes("/") ? m : `${p.name}/${m}`)));
}

/** The name of a model as the server compares them: a bare id is Anthropic's. */
export function canonical(name: string): string {
  const n = name.trim();
  return n === "" || n.includes("/") ? n : `anthropic/${n}`;
}

/** The known models a pattern allows. */
export function matches(pattern: string, known: string[]): string[] {
  const re = patternOf(pattern);
  return re ? known.filter((m) => re.test(m)) : [];
}

const positive = (s: string) => s.trim() === "" || /^[1-9][0-9]{0,9}$/.test(s.trim());

/** The draft's inline checks, by field path. */
export function check(d: Draft, isNew: boolean): Map<string, string> {
  const errs = new Map<string, string>();
  if (isNew && !namePattern.test(d.name.trim())) {
    errs.set("name", "1 to 64 letters, digits, - and _.");
  }
  if (d.model.trim() === "") errs.set("model", "Pick the model it runs by default.");
  else if (/\s/.test(d.model.trim()) || d.model.trim().endsWith("/"))
    errs.set("model", "A model name, such as anthropic/claude-sonnet-5-5.");
  d.patterns.forEach((p, i) => {
    if (p.trim() === "") errs.set(`models.allow_patterns[${i}]`, "Empty: remove it or write a pattern.");
    else if (!patternOf(p)) errs.set(`models.allow_patterns[${i}]`, "Not a valid pattern.");
  });
  d.variants.forEach((v, i) => {
    const at = `models.variants[${i}]`;
    if (v.match.trim() === "") errs.set(`${at}.match`, "Name the model, or re: and a pattern.");
    else if (v.match.startsWith(patternPrefix) && !patternOf(v.match.slice(patternPrefix.length))) {
      errs.set(`${at}.match`, "Not a valid pattern.");
    }
    if (!positive(v.maxTokens)) errs.set(`${at}.max_tokens`, "A whole number above 0, or empty.");
    checkDescs(errs, `${at}.tool_descriptions`, v.toolDescriptions);
  });
  checkDescs(errs, "tool_descriptions", d.toolDescriptions);
  if (!positive(d.maxTurns)) errs.set("max_turns", "A whole number above 0, or empty for no limit.");
  if (!positive(d.maxTotalTokens)) errs.set("max_total_tokens", "A whole number above 0, or empty for no limit.");
  return errs;
}

function checkDescs(errs: Map<string, string>, at: string, descs: Record<string, string>) {
  for (const [name, text] of Object.entries(descs)) {
    if (text.trim() === "") errs.set(`${at}.${name}`, "Empty: remove the override to keep the tool's own.");
    else if (new TextEncoder().encode(text).length > maxToolDescription) {
      errs.set(`${at}.${name}`, `Longer than ${maxToolDescription} bytes.`);
    }
  }
}

// The text the versions page compares (docs/UI.md "Versions"): the
// definition, a line per value, long strings a sentence per line, so a
// changed sentence of the instructions is one changed line.

/** A definition as lines of text. */
export function definitionText(def: Record<string, unknown>): string[] {
  const out: string[] = [];
  const write = (key: string, v: unknown, ind: string) => {
    const head = key === "" ? `${ind}-` : `${ind}${key}:`;
    if (typeof v === "string" && (v.includes("\n") || v.length > 80)) {
      out.push(`${head} |`);
      for (const line of v.split("\n")) {
        for (const s of line.split(/(?<=[.!?])\s+/)) out.push(`${ind}  ${s}`);
      }
    } else if (Array.isArray(v)) {
      if (v.length === 0) out.push(`${head} []`);
      else {
        out.push(head);
        for (const x of v) write("", x, `${ind}  `);
      }
    } else if (v && typeof v === "object") {
      const entries = Object.entries(v as Record<string, unknown>);
      if (entries.length === 0) out.push(`${head} {}`);
      else {
        out.push(head);
        for (const [k, x] of entries) write(k, x, `${ind}  `);
      }
    } else {
      out.push(`${head} ${JSON.stringify(v)}`);
    }
  };
  for (const [k, v] of Object.entries(def)) write(k, v, "");
  return out;
}

export type DiffLine = { op: "same" | "add" | "del"; text: string };

/** A line diff of a to b (the longest common subsequence). */
export function diffLines(a: string[], b: string[]): DiffLine[] {
  const n = a.length;
  const m = b.length;
  const w = m + 1;
  // lcs[i*w+j]: the longest common subsequence of a[i:] and b[j:].
  const lcs = new Int32Array((n + 1) * w);
  for (let i = n - 1; i >= 0; i--) {
    for (let j = m - 1; j >= 0; j--) {
      lcs[i * w + j] =
        a[i] === b[j]
          ? (lcs[(i + 1) * w + j + 1] ?? 0) + 1
          : Math.max(lcs[(i + 1) * w + j] ?? 0, lcs[i * w + j + 1] ?? 0);
    }
  }
  const out: DiffLine[] = [];
  let i = 0;
  let j = 0;
  while (i < n || j < m) {
    if (i < n && j < m && a[i] === b[j]) {
      out.push({ op: "same", text: a[i] ?? "" });
      i++;
      j++;
    } else if (i < n && (j === m || (lcs[(i + 1) * w + j] ?? 0) >= (lcs[i * w + j + 1] ?? 0))) {
      out.push({ op: "del", text: a[i] ?? "" }); // what went, before what came
      i++;
    } else {
      out.push({ op: "add", text: b[j] ?? "" });
      j++;
    }
  }
  return out;
}

/** How many lines differ between two definitions. */
export function changedLines(a: Record<string, unknown>, b: Record<string, unknown>): number {
  return diffLines(definitionText(a), definitionText(b)).filter((l) => l.op !== "same").length;
}
