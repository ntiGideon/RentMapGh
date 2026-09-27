// Search page: the map and the list stay in sync, and the URL is the
// source of truth (every search is a shareable, back-button-safe link).
// Without this script the page is a plain GET form with a list.
//
//   filters / sort / chips ──┐
//   map pan / zoom ──────────┼─► runSearch() ─► GET /search?…  → list pane (htmx)
//                            │                 GET /search/markers.geojson?… → map
//   back / forward ──────────┘
const form = document.querySelector("[data-search]");
const bboxInput = form.querySelector("[data-bbox]");
const mapEl = form.querySelector("[data-search-map]");
const loading = form.querySelector("[data-map-loading]");
const preview = form.querySelector("[data-preview]");
const dataSaver = document.documentElement.classList.contains("data-saver");
document.documentElement.classList.add("js");

const STYLE = {
  light: "https://tiles.openfreemap.org/styles/positron",
  dark: "https://tiles.openfreemap.org/styles/dark",
};

// ── Query ────────────────────────────────────────────────────────────────
// Type chips appear up to three times (header, phone row, filter sheet):
// keep the copies in step, and send each name=value once.
function query() {
  const seen = new Set();
  const q = new URLSearchParams();
  for (const [k, v] of new FormData(form)) {
    if (v === "" || (k === "sort" && v === "recommended")) continue;
    const key = k + "=" + v;
    if (seen.has(key)) continue;
    seen.add(key);
    q.append(k, v);
  }
  return q;
}

form.addEventListener("change", (e) => {
  const el = e.target;
  if (el.type === "checkbox" && el.name) {
    form.querySelectorAll(`input[type=checkbox][name="${el.name}"][value="${el.value}"]`).forEach((c) => (c.checked = el.checked));
  }
  if (el.matches("[data-autosubmit]")) runSearch({ push: true });
});

// ── Running a search ─────────────────────────────────────────────────────
let lastQuery = null;
function runSearch({ push = false, page = null } = {}) {
  const q = query();
  if (page) q.set("page", page);
  const qs = q.toString();
  const url = "/search" + (qs ? "?" + qs : "");
  if (qs === lastQuery) return;
  lastQuery = qs;
  history[push ? "pushState" : "replaceState"]({ search: true }, "", url);
  htmx.ajax("GET", url, { target: "#search-results", swap: "outerHTML" });
  q.delete("page");
  loadMarkers(q.toString());
}

// The header's filter count lives outside the swapped pane.
document.body.addEventListener("htmx:afterSwap", () => {
  const n = Number(document.getElementById("search-results")?.dataset.active || 0);
  const badge = form.querySelector("[data-active-badge]");
  badge.textContent = n;
  badge.classList.toggle("hidden", n === 0);
  badge.classList.toggle("grid", n > 0);
});

// Back/forward: reload the page state from the URL.
window.addEventListener("popstate", () => location.reload());

// Pagination and "Widen the area" without a full page load.
form.addEventListener("click", (e) => {
  const pageLink = e.target.closest("a[data-page]");
  if (pageLink) {
    e.preventDefault();
    const page = new URL(pageLink.href).searchParams.get("page") || "1";
    runSearch({ push: true, page });
    form.querySelector("[data-list-pane]").scrollTo({ top: 0 });
    return;
  }
  if (e.target.closest("a[data-zoom-out]") && map) {
    e.preventDefault();
    map.zoomOut();
  }
});

// ── Filter sheet ─────────────────────────────────────────────────────────
const sheet = form.querySelector("[data-filters]");
form.querySelector("[data-open-filters]").addEventListener("click", () => sheet.showModal());
form.querySelector("[data-close-filters]").addEventListener("click", () => sheet.close());
form.addEventListener("submit", (e) => {
  e.preventDefault();
  if (sheet.open) sheet.close();
  runSearch({ push: true });
});
form.querySelector("[data-clear-filters]").addEventListener("click", (e) => {
  e.preventDefault();
  for (const el of form.elements) {
    if (el === bboxInput || el.name === "sort") continue;
    if (el.type === "checkbox") el.checked = false;
    else if (el.tagName === "SELECT" || el.type === "text" || el.type === "date") el.value = "";
  }
  sheet.close();
  runSearch({ push: true });
});

