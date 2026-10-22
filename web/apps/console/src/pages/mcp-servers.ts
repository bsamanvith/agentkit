// The pure parts of adding and reviewing MCP servers (docs/UI.md "Adding an
// MCP server", T096): what the add form checks before any request, how a
// tool's text is shown (invisible characters made visible, flags), the
// diffs of the review page, and what an API error means for the form.
//
// Everything a server sends (names, titles, descriptions, schemas) is
// hostile input: these functions return plain strings, which the pages
// render as text, never as markup or links.

import {
  ApiError,
  NetworkError,
  type ReviewTool,
  type ToolDecision,
  type ToolDefinition,
} from "@agentkit/agent-ui/api";

/** A toolset name, as POST /v1/toolsets takes it. */
export const namePattern = /^[a-zA-Z0-9_-]{1,64}$/;

/**
 * What is wrong with a URL before it is sent: a reason, or "" when the
 * server should decide. Only what the server always refuses is refused
 * here. Plain http and private addresses work for hosts the operator allows
 * (runtime_toolsets.allow_hosts), which the console can't know, so they get
 * a warning (urlWarning) and the server's answer.
 */
export function urlProblem(raw: string): string {
  const s = raw.trim();
  if (!s) return "Paste the server's URL.";
  let u: URL;
  try {
    u = new URL(s);
  } catch {
    return "That isn't a URL. It looks like https://mcp.example.com/mcp.";
  }
  if (u.protocol !== "https:" && u.protocol !== "http:") return "Use an https URL.";
  if (u.username || u.password) {
    return "Take the user name and password out of the URL. A credential goes in a header, from a server variable.";
  }
  if (!u.hostname) return "That URL has no host.";
  const variable = /\{[^}]*\}/.exec(s);
  if (variable) return `Fill in ${variable[0]} in the URL first.`;
  return "";
}

/**
 * Whether url is template (a registry remote's URL) with each {variable}
 * replaced by anything, even nothing while it is being typed over (the
 * server and urlProblem judge the URL itself). Matched piece by piece,
 * never as a regular expression built from the registry's text.
 */
export function fillsTemplate(template: string, url: string): boolean {
  const pieces = template.split(/\{[^{}]*\}/);
  const s = url.trim();
  const first = pieces[0] ?? "";
  const last = pieces.at(-1) ?? "";
  if (pieces.length === 1) return s === template;
  if (!s.startsWith(first) || !s.endsWith(last) || s.length < first.length + last.length) return false;
  let at = first.length;
  for (const piece of pieces.slice(1, -1)) {
    const found = s.indexOf(piece, at);
    if (found < 0) return false;
    at = found + piece.length;
  }
  return at <= s.length - last.length;
}

/** A warning for a URL the server refuses unless the operator allows its host; "" otherwise. */
export function urlWarning(raw: string): string {
  let u: URL;
  try {
    u = new URL(raw.trim());
  } catch {
    return "";
  }
  const host = u.hostname.replace(/^\[|\]$/g, "");
  if (isPrivateHost(host)) {
    return `${host} is a private or local address. agentkit connects only to public addresses, unless the operator allows this host.`;
  }
  if (u.protocol === "http:") return "Plain http works only for a host the operator allows. Use https.";
  return "";
}

/** Whether a host is a loopback, private, link-local or CGNAT address, or a local name. */
export function isPrivateHost(host: string): boolean {
  const h = host.toLowerCase();
  if (h === "localhost" || h.endsWith(".localhost") || h.endsWith(".local") || h.endsWith(".internal")) return true;
  if (h.includes(":")) {
    // IPv6: loopback, unspecified, ULA, link-local, and IPv4-mapped ones judged by their IPv4.
    if (h === "::1" || h === "::") return true;
    const mapped = /^::ffff:(\d+\.\d+\.\d+\.\d+)$/.exec(h);
    if (mapped?.[1]) return isPrivateHost(mapped[1]);
    if (/^::ffff:[0-9a-f]{1,4}:[0-9a-f]{1,4}$/.test(h)) return true; // the URL parser's hex form of a mapped one
    return /^f[cd]/.test(h) || /^fe[89ab]/.test(h);
  }
  const m = /^(\d+)\.(\d+)\.(\d+)\.(\d+)$/.exec(h);
  if (!m) return false;
  const [a, b] = [Number(m[1]), Number(m[2])];
  return (
    a === 0 ||
    a === 10 ||
    a === 127 ||
    (a === 169 && b === 254) ||
    (a === 172 && b >= 16 && b <= 31) ||
    (a === 192 && b === 168) ||
    (a === 100 && b >= 64 && b <= 127)
  );
}

