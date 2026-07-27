// Service worker: receive push messages and show notifications.

self.addEventListener("install", () => self.skipWaiting());
self.addEventListener("activate", (e) => e.waitUntil(self.clients.claim()));

self.addEventListener("push", (event) => {
  let payload = {};
  try {
    payload = event.data ? event.data.json() : {};
  } catch (_) {
    payload = { body: event.data ? event.data.text() : "" };
  }

  const acts = Array.isArray(payload.actions) ? payload.actions.slice(0, 2) : [];
  const title = payload.title || "Notification";
  const options = {
    body: payload.body || "",
    icon: "/icon.png",
    badge: "/icon.png",
    tag: payload.tag || undefined,
    image: payload.image || undefined,
    actions: acts.map((a, i) => ({ action: String(i), title: a.title })),
    data: {
      url: payload.url || "/",
      actionUrls: acts.map((a) => a.url),
    },
  };
  event.waitUntil(self.registration.showNotification(title, options));
});

self.addEventListener("notificationclick", (event) => {
  event.notification.close();
  const data = event.notification.data || {};
  let url = data.url || "/";
  if (event.action !== "" && Array.isArray(data.actionUrls)) {
    const i = Number(event.action);
    if (data.actionUrls[i]) url = data.actionUrls[i];
  }
  // client.url from clients.matchAll() is always absolute, while `url` here
  // is typically a relative path (e.g. "/", "/a"). Resolve against the SW's
  // own origin so the comparison below can actually match an open tab.
  const target = new URL(url, self.location.origin).href;
  event.waitUntil(
    self.clients.matchAll({ type: "window", includeUncontrolled: true }).then((clients) => {
      for (const client of clients) {
        if ("focus" in client && client.url === target) return client.focus();
      }
      return self.clients.openWindow(url);
    })
  );
});
