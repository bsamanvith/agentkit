// The farm app's service worker: it shows the alerts the server pushes
// (apps/farm/notify/webpush, Payload) and opens the plot when one is
// tapped. It caches nothing; the app is online-first for now.
"use strict";

self.addEventListener("install", () => self.skipWaiting());
self.addEventListener("activate", (event) => event.waitUntil(self.clients.claim()));

self.addEventListener("push", (event) => {
  let p;
  try {
    p = event.data ? event.data.json() : {};
  } catch {
    p = { title: "Farm alert", body: event.data ? event.data.text() : "" };
  }
  const urgent = p.severity === "urgent";
  event.waitUntil(
    self.registration.showNotification(p.title || "Farm alert", {
      body: p.body || "",
      // One notification per message: a resend replaces it.
      tag: p.id || undefined,
      icon: "/favicon.svg",
      badge: "/favicon.svg",
      requireInteraction: urgent,
      renotify: urgent && !!p.id,
      data: { link: p.link || "", farm: p.farm || "" },
    }),
  );
});

self.addEventListener("notificationclick", (event) => {
  event.notification.close();
  const d = event.notification.data || {};
  // Only this app's pages: a link elsewhere opens the plot instead.
  let url = d.farm ? "/?farm=" + encodeURIComponent(d.farm) : "/";
  try {
    const u = new URL(d.link || "", self.location.origin);
    if (d.link && u.origin === self.location.origin) url = u.pathname + u.search;
  } catch {
    // keep the plot's URL
  }
  event.waitUntil(
    self.clients.matchAll({ type: "window", includeUncontrolled: true }).then((wins) => {
      for (const w of wins) {
        if (new URL(w.url).origin === self.location.origin && "navigate" in w) {
          return w.navigate(url).then((c) => (c || w).focus());
        }
      }
      return self.clients.openWindow(url);
    }),
  );
});
