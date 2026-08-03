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
  roomMsgEl.className = "status" + (kind ? " " + kind : "");
}

async function showRooms(endpoint) {
  currentEndpoint = endpoint;
  roomsEl.hidden = false;
  await loadRooms();
}

async function loadRooms() {
  const res = await fetch("/api/rooms?endpoint=" + encodeURIComponent(currentEndpoint));
  const rooms = res.ok ? await res.json() : [];
  roomListEl.textContent = "";
  if (!rooms.length) {
    const empty = document.createElement("div");
    empty.className = "room-log";
    empty.textContent = "You have not joined any rooms yet.";
    roomListEl.appendChild(empty);
  }
  for (const r of rooms) roomListEl.appendChild(roomItem(r));
}

function roomItem(r) {
  const wrap = document.createElement("div");
  wrap.className = "room-item";

  const head = document.createElement("div");
  head.className = "room-head";
  const name = document.createElement("span");
  name.className = "room-name";
  name.textContent = r.room + (r.has_secret ? " 🔒" : "");
  const actions = document.createElement("div");
  actions.className = "room-actions";

  const secretBtn = document.createElement("button");
  secretBtn.type = "button";
  secretBtn.className = "ghost";
  secretBtn.textContent = r.has_secret ? "Change secret" : "Set secret";
  secretBtn.addEventListener("click", () => setSecret(r.room));

  const clearBtn = document.createElement("button");
  clearBtn.type = "button";
  clearBtn.className = "ghost";
  clearBtn.textContent = "Clear";
  clearBtn.hidden = !r.has_secret;
  clearBtn.addEventListener("click", () => clearSecret(r.room));

  const leaveBtn = document.createElement("button");
  leaveBtn.type = "button";
  leaveBtn.textContent = "Leave";
  leaveBtn.addEventListener("click", () => leaveRoom(r.room));

  actions.append(secretBtn, clearBtn, leaveBtn);
  head.append(name, actions);

  const logBtn = document.createElement("button");
  logBtn.type = "button";
  logBtn.className = "ghost";
  logBtn.style.marginTop = "8px";
  logBtn.textContent = "Recent";
  const logEl = document.createElement("div");
  logEl.className = "room-log";
  logEl.hidden = true;
  logBtn.addEventListener("click", async () => {
    logEl.hidden = !logEl.hidden;
    if (!logEl.hidden) await loadRoomLog(r.room, logEl);
  });

  wrap.append(head, logBtn, logEl);
  return wrap;
}

async function loadRoomLog(room, el) {
  const res = await fetch(
    "/api/rooms/log?endpoint=" + encodeURIComponent(currentEndpoint) + "&room=" + encodeURIComponent(room));
  const posts = res.ok ? await res.json() : [];
  el.textContent = "";
  if (!posts.length) { el.textContent = "No received notifications."; return; }
  for (const p of posts) {
    const row = document.createElement("div");
    const when = new Date(p.created_at * 1000).toLocaleString();
    row.textContent = when + " — " + (p.title || "") + (p.body ? ": " + p.body : "");
    el.appendChild(row);
  }
}

async function postRoom(method, room, secret) {
  const body = { endpoint: currentEndpoint, room };
  if (secret !== undefined) body.secret = secret;
  const res = await fetch("/api/rooms", {
    method,
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
  return res.ok;
}

async function joinRoomByName() {
  const input = document.getElementById("room-name");
  const room = input.value.trim();
  if (!room) return;
  if (await postRoom("POST", room)) {
    input.value = "";
    roomMsg("Joined " + room + ".", "ok");
    await loadRooms();
  } else {
    roomMsg("Could not join " + room + ".", "err");
  }
}

async function setSecret(room) {
  const secret = prompt("Secret for room \"" + room + "\" (posts must include it to reach you):");
  if (secret === null) return;
  if (await postRoom("POST", room, secret)) {
    roomMsg("Secret updated for " + room + ".", "ok");
    await loadRooms();
  } else {
    roomMsg("Could not update secret.", "err");
  }
}

async function clearSecret(room) {
  if (await postRoom("POST", room, "")) {
    roomMsg("Secret cleared for " + room + ".", "ok");
    await loadRooms();
  }
}

async function leaveRoom(room) {
  const res = await fetch("/api/rooms", {
    method: "DELETE",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ endpoint: currentEndpoint, room }),
  });
  if (res.ok) { roomMsg("Left " + room + ".", "ok"); await loadRooms(); }
}

document.getElementById("room-add").addEventListener("click", joinRoomByName);

init();
