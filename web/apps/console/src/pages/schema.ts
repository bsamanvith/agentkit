// A tool's JSON Schema as a field list people can read (docs/UI.md
// "Toolsets": name, type, required, help), the way Swagger UI and Redoc show
// a request body: one row per property, nested objects and arrays of
// objects indented under their parent. The raw schema is shown beside it,
// so this only needs to be readable, not complete.

export type Field = {
  /** The property's path: "filters.domains", "results[].url". */
  path: string;
  /** Its last part, "domains". */
  name: string;
  /** 0 for a top-level property. */
  depth: number;
  /** "string", "array of string", "integer or null", "one of: fast, deep". */
  type: string;
  required: boolean;
  /** The description, then the default and limits. */
  help: string;
};

type Schema = Record<string, unknown>;

const isSchema = (v: unknown): v is Schema => typeof v === "object" && v !== null && !Array.isArray(v);

/** Nesting deeper than this is in the raw schema only. */
const maxDepth = 4;

/** Resolves a local "#/$defs/x" (or "#/definitions/x") against root, once per path. */
function resolve(s: Schema, root: Schema, seen: ReadonlySet<string>): { schema: Schema; ref?: string } {
  const ref = s.$ref;
  if (typeof ref !== "string" || !ref.startsWith("#/") || seen.has(ref)) return { schema: s };
  let at: unknown = root;
  for (const part of ref.slice(2).split("/")) {
    if (!isSchema(at)) return { schema: s };
    at = at[part.replace(/~1/g, "/").replace(/~0/g, "~")];
  }
  return isSchema(at) ? { schema: { ...at, ...s, $ref: undefined }, ref } : { schema: s };
}

const show = (v: unknown) => (typeof v === "string" ? v : JSON.stringify(v));

/** A schema's type in words. */
export function typeOf(s: Schema, root: Schema = s, seen: ReadonlySet<string> = new Set()): string {
  const { schema, ref } = resolve(s, root, seen);
  const next = ref ? new Set([...seen, ref]) : seen;
  if ("const" in schema) return `always ${show(schema.const)}`;
  if (Array.isArray(schema.enum)) return `one of: ${schema.enum.map(show).join(", ")}`;
  const alts = (Array.isArray(schema.anyOf) ? schema.anyOf : Array.isArray(schema.oneOf) ? schema.oneOf : []).filter(
    isSchema,
  );
  if (alts.length > 0) return [...new Set(alts.map((a) => typeOf(a, root, next)))].join(" or ");
  const types = Array.isArray(schema.type)
    ? schema.type.filter((t): t is string => typeof t === "string")
    : typeof schema.type === "string"
      ? [schema.type]
      : [];
  if (types.length === 0) {
    if (isSchema(schema.properties)) types.push("object");
    else if (typeof schema.$ref === "string") return schema.$ref.split("/").at(-1) ?? "any";
    else return "any";
  }
  const words = types
    .filter((t) => t !== "null")
    .map((t) => {
      if (t === "array" && isSchema(schema.items)) return `array of ${typeOf(schema.items, root, next)}`;
      if (typeof schema.format === "string") return `${t} (${schema.format})`;
      return t;
    });
  if (types.includes("null")) words.push("null");
  return words.join(" or ") || "null";
}

/** The description, then the default and the limits, as one line. */
function helpOf(s: Schema): string {
  const parts: string[] = [];
  if (typeof s.description === "string" && s.description.trim()) parts.push(s.description.trim());
  if ("default" in s) parts.push(`Default: ${show(s.default)}.`);
  const range = (lo: string, hi: string, unit: string) => {
    const min = typeof s[lo] === "number" ? s[lo] : undefined;
    const max = typeof s[hi] === "number" ? s[hi] : undefined;
    if (min !== undefined && max !== undefined) parts.push(`${min} to ${max}${unit}.`);
    else if (min !== undefined) parts.push(`At least ${min}${unit}.`);
    else if (max !== undefined) parts.push(`At most ${max}${unit}.`);
  };
  range("minimum", "maximum", "");
  range("minLength", "maxLength", " characters");
  range("minItems", "maxItems", " items");
  return parts.join(" ");
}

/** The object schema whose properties a field's children are: its own, or its array items'. */
function childrenOf(s: Schema, root: Schema, seen: ReadonlySet<string>): { schema: Schema; suffix: string } | null {
  const { schema } = resolve(s, root, seen);
  if (isSchema(schema.properties)) return { schema, suffix: "" };
  if (isSchema(schema.items)) {
    const { schema: items } = resolve(schema.items, root, seen);
    if (isSchema(items.properties)) return { schema: items, suffix: "[]" };
  }
  return null;
}

/**
 * The fields of an object schema, in its property order, each followed by
 * its own fields. Anything that isn't an object schema with properties has
 * no fields (an empty input, or a schema to read raw).
 */
export function fields(schema: unknown): Field[] {
  if (!isSchema(schema)) return [];
  const out: Field[] = [];
  const walk = (s: Schema, prefix: string, depth: number, seen: ReadonlySet<string>) => {
    const { schema: obj, ref } = resolve(s, schema, seen);
    const here = ref ? new Set([...seen, ref]) : seen;
    if (!isSchema(obj.properties)) return;
    const required = new Set(Array.isArray(obj.required) ? obj.required.filter((r) => typeof r === "string") : []);
    for (const [name, raw] of Object.entries(obj.properties)) {
      if (!isSchema(raw)) continue;
      const path = prefix + name;
      const resolved = resolve(raw, schema, here);
      const seenHere = resolved.ref ? new Set([...here, resolved.ref]) : here;
      out.push({
        path,
        name,
        depth,
        type: typeOf(raw, schema, here),
        required: required.has(name),
        help: helpOf(resolved.schema),
      });
      const kids = depth + 1 < maxDepth ? childrenOf(raw, schema, here) : null;
      if (kids) walk(kids.schema, `${path}${kids.suffix}.`, depth + 1, seenHere);
    }
  };
  walk(schema, "", 0, new Set());
  return out;
}
