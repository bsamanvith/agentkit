// Dynamic values of A2UI v0.9: a literal, a data binding {"path"} (absolute,
// or relative to a template's item) or a call of one of the basic catalog's
// functions {"call", "args"}. Functions here only compute values; openUrl,
// the one with an effect, is the renderer's (surface.tsx), which checks its
// URL first.

import { getAt, joinPath } from "./model";

/** Where values are read: the data model (with the person's edits) and a template item's path. */
export type Scope = { data: unknown; base: string };

function isObject(v: unknown): v is Record<string, unknown> {
  return typeof v === "object" && v !== null && !Array.isArray(v);
}

/** The value of a dynamic value, or undefined. */
export function resolve(v: unknown, scope: Scope): unknown {
  if (Array.isArray(v)) return v.map((item) => resolve(item, scope));
  if (!isObject(v)) return v;
  if (typeof v.call === "string") return call(v.call, isObject(v.args) ? v.args : {}, scope);
  if (typeof v.path === "string") return getAt(scope.data, bindingPath(v.path, scope));
  return v;
}

/** The absolute path of a binding's path in a scope. */
export function bindingPath(path: string, scope: Scope): string {
  return joinPath(scope.base, path);
}

/** The data model path a value is bound to, or undefined when it isn't a binding. */
export function boundPath(v: unknown, scope: Scope): string | undefined {
  return isObject(v) && typeof v.path === "string" && v.call === undefined ? bindingPath(v.path, scope) : undefined;
}

/** A value as text: strings as they are, numbers and booleans written out, anything else empty. */
export function asText(v: unknown): string {
  if (typeof v === "string") return v;
  if (typeof v === "number" || typeof v === "boolean") return String(v);
  return "";
}

export const text = (v: unknown, scope: Scope) => asText(resolve(v, scope));

export function bool(v: unknown, scope: Scope): boolean {
  return resolve(v, scope) === true;
}

export function num(v: unknown, scope: Scope): number | undefined {
  const r = resolve(v, scope);
  const n = typeof r === "number" ? r : typeof r === "string" && r.trim() !== "" ? Number(r) : NaN;
  return Number.isFinite(n) ? n : undefined;
}

export function list(v: unknown, scope: Scope): string[] {
  const r = resolve(v, scope);
  return Array.isArray(r) ? r.filter((x): x is string => typeof x === "string") : [];
}

const empty = (v: unknown) => v === undefined || v === null || v === "" || (Array.isArray(v) && v.length === 0);

/** Calls one of the basic catalog's functions; an unknown one is undefined. */
export function call(name: string, args: Record<string, unknown>, scope: Scope): unknown {
  const arg = (k: string) => resolve(args[k], scope);
  const n = (k: string) => num(args[k], scope);
  switch (name) {
    case "required":
      return !empty(arg("value"));
    case "regex": {
      try {
        return new RegExp(asText(args.pattern)).test(asText(arg("value")));
      } catch {
        return false;
      }
    }
    case "length": {
      const len = [...asText(arg("value"))].length;
      const min = n("min");
      const max = n("max");
      return (min === undefined || len >= min) && (max === undefined || len <= max);
    }
    case "numeric": {
      const v = n("value");
      const min = n("min");
      const max = n("max");
      return v !== undefined && (min === undefined || v >= min) && (max === undefined || v <= max);
    }
    case "email":
      return /^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(asText(arg("value")));
    case "and": {
      const vs = Array.isArray(args.values) ? args.values : [];
      return vs.length > 0 && vs.every((x) => resolve(x, scope) === true);
    }
    case "or": {
      const vs = Array.isArray(args.values) ? args.values : [];
      return vs.some((x) => resolve(x, scope) === true);
    }
    case "not":
      return arg("value") !== true;
    case "formatString":
      return interpolate(asText(arg("value")), scope);
    case "formatNumber":
    case "formatCurrency": {
      const v = n("value");
      if (v === undefined) return "";
      const decimals = n("decimals");
      const grouping = arg("grouping");
      try {
        return new Intl.NumberFormat(undefined, {
          ...(name === "formatCurrency" ? { style: "currency", currency: asText(arg("currency")) } : {}),
          ...(decimals !== undefined
            ? { minimumFractionDigits: clampDigits(decimals), maximumFractionDigits: clampDigits(decimals) }
            : {}),
          useGrouping: grouping !== false,
        }).format(v);
      } catch {
        return String(v);
      }
    }
    case "formatDate":
      return formatDate(arg("value"), asText(arg("format")));
    case "pluralize": {
      const v = n("value") ?? 0;
      if (v === 0 && typeof args.zero !== "undefined") return asText(arg("zero"));
      const category = new Intl.PluralRules().select(v);
      return asText(args[category] !== undefined ? arg(category) : arg("other"));
    }
  }
  return undefined;
}

