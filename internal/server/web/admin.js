// Admin page logic. The page is gated by the admin session cookie, which is
// sent automatically on same-origin requests — no bearer token is injected.

function msg(el, text, kind) {
  el.textContent = text;
  el.className = "msg" + (kind ? " " + kind : "");
}

// Save appearance (name + optional icon).
document.getElementById("save").addEventListener("click", async () => {
  const el = document.getElementById("save-msg");
  const form = new FormData();
  form.append("name", document.getElementById("name").value);
  const file = document.getElementById("icon").files[0];
  if (file) form.append("icon", file);
  try {
    const res = await fetch("/api/config", { method: "POST", body: form });
    if (!res.ok) throw new Error(await res.text());
    msg(el, "Saved. Reloading…", "ok");
    setTimeout(() => location.reload(), 600);
  } catch (err) {
    msg(el, "Error: " + err.message, "err");
  }
});

// Send a test notification to a room.
document.getElementById("send").addEventListener("click", async () => {
  const el = document.getElementById("send-msg");
  const room = document.getElementById("t-room").value.trim();
  if (!room) { msg(el, "Enter a room name.", "err"); return; }
  msg(el, "Sending…", null);
  try {
    const headers = { "Content-Type": "application/json" };
    const secret = document.getElementById("t-secret").value;
    if (secret) headers["X-Room-Secret"] = secret;
    const res = await fetch("/n/" + encodeURIComponent(room), {
      method: "POST",
      headers,
      body: JSON.stringify({
        title: document.getElementById("t-title").value,
        body: document.getElementById("t-body").value,
        url: "/",
      }),
    });
    if (!res.ok) throw new Error(await res.text());
    const r = await res.json();
    msg(el, `Sent to ${r.recipients} device(s) (sent ${r.sent}, failed ${r.failed}).`, "ok");
  } catch (err) {
    msg(el, "Error: " + err.message, "err");
  }
});

// Log out: clear the session cookie and return to the token gate.
document.getElementById("logout").addEventListener("click", async () => {
  await fetch("/admin/logout", { method: "POST" });
  location.href = "/admin";
});

// --- Tokens -------------------------------------------------------------

async function loadTokens() {
  const box = document.getElementById("tokens");
  try {
    const res = await fetch("/api/tokens");
    if (!res.ok) throw new Error(await res.text());
    const toks = await res.json();
    if (!toks.length) { box.textContent = "No tokens yet."; return; }
    box.classList.remove("muted");
    box.innerHTML = "";
    for (const t of toks) {
      const row = document.createElement("div");
      row.className = "token-row";
      const meta = document.createElement("div");
      meta.className = "token-meta";
      const name = document.createElement("div");
      name.className = "name";
      name.textContent = t.label || "(unnamed)";
      const sub = document.createElement("div");
      sub.className = "sub";
      sub.textContent = t.prefix + "…";
      meta.append(name, sub);
      const controls = document.createElement("span");
      const del = document.createElement("button");
      del.className = "secondary";
      del.textContent = "Delete";
      del.addEventListener("click", async () => {
        if (!confirm(`Delete token "${t.label || t.prefix}"?`)) return;
        const res = await fetch("/api/tokens/" + t.id, { method: "DELETE" });
        if (!res.ok) { alert(await res.text()); return; }
        loadTokens();
      });
      controls.append(del);
      row.append(meta, controls);
      box.append(row);
    }
  } catch (err) {
    box.textContent = "Could not load tokens: " + err.message;
  }
}

document.getElementById("tk-create").addEventListener("click", async () => {
  const el = document.getElementById("tk-msg");
  try {
    const res = await fetch("/api/tokens", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ label: document.getElementById("tk-label").value }),
    });
    if (!res.ok) throw new Error(await res.text());
    const t = await res.json();
    msg(el, "Token created.", "ok");
    document.getElementById("tk-label").value = "";
    const box = document.getElementById("tk-secret");
    box.hidden = false;
    document.getElementById("tk-secret-val").textContent = t.secret;
    document.getElementById("tk-curl").textContent =
      `curl -H "Authorization: Bearer ${t.secret}" \\\n` +
      `  ${location.origin}/api/admin/rooms`;
    loadTokens();
  } catch (err) {
    msg(el, "Error: " + err.message, "err");
  }
});

