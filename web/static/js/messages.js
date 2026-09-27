// A conversation: send without reloading, and show new messages as they
// arrive (events.js tells us; we fetch them over plain HTTP, so nothing is
// lost if an event is). Without this script the thread is a plain form.
const thread = document.getElementById("thread");
const form = document.querySelector("[data-composer]");
if (thread && form) {
  const id = thread.dataset.thread;
  const box = form.querySelector("textarea");
  const after = form.querySelector("[data-after]");
  const errorEl = form.querySelector("[data-composer-error]");
  let last = thread.dataset.lastId || "";
  let busy = false;

  const toBottom = () => window.scrollTo({ top: document.body.scrollHeight });
  toBottom();

  // Mark my latest message "Seen" (a reply from them means they read it).
  function markSeen() {
    thread.querySelectorAll("[data-seen]").forEach((s) => s.remove());
    const mine = thread.querySelectorAll("li.items-end");
    const p = mine[mine.length - 1]?.querySelector("p");
    if (p) p.insertAdjacentHTML("beforeend", "<span data-seen>· Seen</span>");
  }

  function append(html) {
    if (!html.trim()) return;
    const tpl = document.createElement("template");
    tpl.innerHTML = html;
    for (const li of tpl.content.querySelectorAll("li[data-id]")) {
      if (thread.querySelector(`li[data-id="${li.dataset.id}"]`)) continue; // already shown
      if (li.classList.contains("items-start")) markSeen();
      thread.appendChild(li);
      last = li.dataset.id;
      htmx.process(li); // report forms
    }
    after.value = last;
    document.querySelector("[data-empty-thread]")?.remove();
    toBottom();
  }

  async function catchUp() {
    const res = await fetch(`/messages/${id}/since?after=${encodeURIComponent(last)}`, { headers: { "HX-Request": "true" } });
    if (res.ok) append(await res.text());
  }

  form.addEventListener("submit", async (e) => {
    e.preventDefault();
    if (busy || !box.value.trim()) return;
    busy = true;
    errorEl.hidden = true;
    after.value = last;
    try {
      const res = await fetch(form.action, { method: "POST", body: new URLSearchParams(new FormData(form)), headers: { "HX-Request": "true" } });
      if (res.ok) {
        append(await res.text());
        box.value = "";
        grow();
      } else {
        errorEl.textContent = res.status === 422 ? await res.text() : "Couldn't send. Check your connection and try again.";
        errorEl.hidden = false;
      }
    } catch (_) {
      errorEl.textContent = "No connection. Your message is still here — try again.";
      errorEl.hidden = false;
    } finally {
      busy = false;
    }
  });

  // Ctrl/⌘+Enter sends; plain Enter is a new line (phones).
  box.addEventListener("keydown", (e) => {
    if (e.key === "Enter" && (e.ctrlKey || e.metaKey)) form.requestSubmit();
  });
  function grow() {
    box.style.height = "auto";
    box.style.height = Math.min(box.scrollHeight, 160) + "px";
  }
  box.addEventListener("input", grow);

  document.addEventListener("rm:message", (e) => {
    if (e.detail.conversation === id) catchUp();
  });
  document.addEventListener("rm:open", catchUp); // after a reconnect
  document.addEventListener("rm:read", (e) => {
    if (e.detail.conversation === id) markSeen();
  });
}

// First-message page: suggestion chips fill the box.
document.querySelectorAll("[data-suggest]").forEach((b) =>
  b.addEventListener("click", () => {
    const t = document.querySelector("[data-first-message] textarea");
    t.value = b.dataset.suggest;
    t.focus();
  }),
);