const clampDigits = (d: number) => Math.min(20, Math.max(0, Math.trunc(d)));

/** formatString: "${/path}" or "${relative}" replaced by the value there; "\${" is a literal "${". */
export function interpolate(s: string, scope: Scope): string {
  return s.replace(/\\\$\{|\$\{([^{}]*)\}/g, (m, path: string | undefined) => {
    if (path === undefined) return "${";
    const p = path.trim();
    return /^[^\s()'",:]*$/.test(p) ? asText(getAt(scope.data, joinPath(scope.base, p))) : m;
  });
}

const months = [
  "January",
  "February",
  "March",
  "April",
  "May",
  "June",
  "July",
  "August",
  "September",
  "October",
  "November",
  "December",
];
const days = ["Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"];

/** formatDate with a Unicode (TR35) pattern's common fields: y M d E H h m s a and 'quoted text'. */
export function formatDate(value: unknown, pattern: string): string {
  // A date alone ("2026-02-10") is that day here: new Date reads it as UTC midnight.
  const day = typeof value === "string" ? /^(\d{4})-(\d{2})-(\d{2})$/.exec(value) : null;
  const d = day
    ? new Date(Number(day[1]), Number(day[2]) - 1, Number(day[3]))
    : typeof value === "string" || typeof value === "number"
      ? new Date(value)
      : undefined;
  if (!d || Number.isNaN(d.getTime())) return asText(value);
  const pad = (x: number, w: number) => String(x).padStart(w, "0");
  return pattern.replace(/'([^']*)'|y+|Y+|M+|d+|E+|H+|h+|m+|s+|a+/g, (tok, quoted: string | undefined) => {
    if (quoted !== undefined) return quoted === "" ? "'" : quoted;
    const w = tok.length;
    switch (tok[0]) {
      case "y":
      case "Y":
        return w === 2 ? pad(d.getFullYear() % 100, 2) : String(d.getFullYear());
      case "M":
        return w >= 4
          ? (months[d.getMonth()] ?? "")
          : w === 3
            ? (months[d.getMonth()] ?? "").slice(0, 3)
            : pad(d.getMonth() + 1, w);
      case "d":
        return pad(d.getDate(), w);
      case "E":
        return w >= 4 ? (days[d.getDay()] ?? "") : (days[d.getDay()] ?? "").slice(0, 3);
      case "H":
        return pad(d.getHours(), w);
      case "h":
        return pad(d.getHours() % 12 || 12, w);
      case "m":
        return pad(d.getMinutes(), w);
      case "s":
        return pad(d.getSeconds(), w);
      default:
        return d.getHours() < 12 ? "AM" : "PM";
    }
  });
}

/** The messages of a component's failing checks ({condition, message} or the call shorthand). */
export function failingChecks(checks: unknown, scope: Scope): string[] {
  if (!Array.isArray(checks)) return [];
  const out: string[] = [];
  for (const c of checks) {
    if (!isObject(c)) continue;
    const ok =
      typeof c.call === "string" ? resolve({ call: c.call, args: c.args }, scope) : resolve(c.condition, scope);
    if (ok !== true && typeof c.message === "string") out.push(c.message);
  }
  return out;
}

/** An action event's context with its bindings and calls resolved: the values sent to the agent. */
export function resolveContext(context: unknown, scope: Scope): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  if (!isObject(context)) return out;
  for (const [k, v] of Object.entries(context)) {
    const r = resolve(v, scope);
    out[k] = r === undefined ? null : r;
  }
  return out;
}