// Render the device list with rename + remove controls.
async function loadDevices() {
  const box = document.getElementById("devices");
  try {
    const res = await fetch("/api/devices");
    if (!res.ok) throw new Error(await res.text());
    const devs = await res.json();
    if (!devs.length) { box.textContent = "No devices subscribed yet."; return; }
    box.innerHTML = "";
    for (const d of devs) {
      const row = document.createElement("div");
      row.className = "row";
      row.style.justifyContent = "space-between";
      row.style.marginTop = "10px";
      const name = d.label || d.user_agent || d.endpoint.slice(0, 40);
      const label = document.createElement("span");
      // Expired devices are kept, greyed out, so their rooms are waiting if the
      // device re-subscribes; they receive nothing in the meantime.
      label.textContent = d.expired_at ? name + " (expired)" : name;
      if (d.expired_at) label.style.opacity = "0.55";
      const controls = document.createElement("span");
      const rename = document.createElement("button");
      rename.className = "secondary"; rename.textContent = "Rename"; rename.style.marginRight = "8px";
      rename.addEventListener("click", async () => {
        const next = prompt("Device label:", d.label || "");
        if (next === null) return;
        await fetch("/api/devices/label", {
          method: "POST", headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ endpoint: d.endpoint, label: next }),
        });
        loadDevices();
      });
      const remove = document.createElement("button");
      remove.className = "secondary"; remove.textContent = "Remove";
      remove.addEventListener("click", async () => {
        if (!confirm("Remove this device?")) return;
        await fetch("/api/devices", {
          method: "DELETE", headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ endpoint: d.endpoint }),
        });
        loadDevices();
      });
      controls.append(rename, remove);
      row.append(label, controls);
      box.append(row);
    }
  } catch (err) {
    box.textContent = "Could not load devices: " + err.message;
  }
}

// --- Rooms -----------------------------------------------------------------
async function loadRooms() {
  const el = document.getElementById("rooms");
  try {
    const res = await fetch("/api/admin/rooms");
    if (!res.ok) throw new Error("failed");
    const rooms = await res.json();
    if (!rooms.length) { el.textContent = "No rooms yet."; return; }
    el.textContent = "";
    for (const r of rooms) {
      const row = document.createElement("div");
      row.className = "room-row";
      const label = document.createElement("span");
      label.textContent = r.room + " — " + r.subscribers + " device(s)";
      const logBtn = document.createElement("button");
      logBtn.className = "secondary";
      logBtn.textContent = "History";
      const logEl = document.createElement("div");
      logEl.className = "muted";
      logEl.hidden = true;
      logBtn.addEventListener("click", async () => {
        logEl.hidden = !logEl.hidden;
        if (!logEl.hidden) await loadRoomHistory(r.room, logEl);
      });
      row.append(label, logBtn);
      el.append(row, logEl);
    }
  } catch {
    el.textContent = "Could not load rooms.";
  }
}

async function loadRoomHistory(room, el) {
  const res = await fetch("/api/admin/rooms/log?room=" + encodeURIComponent(room));
  const posts = res.ok ? await res.json() : [];
  el.textContent = "";
  if (!posts.length) { el.textContent = "No notifications logged."; return; }
  for (const p of posts) {
    const row = document.createElement("div");
    const when = new Date(p.created_at * 1000).toLocaleString();
    row.textContent = when + " — " + (p.title || "") + (p.body ? ": " + p.body : "") +
      "  [sent " + p.sent + ", failed " + p.failed + (p.had_secret ? ", secret" : "") + "]";
    el.appendChild(row);
  }
}

loadTokens();
loadDevices();
loadRooms();
