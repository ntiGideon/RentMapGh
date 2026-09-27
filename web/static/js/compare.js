// Compare tray: up to four places, remembered on this device (a
// convenience — if storage is blocked the tray simply starts empty).
// Buttons with [data-compare=id] toggle a place; the tray links to
// /compare?ids=… .
const KEY = "rentmap.compare";
const MAX = 4;

function load() {
  try {
    const v = JSON.parse(localStorage.getItem(KEY) || "[]");
    return Array.isArray(v) ? v.filter((x) => typeof x === "string").slice(0, MAX) : [];
  } catch (_) {
    return [];
  }
}
function save(ids) {
  try {
    localStorage.setItem(KEY, JSON.stringify(ids));
  } catch (_) {}
}
let ids = load();

function toast(kind, message) {
  document.body.dispatchEvent(new CustomEvent("showToast", { detail: { kind, message } }));
}

// ── Tray ─────────────────────────────────────────────────────────────────
const tray = document.createElement("div");
tray.className =
  "fixed bottom-20 right-4 z-40 hidden items-center gap-2 rounded-full bg-graphite-900 py-1.5 pl-4 pr-1.5 text-sm text-white shadow-card ring-1 ring-black/10 lg:bottom-6 dark:bg-mint-200 dark:text-graphite-900";
tray.innerHTML =
  '<span data-tray-count class="font-medium tabular-nums"></span>' +
  '<a data-tray-link class="inline-flex h-8 items-center rounded-full bg-white px-3 font-semibold text-graphite-900 dark:bg-graphite-900 dark:text-white">Compare</a>' +
  '<button type="button" data-tray-clear aria-label="Clear comparison" class="grid size-8 place-items-center rounded-full hover:bg-white/10 dark:hover:bg-black/10">✕</button>';
document.body.appendChild(tray);
tray.querySelector("[data-tray-clear]").addEventListener("click", () => {
  ids = [];
  save(ids);
  render();
});

function render() {
  const onComparePage = location.pathname === "/compare";
  tray.classList.toggle("hidden", ids.length === 0 || onComparePage);
  tray.classList.toggle("flex", ids.length > 0 && !onComparePage);
  tray.querySelector("[data-tray-count]").textContent = ids.length === 1 ? "1 place" : ids.length + " places";
  const link = tray.querySelector("[data-tray-link]");
  link.href = "/compare?ids=" + ids.join(",");
  link.classList.toggle("pointer-events-none", ids.length < 2);
  link.classList.toggle("opacity-50", ids.length < 2);
  link.title = ids.length < 2 ? "Add one more place to compare" : "";
  document.querySelectorAll("[data-compare]").forEach((b) => {
    b.hidden = false;
    const on = ids.includes(b.dataset.compare);
    b.setAttribute("aria-pressed", String(on));
    const label = b.querySelector("[data-compare-label]");
    if (label) label.textContent = on ? "Comparing" : "Compare";
    b.title = on ? "Remove from compare" : "Add to compare";
  });
  const clear = document.querySelector("[data-compare-clear]");
  if (clear) clear.hidden = !onComparePage || ids.length === 0;
}

document.addEventListener("click", (e) => {
  const b = e.target.closest("[data-compare]");
  if (b) {
    const id = b.dataset.compare;
    if (ids.includes(id)) {
      ids = ids.filter((x) => x !== id);
    } else if (ids.length >= MAX) {
      toast("error", "You can compare up to 4 places. Remove one first.");
      return;
    } else {
      ids = [...ids, id];
    }
    save(ids);
    render();
    return;
  }
  if (e.target.closest("[data-compare-clear]")) {
    ids = [];
    save(ids);
    location.href = "/saved";
  }
});

// Another tab changed the list.
window.addEventListener("storage", (e) => {
  if (e.key === KEY) {
    ids = load();
    render();
  }
});
document.body.addEventListener("htmx:afterSwap", render);

// The compare page is the source of truth for what's on it.
if (location.pathname === "/compare") {
  const shown = new URLSearchParams(location.search).get("ids");
  if (shown) {
    ids = shown.split(",").filter(Boolean).slice(0, MAX);
    save(ids);
  }
}
render();
