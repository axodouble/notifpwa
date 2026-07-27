// Admin page logic. All privileged calls carry the Bearer token that was
// injected into the page (window.TOKEN).

function authHeader() {
  return { Authorization: "Bearer " + window.TOKEN };
}

function msg(el, text, kind) {
  el.textContent = text;
  el.className = "msg" + (kind ? " " + kind : "");
}

function updateCurl() {
  document.getElementById("curl").textContent =
    `curl -X POST ${location.origin}/api/send \\\n` +
    `  -H "Authorization: Bearer ${window.TOKEN}" \\\n` +
    `  -H "Content-Type: application/json" \\\n` +
    `  -d '{"title":"Hello","body":"It works","url":"/"}'`;
}

// Show a ready-to-copy curl command for the current origin.
updateCurl();

// Save appearance (name + optional icon).
document.getElementById("save").addEventListener("click", async () => {
  const el = document.getElementById("save-msg");
  const form = new FormData();
  form.append("name", document.getElementById("name").value);
  const file = document.getElementById("icon").files[0];
  if (file) form.append("icon", file);
  try {
    const res = await fetch("/api/config", { method: "POST", headers: authHeader(), body: form });
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
      headers: { ...authHeader(), "Content-Type": "application/json" },
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

// Rotate the API token. Shows the new value once; existing clients must update.
document.getElementById("rotate").addEventListener("click", async () => {
  const el = document.getElementById("rotate-msg");
  if (!confirm("Rotate the API token? Existing API clients will stop working until updated.")) return;
  try {
    const res = await fetch("/api/rotate-token", { method: "POST", headers: authHeader() });
    if (!res.ok) throw new Error(await res.text());
    const { token } = await res.json();
    window.TOKEN = token;
    document.getElementById("token").textContent = token;
    updateCurl();
    msg(el, "New token generated. Update your API clients.", "ok");
  } catch (err) {
    msg(el, "Error: " + err.message, "err");
  }
});

// Log out: clear the session cookie and return to the token gate.
document.getElementById("logout").addEventListener("click", async () => {
  await fetch("/admin/logout", { method: "POST" });
  location.href = "/admin";
});

// Render the device list with rename + remove controls.
async function loadDevices() {
  const box = document.getElementById("devices");
  try {
    const res = await fetch("/api/devices", { headers: authHeader() });
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
          method: "POST", headers: { ...authHeader(), "Content-Type": "application/json" },
          body: JSON.stringify({ endpoint: d.endpoint, label: next }),
        });
        loadDevices();
      });
      const remove = document.createElement("button");
      remove.className = "secondary"; remove.textContent = "Remove";
      remove.addEventListener("click", async () => {
        if (!confirm("Remove this device?")) return;
        await fetch("/api/devices", {
          method: "DELETE", headers: { ...authHeader(), "Content-Type": "application/json" },
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
loadDevices();