/** A name for the toolset from the URL's host: mcp.linear.app is "linear". */
export function nameFromURL(raw: string): string {
  let host: string;
  try {
    host = new URL(raw.trim()).hostname;
  } catch {
    return "";
  }
  if (/^[\d.]+$/.test(host) || host.includes(":")) return "";
  const labels = host.split(".").filter((l) => l && !["mcp", "www", "api", "server"].includes(l));
  const word = (labels.length > 1 ? labels.slice(0, -1) : labels)[0] ?? "";
  return word
    .toLowerCase()
    .replace(/[^a-z0-9_-]/g, "")
    .slice(0, 64);
}

/** What is wrong with a toolset name: a reason, or "". */
export function nameProblem(name: string, taken: string[]): string {
  if (!name) return "Give it a name.";
  if (!namePattern.test(name)) return "Use letters, digits, - and _ only, at most 64.";
  if (name === "new") return "new is taken by the console's own page. Pick another name.";
  if (taken.includes(name)) return `A toolset called ${name} already exists.`;
  return "";
}

/** A header name that carries a secret: its value must come from a server variable. */
export function secretHeader(name: string): boolean {
  return /authorization|cookie|key|token|secret|password|auth|session|signature/i.test(name);
}

/** The ${NAME} references in a header value. */
export function headerVariables(value: string): string[] {
  return [...value.matchAll(/\$\{([A-Za-z_][A-Za-z0-9_]*)\}/g)].map((m) => m[1] ?? "");
}

/**
 * What is wrong with a header before it is sent: a reason, or "". A value
 * for a header that carries a secret must be a ${VAR} reference, so a key
 * pasted by mistake never leaves the browser. The server checks the
 * variables against runtime_toolsets.header_env.
 */
