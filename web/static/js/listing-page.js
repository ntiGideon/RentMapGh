// Public listing page: photo lightbox, the approximate-area map and "Copy
// link". Everything degrades: photos are plain links to the large size, the
// map is a label until it scrolls into view.

// ── Lightbox ─────────────────────────────────────────────────────────────
const dialog = document.querySelector("[data-lightbox]");
const links = [...document.querySelectorAll("[data-gallery] a[data-index]")];
if (dialog && links.length) {
  const img = dialog.querySelector("[data-lightbox-img]");
  const count = dialog.querySelector("[data-lightbox-count]");
  let at = 0;
  const show = (i) => {
    at = (i + links.length) % links.length;
    img.src = links[at].href;
    img.alt = links[at].querySelector("img")?.alt || "";
    count.textContent = `${at + 1} / ${links.length}`;
    // Warm the next one so swiping feels instant.
    new Image().src = links[(at + 1) % links.length].href;
  };
  links.forEach((a, i) =>
    a.addEventListener("click", (e) => {
      if (e.metaKey || e.ctrlKey || e.shiftKey) return; // new tab still works
      e.preventDefault();
      show(i);
      dialog.showModal();
    }),
  );
  dialog.querySelector("[data-lightbox-prev]").addEventListener("click", () => show(at - 1));
  dialog.querySelector("[data-lightbox-next]").addEventListener("click", () => show(at + 1));
  dialog.querySelector("[data-lightbox-close]").addEventListener("click", () => dialog.close());
  dialog.addEventListener("keydown", (e) => {
    if (e.key === "ArrowLeft") show(at - 1);
    if (e.key === "ArrowRight") show(at + 1);
  });
  // Swipe left/right on phones.
  let x0 = null;
  img.addEventListener("pointerdown", (e) => (x0 = e.clientX));
  img.addEventListener("pointerup", (e) => {
    if (x0 === null) return;
    const dx = e.clientX - x0;
    x0 = null;
    if (Math.abs(dx) > 40) show(at + (dx < 0 ? 1 : -1));
  });
  img.addEventListener("dragstart", (e) => e.preventDefault());
}

// ── Area map ─────────────────────────────────────────────────────────────
// A soft ~300 m circle around the approximate point; no pin. MapLibre
// (~300 KB) loads only when the map scrolls near the viewport.
const STYLE = {
  light: "https://tiles.openfreemap.org/styles/positron",
  dark: "https://tiles.openfreemap.org/styles/dark",
};

function circle(lng, lat, metres, steps = 64) {
  const pts = [];
  const dLat = metres / 111320;
  const dLng = metres / (111320 * Math.cos((lat * Math.PI) / 180));
  for (let i = 0; i <= steps; i++) {
    const t = (i / steps) * 2 * Math.PI;
    pts.push([lng + dLng * Math.cos(t), lat + dLat * Math.sin(t)]);
  }
  return { type: "Feature", geometry: { type: "Polygon", coordinates: [pts] } };
}

async function drawMap(root) {
  const maplibregl = await import("/static/vendor/maplibre-6.11.2/maplibre-gl.mjs");
  const lat = parseFloat(root.dataset.lat);
  const lng = parseFloat(root.dataset.lng);
  const dark = document.documentElement.classList.contains("dark");
  const map = new maplibregl.Map({
    container: root.querySelector("[data-map]"),
    style: dark ? STYLE.dark : STYLE.light,
    center: [lng, lat],
    zoom: 14.6,
    maxZoom: 15.5, // closer than this invites guessing the building
    attributionControl: { compact: true },
    cooperativeGestures: true,
  });
  map.addControl(new maplibregl.NavigationControl({ showCompass: false }), "top-right");
  map.on("load", () => {
    root.querySelector("[data-map-fallback]")?.remove();
    map.addSource("area", { type: "geojson", data: circle(lng, lat, 300) });
    map.addLayer({ id: "area-fill", type: "fill", source: "area", paint: { "fill-color": "#34d3a6", "fill-opacity": 0.18 } });
    map.addLayer({ id: "area-line", type: "line", source: "area", paint: { "line-color": "#0f9f7a", "line-width": 1.5, "line-opacity": 0.7 } });
  });
}

const area = document.querySelector("[data-area-map]");
if (area && !document.documentElement.classList.contains("data-saver")) {
  const io = new IntersectionObserver(
    (entries) => {
      if (entries.some((e) => e.isIntersecting)) {
        io.disconnect();
        drawMap(area).catch(() => {}); // the label stays
      }
    },
    { rootMargin: "300px" },
  );
  io.observe(area);
}

// ── Copy link ────────────────────────────────────────────────────────────
document.addEventListener("click", async (e) => {
  const btn = e.target.closest("[data-copy]");
  if (!btn) return;
  try {
    await navigator.clipboard.writeText(btn.dataset.copy);
    const label = btn.textContent;
    btn.textContent = "Copied";
    setTimeout(() => (btn.textContent = label), 1500);
  } catch (_) {
    window.prompt("Copy this link:", btn.dataset.copy);
  }
});
