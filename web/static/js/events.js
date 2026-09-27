// Live updates on every signed-in page: one EventSource on /events keeps
// the header's unread badge current and re-broadcasts each event as a DOM
// event ("rm:message", "rm:read", "rm:open") for page scripts like
// messages.js. The browser reconnects by itself after a drop.
if (window.EventSource && !window.__rmEvents) {
  window.__rmEvents = true;
  const es = new EventSource("/events");

  function setBadge(n) {
    document.querySelectorAll('[data-badge="unread"]').forEach((b) => {
      b.textContent = n;
      b.classList.toggle("hidden", n === 0);
      b.classList.toggle("grid", n > 0);
    });
  }

  es.addEventListener("notification", (e) => {
    let n = 0;
    try {
      n = JSON.parse(e.data).unread || 0;
    } catch (_) {}
    document.querySelectorAll('[data-badge="notifications"]').forEach((b) => {
      b.textContent = n;
      b.classList.toggle("hidden", n === 0);
      b.classList.toggle("grid", n > 0);
    });
  });

  for (const kind of ["unread", "message", "read"]) {
    es.addEventListener(kind, (e) => {
      let data = {};
      try {
        data = JSON.parse(e.data);
      } catch (_) {}
      if (kind !== "read") setBadge(data.unread || 0);
      document.dispatchEvent(new CustomEvent("rm:" + kind, { detail: data }));
    });
  }
  es.addEventListener("open", () => document.dispatchEvent(new CustomEvent("rm:open")));
}
