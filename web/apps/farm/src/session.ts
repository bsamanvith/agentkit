/**
 * The signed-in session, when the server has sign-in: the CSRF token every
 * change sends, and the farm (holding) the app acts for. Without sign-in
 * both stay empty and requests carry neither.
 */
export const session = { csrf: "", holding: "" };

/** The headers a request with method sends for the session. */
export function sessionHeaders(method: string): Record<string, string> {
  const h: Record<string, string> = {};
  if (session.holding) h["X-Farm-Holding"] = session.holding;
  if (session.csrf && method !== "GET" && method !== "HEAD") h["X-CSRF-Token"] = session.csrf;
  return h;
}

/** Tells the app the session ended (a 401 from the API), so it shows sign-in. */
export function signedOut(status: number) {
  if (status === 401 && session.csrf) {
    session.csrf = "";
    window.dispatchEvent(new Event("farm:signed-out"));
  }
}

/** A link to an API path that keeps acting for the session's farm. */
export function withHolding(path: string): string {
  if (!session.holding) return path;
  return `${path}${path.includes("?") ? "&" : "?"}holding=${encodeURIComponent(session.holding)}`;
}

/** Where the farm the app last acted for is remembered on this phone. */
const holdingKey = "farm.holding";

/** The farm this phone last acted for, if any. */
export function rememberedHolding(): string {
  try {
    return localStorage.getItem(holdingKey) ?? "";
  } catch {
    return "";
  }
}

/** Remembers the farm to act for, and opens the app on it. */
export function openFarm(holding: string) {
  try {
    localStorage.setItem(holdingKey, holding);
  } catch {
    // Private mode: the app opens on the first farm next time.
  }
  window.location.assign("/");
}
