import type { PushSubscriptionJSON } from "./types";

/** Why this browser cannot take pushes, or "" when it can. */
export function pushUnsupported(): string {
  if (typeof window === "undefined" || !("serviceWorker" in navigator) || !("PushManager" in window))
    return "This browser cannot show alerts from the app. On a phone, use Chrome, or add the app to the home screen.";
  if (!window.isSecureContext) return "Alerts on this phone need the app on https.";
  return "";
}

/** The server's VAPID key (base64url) as the bytes PushManager wants. */
export function keyBytes(b64url: string): Uint8Array<ArrayBuffer> {
  const b64 = b64url.replace(/-/g, "+").replace(/_/g, "/") + "=".repeat((4 - (b64url.length % 4)) % 4);
  const raw = atob(b64);
  const out = new Uint8Array(new ArrayBuffer(raw.length));
  for (let i = 0; i < raw.length; i++) out[i] = raw.charCodeAt(i);
  return out;
}

/**
 * Subscribes this browser to the server's pushes: registers the service
 * worker, asks the person, and returns the subscription for the server.
 */
export async function subscribePush(serverKey: string): Promise<PushSubscriptionJSON> {
  const why = pushUnsupported();
  if (why) throw new Error(why);
  if ((await Notification.requestPermission()) !== "granted")
    throw new Error("Alerts are blocked for this site. Allow notifications in the browser's site settings.");
  const reg = await navigator.serviceWorker.register("/sw.js");
  await navigator.serviceWorker.ready;
  const key = keyBytes(serverKey);
  let sub = await reg.pushManager.getSubscription();
  if (sub && !sameKey(sub.options.applicationServerKey, key)) {
    await sub.unsubscribe(); // made for another server key
    sub = null;
  }
  try {
    sub ??= await reg.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: key });
  } catch (e) {
    const why = e instanceof Error ? e.message : String(e);
    throw new Error(`This browser would not take app alerts (${why}). On a phone, try Chrome.`);
  }
  const j = sub.toJSON();
  if (!j.endpoint || !j.keys?.p256dh || !j.keys.auth) throw new Error("The browser gave an incomplete subscription.");
  return { endpoint: j.endpoint, keys: { p256dh: j.keys.p256dh, auth: j.keys.auth } };
}

/** This browser's current push endpoint, if it has one. */
export async function currentPush(): Promise<string | undefined> {
  if (pushUnsupported()) return undefined;
  const reg = await navigator.serviceWorker.getRegistration("/");
  return (await reg?.pushManager.getSubscription())?.endpoint;
}

function sameKey(a: ArrayBuffer | null | undefined, b: Uint8Array): boolean {
  if (!a) return false;
  const x = new Uint8Array(a);
  return x.length === b.length && x.every((v, i) => v === b[i]);
}
