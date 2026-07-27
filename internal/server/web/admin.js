// Admin page logic. All privileged calls carry the Bearer token that was
// injected into the page (window.TOKEN).

const auth = { Authorization: "Bearer " + window.TOKEN };

function msg(el, text, kind) {
  el.textContent = text;
  el.className = "msg" + (kind ? " " + kind : "");
}

// Show a ready-to-copy curl command for the current origin.
document.getElementById("curl").textContent =
  `curl -X POST ${location.origin}/api/send \\\n` +
  `  -H "Authorization: Bearer ${window.TOKEN}" \\\n` +
  `  -H "Content-Type: application/json" \\\n` +
  `  -d '{"title":"Hello","body":"It works","url":"/"}'`;

// Save appearance (name + optional icon).
document.getElementById("save").addEventListener("click", async () => {
  const el = document.getElementById("save-msg");
  const form = new FormData();
  form.append("name", document.getElementById("name").value);
  const file = document.getElementById("icon").files[0];
  if (file) form.append("icon", file);
  try {
    const res = await fetch("/api/config", { method: "POST", headers: auth, body: form });
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
      headers: { ...auth, "Content-Type": "application/json" },
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
    const res = await fetch("/api/rotate-token", { method: "POST", headers: auth });
    if (!res.ok) throw new Error(await res.text());
    const { token } = await res.json();
    window.TOKEN = token;
    document.getElementById("token").textContent = token;
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