// ── Map ──────────────────────────────────────────────────────────────────
let map = null;
let maplibregl = null;
const pills = new Map(); // listing id → maplibregl.Marker on screen
const emptyGeo = { type: "FeatureCollection", features: [] };

function bboxOf(s) {
  const v = (s || "").split(",").map(Number);
  return v.length === 4 && v.every(Number.isFinite) ? [[v[0], v[1]], [v[2], v[3]]] : null;
}

async function initMap() {
  if (map) return;
  maplibregl = await import("/static/vendor/maplibre-6.11.2/maplibre-gl.mjs");
  const dark = document.documentElement.classList.contains("dark");
  const start = bboxOf(bboxInput.value) || bboxOf(mapEl.dataset.defaultBbox);
  map = new maplibregl.Map({
    container: mapEl,
    style: dark ? STYLE.dark : STYLE.light,
    bounds: start,
    maxZoom: 16, // pills sit on approximate points; closer adds nothing
    attributionControl: { compact: true },
  });
  map.addControl(new maplibregl.NavigationControl({ showCompass: false }), "top-right");
  map.addControl(new maplibregl.GeolocateControl({ positionOptions: { enableHighAccuracy: false }, showAccuracyCircle: false }), "top-right");

  map.on("load", () => {
    map.addSource("listings", { type: "geojson", data: emptyGeo, cluster: true, clusterRadius: 44, clusterMaxZoom: 15 });
    map.addLayer({
      id: "clusters", type: "circle", source: "listings", filter: ["has", "point_count"],
      paint: {
        "circle-color": dark ? "#b8f7e4" : "#25272c",
        "circle-radius": ["step", ["get", "point_count"], 16, 10, 20, 50, 26],
        "circle-stroke-width": 3, "circle-stroke-color": dark ? "rgba(184,247,228,0.25)" : "rgba(37,39,44,0.18)",
      },
    });
    map.addLayer({
      id: "cluster-count", type: "symbol", source: "listings", filter: ["has", "point_count"],
      layout: { "text-field": ["get", "point_count_abbreviated"], "text-font": ["Noto Sans Bold"], "text-size": 12.5 },
      paint: { "text-color": dark ? "#25272c" : "#ffffff" },
    });
    map.on("click", "clusters", async (e) => {
      const f = map.queryRenderedFeatures(e.point, { layers: ["clusters"] })[0];
      const zoom = await map.getSource("listings").getClusterExpansionZoom(f.properties.cluster_id);
      map.easeTo({ center: f.geometry.coordinates, zoom });
    });
    map.on("mouseenter", "clusters", () => (map.getCanvas().style.cursor = "pointer"));
    map.on("mouseleave", "clusters", () => (map.getCanvas().style.cursor = ""));
    map.on("render", syncPills);
    const q = query();
    q.delete("page");
    loadMarkers(q.toString());
  });

  // Panning searches the new area once the map settles. The initial fit
  // (before the first idle) isn't a search: the list already matches it.
  let ready = false;
  map.once("idle", () => (ready = true));
  let timer = null;
  map.on("moveend", () => {
    if (!ready) return;
    clearTimeout(timer);
    timer = setTimeout(() => {
      const b = map.getBounds();
      bboxInput.value = [b.getWest(), b.getSouth(), b.getEast(), b.getNorth()].map((n) => n.toFixed(5)).join(",");
      runSearch();
    }, 350);
  });
}

