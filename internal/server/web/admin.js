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

// Send a test notification to all subscribed devices.
document.getElementById("send").addEventListener("click", async () => {
  const el = document.getElementById("send-msg");
  msg(el, "Sending…", null);
  try {
    const res = await fetch("/api/send", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        title: document.getElementById("t-title").value,
        body: document.getElementById("t-body").value,
        url: "/",
      }),
    });
    if (!res.ok) throw new Error(await res.text());
    const r = await res.json();
    msg(el, `Sent ${r.sent}, failed ${r.failed}, pruned ${r.pruned}.`, "ok");
    if (typeof r.pruned === "number") {
      const c = document.getElementById("count");
      c.textContent = Math.max(0, parseInt(c.textContent, 10) - r.pruned);
    }
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

function scopeBadges(t) {
  const a = `<span class="badge${t.admin ? " on" : ""}">admin</span>`;
  const s = `<span class="badge${t.send ? " on" : ""}">send</span>`;
  return a + s;
}

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
      sub.append(document.createTextNode(t.prefix + "… · "));
      const badges = document.createElement("span");
      badges.innerHTML = scopeBadges(t);
      sub.append(badges);
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
  const admin = document.getElementById("tk-admin").checked;
  const send = document.getElementById("tk-send").checked;
  if (!admin && !send) { msg(el, "Pick at least one scope.", "err"); return; }
  try {
    const res = await fetch("/api/tokens", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ label: document.getElementById("tk-label").value, admin, send }),
    });
    if (!res.ok) throw new Error(await res.text());
    const t = await res.json();
    msg(el, "Token created.", "ok");
    document.getElementById("tk-label").value = "";
    const box = document.getElementById("tk-secret");
    box.hidden = false;
    document.getElementById("tk-secret-val").textContent = t.secret;
    document.getElementById("tk-curl").textContent =
      `curl -X POST ${location.origin}/api/send \\\n` +
      `  -H "Authorization: Bearer ${t.secret}" \\\n` +
      `  -H "Content-Type: application/json" \\\n` +
      `  -d '{"title":"Hello","body":"It works","url":"/"}'`;
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
      label.textContent = name;
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

loadTokens();
loadDevices();
