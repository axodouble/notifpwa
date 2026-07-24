// Front-end flow: register the service worker, ask for notification
// permission, subscribe to push, and hand the subscription to the server.

const statusEl = document.getElementById("status");
const btn = document.getElementById("enable");
const iosHint = document.getElementById("ios-hint");

function setStatus(msg, kind) {
  statusEl.textContent = msg;
  statusEl.className = "status" + (kind ? " " + kind : "");
}

// VAPID public key (base64url) -> Uint8Array for applicationServerKey.
function urlB64ToUint8Array(base64) {
  const padding = "=".repeat((4 - (base64.length % 4)) % 4);
  const b64 = (base64 + padding).replace(/-/g, "+").replace(/_/g, "/");
  const raw = atob(b64);
  return Uint8Array.from(raw, (c) => c.charCodeAt(0));
}

// iOS only allows push from an installed (standalone) PWA.
const isIOS = /iphone|ipad|ipod/i.test(navigator.userAgent);
const isStandalone =
  window.matchMedia("(display-mode: standalone)").matches ||
  window.navigator.standalone === true;

async function init() {
  if (!("serviceWorker" in navigator) || !("PushManager" in window)) {
    setStatus("This browser does not support push notifications.", "err");
    btn.disabled = true;
    if (isIOS && !isStandalone) iosHint.hidden = false;
    return;
  }

  const reg = await navigator.serviceWorker.register("/sw.js");
  const existing = await reg.pushManager.getSubscription();

  if (Notification.permission === "granted" && existing) {
    setStatus("Notifications are on for this device. ✓", "ok");
    btn.textContent = "Notifications enabled";
    btn.disabled = true;
    return;
  }

  if (Notification.permission === "denied") {
    setStatus("Notifications are blocked. Enable them in browser settings.", "err");
    btn.disabled = true;
    return;
  }

  if (isIOS && !isStandalone) {
    iosHint.hidden = false;
    setStatus("Add this app to your Home Screen first.", null);
  } else {
    setStatus("Tap the button to turn on notifications.", null);
  }
}

async function enable() {
  btn.disabled = true;
  try {
    const permission = await Notification.requestPermission();
    if (permission !== "granted") {
      setStatus("Permission was not granted.", "err");
      btn.disabled = false;
      return;
    }

    const reg = await navigator.serviceWorker.ready;
    let sub = await reg.pushManager.getSubscription();
    if (!sub) {
      sub = await reg.pushManager.subscribe({
        userVisibleOnly: true,
        applicationServerKey: urlB64ToUint8Array(window.VAPID_PUBLIC_KEY),
      });
    }

    const res = await fetch("/api/subscribe", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(sub),
    });
    if (!res.ok) throw new Error("server rejected subscription");

    setStatus("Notifications are on for this device. ✓", "ok");
    btn.textContent = "Notifications enabled";
  } catch (err) {
    setStatus("Could not enable notifications: " + err.message, "err");
    btn.disabled = false;
  }
}

btn.addEventListener("click", enable);
init();
