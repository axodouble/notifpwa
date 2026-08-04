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
    showRooms(existing.endpoint);
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
    showRooms(sub.endpoint);
  } catch (err) {
    setStatus("Could not enable notifications: " + err.message, "err");
    btn.disabled = false;
  }
}

btn.addEventListener("click", enable);

// --- Rooms -----------------------------------------------------------------
// A device manages its own room memberships, identified by its push endpoint.
let currentEndpoint = null;
const roomsEl = document.getElementById("rooms");
const roomListEl = document.getElementById("room-list");
const roomMsgEl = document.getElementById("room-msg");

function roomMsg(text, kind) {
  roomMsgEl.hidden = !text;
  roomMsgEl.textContent = text || "";
  roomMsgEl.className = "room-msg" + (kind ? " " + kind : "");
}

async function showRooms(endpoint) {
  currentEndpoint = endpoint;
  roomsEl.hidden = false;
  await loadRooms();
}

async function loadRooms() {
  let rooms = [];
  try {
    const res = await fetch("/api/rooms?endpoint=" + encodeURIComponent(currentEndpoint));
    if (res.ok) rooms = await res.json();
  } catch (_) { /* offline: fall through to empty state */ }

  roomListEl.textContent = "";
  if (!rooms.length) {
    const empty = document.createElement("p");
    empty.className = "room-empty";
    empty.textContent =
      "You haven't joined any rooms yet. Join one below to start receiving its notifications.";
    roomListEl.appendChild(empty);
    return;
  }
  rooms.forEach((r, i) => roomListEl.appendChild(roomRow(r, i)));
}

function closeOtherRooms(except) {
  roomListEl.querySelectorAll(".room-toggle[aria-expanded='true']").forEach((t) => {
    if (t !== except) {
      t.setAttribute("aria-expanded", "false");
      t.nextElementSibling.hidden = true;
    }
  });
}

function roomRow(r, i) {
  const row = document.createElement("div");
  row.className = "room";

  const panelId = "room-panel-" + i;
  const toggle = document.createElement("button");
  toggle.type = "button";
  toggle.className = "room-toggle";
  toggle.setAttribute("aria-expanded", "false");
  toggle.setAttribute("aria-controls", panelId);

  const name = document.createElement("span");
  name.className = "room-name";
  name.textContent = r.room;

  const flags = document.createElement("span");
  flags.className = "room-flags";
  if (r.has_secret) {
    const lock = document.createElement("span");
    lock.className = "room-lock";
    lock.title = "Secret set";
    lock.textContent = "🔒";
    flags.appendChild(lock);
  }
  const chev = document.createElement("span");
  chev.className = "room-chevron";
  chev.setAttribute("aria-hidden", "true");
  flags.appendChild(chev);
  toggle.append(name, flags);

  const panel = buildRoomPanel(r, panelId);
  let logged = false;
  toggle.addEventListener("click", () => {
    const open = toggle.getAttribute("aria-expanded") === "true";
    if (open) {
      toggle.setAttribute("aria-expanded", "false");
      panel.hidden = true;
      return;
    }
    closeOtherRooms(toggle);
    toggle.setAttribute("aria-expanded", "true");
    panel.hidden = false;
    if (!logged) {
      logged = true;
      loadRoomLog(r.room, panel.querySelector(".room-recent"));
    }
  });

  row.append(toggle, panel);
  return row;
}

function buildRoomPanel(r, panelId) {
  const panel = document.createElement("div");
  panel.className = "room-panel";
  panel.id = panelId;
  panel.hidden = true;

  const state = document.createElement("p");
  state.className = "room-secret-state";
  state.textContent = r.has_secret ? "Secret set" : "No secret";

  const secretRow = document.createElement("div");
  secretRow.className = "room-secret-row";
  const input = document.createElement("input");
  input.type = "password";
  input.className = "room-secret-input";
  input.autocomplete = "new-password";
  input.placeholder = r.has_secret ? "Change secret" : "Set a secret";
  const save = document.createElement("button");
  save.type = "button";
  save.className = "room-btn";
  save.textContent = "Save";
  save.addEventListener("click", () => {
    if (input.value === "") return; // empty Save is a no-op; use Clear to remove
    saveSecret(r.room, input.value);
  });
  secretRow.append(input, save);
  if (r.has_secret) {
    const clear = document.createElement("button");
    clear.type = "button";
    clear.className = "room-btn ghost";
    clear.textContent = "Clear";
    clear.addEventListener("click", () => saveSecret(r.room, ""));
    secretRow.append(clear);
  }

  const recentLabel = document.createElement("p");
  recentLabel.className = "room-recent-label";
  recentLabel.textContent = "Recent";
  const recent = document.createElement("div");
  recent.className = "room-recent";
  recent.textContent = "Loading…";

  const leave = document.createElement("button");
  leave.type = "button";
  leave.className = "room-leave";
  leave.textContent = "Leave room";
  leave.addEventListener("click", () => leaveRoom(r.room));

  panel.append(state, secretRow, recentLabel, recent, leave);
  return panel;
}

async function loadRoomLog(room, el) {
  let posts = [];
  try {
    const res = await fetch(
      "/api/rooms/log?endpoint=" + encodeURIComponent(currentEndpoint) +
      "&room=" + encodeURIComponent(room));
    if (res.ok) posts = await res.json();
  } catch (_) { /* ignore */ }
  el.textContent = "";
  if (!posts.length) { el.textContent = "No notifications received yet."; return; }
  for (const p of posts) {
    const item = document.createElement("div");
    item.className = "room-recent-item";
    const when = document.createElement("span");
    when.className = "room-recent-when";
    when.textContent = new Date(p.created_at * 1000).toLocaleString();
    const text = document.createElement("span");
    text.className = "room-recent-text";
    text.textContent = (p.title || "") + (p.body ? ": " + p.body : "");
    item.append(when, text);
    el.appendChild(item);
  }
}

async function apiRoom(method, room, secret) {
  const body = { endpoint: currentEndpoint, room };
  if (secret !== undefined) body.secret = secret;
  try {
    const res = await fetch("/api/rooms", {
      method,
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
    });
    return res.ok;
  } catch (_) {
    return false;
  }
}

async function joinRoomByName(e) {
  if (e) e.preventDefault();
  const input = document.getElementById("room-name");
  const room = input.value.trim();
  if (!room) return;
  if (await apiRoom("POST", room)) {
    input.value = "";
    roomMsg("Joined " + room + ".", "ok");
    await loadRooms();
  } else {
    roomMsg("Could not join " + room + ".", "err");
  }
}

async function saveSecret(room, secret) {
  if (await apiRoom("POST", room, secret)) {
    roomMsg(secret === "" ? "Secret cleared for " + room + "." : "Secret updated for " + room + ".", "ok");
    await loadRooms();
  } else {
    roomMsg("Could not update the secret.", "err");
  }
}

async function leaveRoom(room) {
  if (await apiRoom("DELETE", room)) {
    roomMsg("Left " + room + ".", "ok");
    await loadRooms();
  } else {
    roomMsg("Could not leave " + room + ".", "err");
  }
}

document.getElementById("room-join").addEventListener("submit", joinRoomByName);

init();