export function headerProblem(name: string, value: string): string {
  if (!name.trim()) return "Name the header.";
  if (!/^[!#$%&'*+.^_`|~0-9A-Za-z-]+$/.test(name.trim())) return "A header name has letters, digits and - only.";
  if (!value.trim()) return "Give the header a value.";
  if ((secretHeader(name) && headerVariables(value).length === 0) || literalSecret(value)) {
    return "Use a server variable, like Bearer ${API_KEY}. The value itself never goes through the console.";
  }
  return "";
}

/**
 * Whether a header value, outside its ${VAR}s, looks like a credential
 * whatever the header's name: a scheme with a literal ("Bearer sk-…",
 * "Bearer ${A}x"), or a long token of letters and digits that isn't a
 * UUID. The server checks only names, so this is what keeps a key pasted
 * under an innocent name (X-Api) from being sent and stored.
 */
export function literalSecret(value: string): boolean {
  const rest = value.replace(/\$\{[A-Za-z_][A-Za-z0-9_]*\}/g, " ");
  if (/^\s*(bearer|basic|token|digest)\s+\S/i.test(rest)) return true;
  return (rest.match(/[A-Za-z0-9_\-+/=.~]{20,}/g) ?? []).some(
    (t) => /[0-9]/.test(t) && /[A-Za-z]/.test(t) && !/^[0-9a-f]{8}(-[0-9a-f]{4}){3}-[0-9a-f]{12}$/i.test(t),
  );
}

/**
 * Whether a character renders as nothing, or changes how the text around it
 * reads: as pkg/mcp/client's SanitizeDescription removes them (controls but
 * newline and tab, format characters, variation selectors, fillers).
 */
function hiddenChar(c: string): boolean {
  if (c === "\n" || c === "\t") return false;
  if (/[\p{Cc}\p{Cf}]/u.test(c)) return true;
  const n = c.codePointAt(0) ?? 0;
  return (
    fillers.includes(n) || (n >= 0xfe00 && n <= 0xfe0f) || (n >= 0xe0100 && n <= 0xe01ef) // variation selectors
  );
}

// Fillers, the grapheme joiner and the Mongolian vowel separator, which render as nothing.
const fillers = [0x034f, 0x115f, 0x1160, 0x180e, 0x3164, 0xffa0];

const mark = (c: string) => `⟦U+${(c.codePointAt(0) ?? 0).toString(16).toUpperCase().padStart(4, "0")}⟧`;

/** The text with each invisible character shown as ⟦U+200B⟧. */
export function visible(s: string): string {
  let out = "";
  for (const c of s) out += hiddenChar(c) ? mark(c) : c;
  return out;
}

/** How many invisible characters the text has. */
export function hiddenCount(s: string): number {
  let n = 0;
  for (const c of s) if (hiddenChar(c)) n++;
  return n;
}

/** A tool name split into its words, for namedIn. */
type ToolName = { name: string; parts: string[]; word: boolean };

const toolNames = (names: string[]): ToolName[] =>
  names.map((name) => {
    const parts = name.match(/[A-Za-z0-9_]+/g) ?? [];
    return { name, parts, word: parts.length === 1 && parts[0] === name };
  });

/**
 * Why a tool needs a closer look (docs/UI.md "Review tools"), as sentences:
 * invisible characters, telling the model to call another tool or to act
 * on its instructions, a description cut short. others are the other tools
 * of the server. For every tool of a review, reviewFlags does the same once.
 */
export function toolFlags(t: ReviewTool, others: string[]): string[] {
  return flagsOf(t, toolNames(others));
}

/** toolFlags of every tool, by name: linear in the tools' text, whatever their number. */
export function reviewFlags(tools: ReviewTool[]): Map<string, string[]> {
  const names = toolNames(tools.map((t) => t.name));
  return new Map(tools.map((t) => [t.name, flagsOf(t, names)]));
}

function flagsOf(t: ReviewTool, names: ToolName[]): string[] {
  const raw = t.current?.description ?? "";
  const shown = t.description ?? "";
  const flags: string[] = [];
  const hidden = hiddenCount(raw) + hiddenCount(t.current?.title ?? "");
  if (hidden > 0) {
    flags.push(
      `${hidden} invisible ${hidden === 1 ? "character" : "characters"}, shown as ⟦U+…⟧. agentkit removes them before the model reads it.`,
    );
  }
  const text = `${t.current?.title ?? ""}\n${shown}`;
  const named = namedIn(
    text,
    names.filter((n) => n.name !== t.name),
  );
  if (named.length > 0 || /\b(call|invoke|run|use)\b[^.\n]{0,40}\btools?\b/i.test(text)) {
    flags.push(`Tells the model to call another tool${named.length ? ` (${named.join(", ")})` : ""}.`);
  }
  if (/\b(ignore|disregard|forget)\b[^.\n]{0,30}\b(instructions|rules|prompt)\b|\bsystem prompt\b/i.test(text)) {
    flags.push("Talks to the model about its instructions.");
  }
  if (/…\[truncated \d+ characters\]$/.test(shown))
    flags.push("Its description is cut short: it is longer than the model is given.");
  return flags;
}

const wordChar = /[A-Za-z0-9_]/;

/**
 * The names that text mentions as whole words (not inside a longer word of
 * letters, digits and _). A server may list thousands of tools with long
 * titles, so this is not a search per name: a name that is one word is a
 * set lookup, and only a name with other characters (a-b, a.b) whose words
 * are all in the text is searched for.
 */
function namedIn(text: string, names: ToolName[]): string[] {
  const words = new Set(text.match(/[A-Za-z0-9_]+/g) ?? []);
  const found: string[] = [];
  for (const { name, parts, word } of names) {
    if (parts.length === 0 || !parts.every((p) => words.has(p))) continue;
    if (word) {
      found.push(name);
      continue;
    }
    for (let at = text.indexOf(name); at >= 0; at = text.indexOf(name, at + 1)) {
      if (!wordChar.test(text[at - 1] ?? "") && !wordChar.test(text[at + name.length] ?? "")) {
        found.push(name);
        break;
      }
    }
  }
  return found;
}

/** What the server claims about a tool (its annotations' hints that are true), never a check. */
export function serverClaims(def: ToolDefinition | undefined): string[] {
  const a = def?.annotations;
  if (!a || typeof a !== "object") return [];
  return ["readOnlyHint", "destructiveHint", "idempotentHint", "openWorldHint"].filter((h) => a[h] === true);
}

export type DiffPart = { op: "same" | "add" | "del"; text: string };

/**
 * The most cells diffTokens' table may have (4 bytes each). The table is
 * quadratic, and a server's text is hostile: past this the changed middle
 * is shown whole, removed then added, rather than freeze the page.
 */
export const maxDiffCells = 1_000_000;

/**
 * The difference between two lists of tokens, as runs of kept, removed and
 * added text: the common start and end, then a longest common subsequence
 * of the middle. A middle over maxDiffCells gives up on detail: all of a's
 * removed, all of b's added. Linear in time and memory but for that table.
 */
export function diffTokens(a: string[], b: string[]): DiffPart[] {
  const out: DiffPart[] = [];
  const push = (op: DiffPart["op"], text: string) => {
    if (!text) return;
    const last = out.at(-1);
    if (last?.op === op) last.text += text;
    else out.push({ op, text });
  };
  let start = 0;
  while (start < a.length && start < b.length && a[start] === b[start]) start++;
  let endA = a.length;
  let endB = b.length;
  while (endA > start && endB > start && a[endA - 1] === b[endB - 1]) {
    endA--;
    endB--;
  }
  push("same", a.slice(0, start).join(""));
  const x = a.slice(start, endA);
  const y = b.slice(start, endB);
  const n = x.length;
  const m = y.length;
  if (n === 0 || m === 0 || (n + 1) * (m + 1) > maxDiffCells) {
    push("del", x.join(""));
    push("add", y.join(""));
  } else {
    // lcs[i*w+j]: the longest common subsequence of x[i:] and y[j:].
    const w = m + 1;
    const lcs = new Uint32Array((n + 1) * w);
    for (let i = n - 1; i >= 0; i--) {
      for (let j = m - 1; j >= 0; j--) {
        lcs[i * w + j] =
          x[i] === y[j]
            ? (lcs[(i + 1) * w + j + 1] as number) + 1
            : Math.max(lcs[(i + 1) * w + j] as number, lcs[i * w + j + 1] as number);
      }
    }
    let i = 0;
    let j = 0;
    while (i < n || j < m) {
      if (i < n && j < m && x[i] === y[j]) {
        push("same", x[i] as string);
        i++;
        j++;
      } else if (i < n && (j === m || (lcs[(i + 1) * w + j] as number) >= (lcs[i * w + j + 1] as number))) {
        push("del", x[i] as string); // removals before additions, as a reader expects
        i++;
      } else {
        push("add", y[j] as string);
        j++;
      }
    }
  }
  push("same", a.slice(endA).join(""));
  return out;
}

/** Words and the space between them, so a diff of prose marks whole words. */
export const words = (s: string) => s.match(/\s+|[^\s]+/g) ?? [];

/** Lines, each with its newline, so a diff of JSON marks whole lines. */
export const lines = (s: string) => s.match(/[^\n]*\n|[^\n]+$/g) ?? [];

/** A definition's schema part as stable JSON text: sorted keys, two spaces. */
export function schemaText(def: ToolDefinition | undefined): string {
  if (!def) return "";
  const parts: Record<string, unknown> = {};
  if (def.title !== undefined) parts.title = def.title;
  if (def.input_schema !== undefined) parts.input_schema = def.input_schema;
  if (def.output_schema !== undefined) parts.output_schema = def.output_schema;
  if (def.annotations !== undefined) parts.annotations = def.annotations;
  return JSON.stringify(sorted(parts), null, 2);
}

function sorted(v: unknown): unknown {
  if (Array.isArray(v)) return v.map(sorted);
  if (v && typeof v === "object") {
    return Object.fromEntries(
      Object.keys(v as object)
        .sort()
        .map((k) => [k, sorted((v as Record<string, unknown>)[k])]),
    );
  }
  return v;
}

/** The tools a review asks about: new and changed ones, which are off until decided. */
export const needsDecision = (t: ReviewTool) => t.status === "new" || t.status === "changed";

/** The kinds and risks an admin may give a tool (E17). */
export const kinds = ["read", "write", "delete", "send", "execute"];
export const risks = ["low", "medium", "high"];

/** An admin's decision on one tool, with the class agentkit will use. */
export type Choice = { decision?: "approve" | "reject"; kind: string; risk: string };
export type Choices = Record<string, Choice>;

/** Undecided choices for every tool, in the class the API proposes (its annotations, or a medium-risk write). */
export function initialChoices(tools: ReviewTool[]): Choices {
  return Object.fromEntries(
    tools.map((t) => [
      t.name,
      { kind: kinds.includes(t.kind) ? t.kind : "write", risk: risks.includes(t.risk) ? t.risk : "medium" },
    ]),
  );
}

/** The decisions to send: one per decided tool, on the definition the review showed. */
export function decisions(tools: ReviewTool[], choices: Choices): ToolDecision[] {
  const out: ToolDecision[] = [];
  for (const t of tools) {
    const c = choices[t.name];
    if (!c?.decision) continue;
    if (c.decision === "approve") {
      if (!t.fingerprint) continue; // a removed tool can only be forgotten
      out.push({ name: t.name, fingerprint: t.fingerprint, decision: "approve", kind: c.kind, risk: c.risk });
    } else {
      out.push({ name: t.name, fingerprint: t.fingerprint ?? "", decision: "reject" });
    }
  }
  return out;
}

/** The tools still to decide on: new and changed ones without a decision. */
export const undecided = (tools: ReviewTool[], choices: Choices) =>
  tools.filter((t) => needsDecision(t) && !choices[t.name]?.decision);

/** What a server changed, for the review's notice: "changed 1 tool and added 1". */
export function changeWords(tools: ReviewTool[]): string {
  const changed = tools.filter((t) => t.status === "changed").length;
  const added = tools.filter((t) => t.status === "new").length;
  return [
    changed ? `changed ${changed} ${changed === 1 ? "tool" : "tools"}` : "",
    added ? `added ${added}${changed ? "" : added === 1 ? " tool" : " tools"}` : "",
  ]
    .filter(Boolean)
    .join(" and ");
}

/** An error of a review's save, as a sentence. */
export function reviewErrorText(e: unknown): string {
  if (e instanceof ApiError && e.status === 409) {
    return "The server changed a tool while you were reviewing it, so nothing was saved. The review below is what it lists now.";
  }
  if (e instanceof ApiError) return `The review wasn't saved: ${e.message} (${e.status} ${e.code}).`;
  return "The review wasn't saved: couldn't reach agentkit. Try again.";
}

/** Where an error of POST /v1/toolsets belongs on the add form, and what to say. */
export type FormError = {
  field: "url" | "name" | "headers" | "form";
  message: string;
  authRequired?: boolean;
  /** auth_required with auth "oauth": the server signs people in with MCP OAuth. */
  oauth?: boolean;
};

/** The add form's reading of an error from POST /v1/toolsets. */
export function addError(e: unknown): FormError {
  if (e instanceof NetworkError) return { field: "form", message: "Couldn't reach agentkit. Try again." };
  if (!(e instanceof ApiError)) return { field: "form", message: "Something went wrong. Try again." };
  const said = `${e.message} (${e.code})`;
  switch (e.code) {
    case "auth_required":
      return { field: "headers", message: said, authRequired: true, oauth: e.auth === "oauth" };
    case "toolset_exists":
      return { field: "name", message: said };
    case "url_not_allowed":
      return { field: "url", message: `This workspace doesn't allow adding servers at that URL. ${said}` };
    case "server_unreachable":
      return { field: "url", message: `agentkit couldn't use the server: ${said}` };
    case "forbidden":
      return { field: "name", message: said };
    case "not_found":
      return {
        field: "form",
        message: "This agentkit server doesn't add MCP servers at run time (runtime_toolsets is off).",
      };
  }
  if (e.status === 400) return { field: /header|\$\{/i.test(e.message) ? "headers" : "url", message: said };
  return { field: "form", message: said };
}