// Price pills are DOM markers for the unclustered points on screen
// (crisp text, CSS hover states); clusters stay in WebGL.
function syncPills() {
  if (!map.isSourceLoaded("listings")) return;
  const seen = new Set();
  for (const f of map.querySourceFeatures("listings", { filter: ["!", ["has", "point_count"]] })) {
    const id = f.properties.id;
    if (seen.has(id)) continue;
    seen.add(id);
    if (!pills.has(id)) {
      const el = document.createElement("button");
      el.type = "button";
      el.className = "price-pill";
      el.dataset.id = id;
      el.textContent = f.properties.label || "₵—";
      el.setAttribute("aria-label", "Listing, " + el.textContent);
      el.addEventListener("click", (e) => {
        e.stopPropagation();
        openPreview(id);
      });
      el.addEventListener("mouseenter", () => card(id)?.classList.add("is-hot"));
      el.addEventListener("mouseleave", () => card(id)?.classList.remove("is-hot"));
      pills.set(id, new maplibregl.Marker({ element: el }).setLngLat(f.geometry.coordinates).addTo(map));
    }
  }
  for (const [id, m] of pills) {
    if (!seen.has(id)) {
      m.remove();
      pills.delete(id);
    }
  }
}

let markersAbort = null;
async function loadMarkers(qs) {
  if (!map || !map.getSource("listings")) return;
  markersAbort?.abort();
  markersAbort = new AbortController();
  loading.classList.remove("hidden");
  try {
    const res = await fetch("/search/markers.geojson" + (qs ? "?" + qs : ""), { signal: markersAbort.signal });
    if (res.ok) map.getSource("listings").setData(await res.json());
  } catch (_) {
    /* aborted or offline: keep the old markers */
  } finally {
    loading.classList.add("hidden");
  }
}

// ── Preview (bottom sheet on phones, card over the map on desktop) ───────
let openId = null;
async function openPreview(id) {
  pills.get(openId)?.getElement().classList.remove("is-open");
  openId = id;
  pills.get(id)?.getElement().classList.add("is-open");
  try {
    const res = await fetch("/l/" + id + "/card", { headers: { "HX-Request": "true" } });
    if (!res.ok || openId !== id) return;
    preview.innerHTML = await res.text();
    preview.classList.remove("hidden");
  } catch (_) {}
}
function closePreview() {
  pills.get(openId)?.getElement().classList.remove("is-open");
  openId = null;
  preview.classList.add("hidden");
  preview.innerHTML = "";
}
preview.addEventListener("click", (e) => {
  if (e.target.closest("[data-close-preview]")) closePreview();
});
mapEl.addEventListener("click", (e) => {
  if (!e.target.closest(".price-pill")) closePreview();
});

// ── List ↔ map highlight ─────────────────────────────────────────────────
function card(id) {
  return form.querySelector(`[data-result-id="${id}"]`);
}
form.addEventListener("mouseover", (e) => {
  const c = e.target.closest("[data-result-id]");
  if (!c) return;
  const pill = pills.get(c.dataset.resultId)?.getElement();
  if (!pill || pill.classList.contains("is-hot")) return;
  form.querySelectorAll(".price-pill.is-hot").forEach((p) => p.classList.remove("is-hot"));
  pill.classList.add("is-hot");
});
form.addEventListener("mouseout", (e) => {
  const c = e.target.closest("[data-result-id]");
  if (c && !c.contains(e.relatedTarget)) pills.get(c.dataset.resultId)?.getElement().classList.remove("is-hot");
});

// ── Map / list toggle (phones) and data saver ────────────────────────────
const desktop = window.matchMedia("(min-width: 1024px)");
form.querySelector("[data-toggle-view]").addEventListener("click", async () => {
  const showMap = form.classList.toggle("show-map");
  if (showMap) {
    await initMap();
    map.resize();
  }
});
form.querySelector("[data-show-map]").addEventListener("click", async () => {
  form.querySelector("[data-map-off]").classList.add("hidden");
  await initMap();
});

if (desktop.matches) {
  if (dataSaver) form.querySelector("[data-map-off]").classList.replace("hidden", "grid");
  else initMap();
}
desktop.addEventListener("change", (e) => {
  if (e.matches && !dataSaver) initMap().then(() => map.resize());
});

lastQuery = query().toString();
