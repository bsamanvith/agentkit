import type { Corner, Location } from "./types";

export interface Parsed {
  corners: Corner[];
  /** Lines that could not be read, 1-based, with why. */
  errors: { line: number; text: string; why: string }[];
}

// One coordinate: decimal degrees ("17.3850", "-78.48") or degrees, minutes
// and seconds ("17°23'6\"N", "17 23 6 N"), with an optional hemisphere.
const dms =
  /^(-?\d+(?:\.\d+)?)\s*[°\s]\s*(?:(\d+(?:\.\d+)?)\s*['′\s]\s*(?:(\d+(?:\.\d+)?)\s*(?:["″]|'')?)?)?\s*([NSEW])?$/i;
const decimal = /^(-?\d+(?:\.\d+)?)\s*°?\s*([NSEW])?$/i;

function coordinate(raw: string): { value: number; hemi?: string } | undefined {
  const s = raw.trim();
  let m = decimal.exec(s);
  if (m) {
    const v = Number(m[1]);
    return { value: m[2] && /[SW]/i.test(m[2]) ? -Math.abs(v) : v, hemi: m[2]?.toUpperCase() };
  }
  m = dms.exec(s);
  if (m) {
    const deg = Number(m[1]);
    const v = Math.abs(deg) + Number(m[2] ?? 0) / 60 + Number(m[3] ?? 0) / 3600;
    const neg = deg < 0 || (m[4] !== undefined && /[SW]/i.test(m[4]));
    return { value: neg ? -v : v, hemi: m[4]?.toUpperCase() };
  }
  return undefined;
}

/**
 * Reads typed or pasted corners, one per line: "lat, lng", "lat lng" or
 * "lat; lng", in decimal degrees or degrees-minutes-seconds. A pair marked
 * E/W first and N/S second is read as lng, lat. Blank lines and lines
 * starting with # are skipped.
 */
export function parseCorners(text: string): Parsed {
  const out: Parsed = { corners: [], errors: [] };
  text.split(/\r?\n/).forEach((line, i) => {
    const t = line.trim();
    if (t === "" || t.startsWith("#")) return;
    let parts = t.split(/\s*[,;\t]\s*/).filter((p) => p !== "");
    if (parts.length === 1) {
      // "17.385 78.4867": split on the space between two numbers.
      parts = /^(\S+)\s+(\S+)$/.exec(t)?.slice(1) ?? parts;
    }
    const [first, second] = parts;
    if (parts.length !== 2 || first === undefined || second === undefined) {
      out.errors.push({ line: i + 1, text: t, why: "want two numbers: latitude, longitude" });
      return;
    }
    const a = coordinate(first);
    const b = coordinate(second);
    if (!a || !b) {
      out.errors.push({ line: i + 1, text: t, why: "not a coordinate" });
      return;
    }
    let lat = a.value;
    let lng = b.value;
    if ((a.hemi === "E" || a.hemi === "W") && (b.hemi === "N" || b.hemi === "S")) [lat, lng] = [lng, lat];
    if (Math.abs(lat) > 90 || Math.abs(lng) > 180) {
      out.errors.push({ line: i + 1, text: t, why: "out of range: latitude within ±90, longitude within ±180" });
      return;
    }
    out.corners.push({ lat, lng });
  });
  return out;
}

/** Writes corners back as text, one "lat, lng" per line, as parseCorners reads them. */
export function formatCorners(corners: Corner[]): string {
  return corners.map((c) => `${c.lat.toFixed(6)}, ${c.lng.toFixed(6)}`).join("\n");
}

/** Formats a number for people: at most `digits` decimals, grouped thousands. */
export function num(n: number, digits = 2): string {
  return n.toLocaleString("en-IN", { maximumFractionDigits: digits });
}

/** Names where a plot is, from the village up: "Nampally, Hyderabad, Telangana, India". */
export function place(l?: Location): string {
  if (!l) return "";
  const parts: string[] = [];
  for (const s of [l.village, l.sub_district, l.district, l.state, l.country]) {
    if (s && parts[parts.length - 1] !== s) parts.push(s);
  }
  return parts.length > 0 ? parts.join(", ") : (l.display_name ?? "");
}
