// Location step of the listing wizard: drop/drag a pin, or use the phone's
// GPS. Only loaded on that page (MapLibre is ~300 KB gzipped).
import * as maplibregl from "/static/vendor/maplibre-6.11.2/maplibre-gl.mjs";

const STYLE = {
  light: "https://tiles.openfreemap.org/styles/positron",
  dark: "https://tiles.openfreemap.org/styles/dark",
};

function init(root) {
  if (root.dataset.ready) return;
  root.dataset.ready = "1";

  const form = root.closest("form");
  const lat = form.querySelector("input[name=lat]");
  const lng = form.querySelector("input[name=lng]");
  const readout = root.querySelector("[data-pin-readout]");
  const hint = root.querySelector("[data-pin-hint]");
  const locate = root.querySelector("[data-locate]");
  const has = lat.value !== "" && lng.value !== "";
  const start = has
    ? [parseFloat(lng.value), parseFloat(lat.value)]
    : [parseFloat(root.dataset.defaultLng), parseFloat(root.dataset.defaultLat)];

  const dark = document.documentElement.classList.contains("dark");
  const map = new maplibregl.Map({
    container: root.querySelector("[data-map]"),
    style: dark ? STYLE.dark : STYLE.light,
    center: start,
    zoom: has ? 17 : 14.5,
    attributionControl: { compact: true },
    cooperativeGestures: window.matchMedia("(pointer: coarse)").matches, // two-finger pan on phones, so the page still scrolls
  });
  map.addControl(new maplibregl.NavigationControl({ showCompass: false }), "top-right");

  const pinEl = document.createElement("div");
  pinEl.className = "location-pin";
  pinEl.innerHTML = '<svg viewBox="0 0 40 52" aria-hidden="true"><path d="M20 1C9.5 1 1 9.3 1 19.6 1 33.2 17.6 49 18.6 50a2 2 0 0 0 2.8 0C22.4 49 39 33.2 39 19.6 39 9.3 30.5 1 20 1Z"/><circle cx="20" cy="19.5" r="7"/></svg>';
  const marker = new maplibregl.Marker({ element: pinEl, draggable: true, anchor: "bottom" });

  function place(lngLat, save) {
    marker.setLngLat(lngLat).addTo(map);
    lat.value = lngLat.lat.toFixed(6);
    lng.value = lngLat.lng.toFixed(6);
    if (readout) readout.textContent = lat.value + ", " + lng.value;
    if (hint) hint.textContent = "Drag the pin to fine-tune. Renters only ever see the approximate area.";
    root.classList.add("has-pin");
    if (save) form.dispatchEvent(new Event("change", { bubbles: true })); // autosave
  }

  if (has) place({ lng: start[0], lat: start[1] }, false);
  map.on("click", (e) => place(e.lngLat, true));
  marker.on("dragend", () => place(marker.getLngLat(), true));

  if (locate && "geolocation" in navigator) {
    locate.hidden = false;
    locate.addEventListener("click", () => {
      locate.disabled = true;
      const label = locate.querySelector("[data-label]");
      const text = label.textContent;
      label.textContent = "Finding you…";
      navigator.geolocation.getCurrentPosition(
        (pos) => {
          const p = { lng: pos.coords.longitude, lat: pos.coords.latitude };
          map.flyTo({ center: p, zoom: 18 });
          place(p, true);
          label.textContent = text;
          locate.disabled = false;
        },
        () => {
          label.textContent = "Location unavailable — drop the pin instead";
          locate.disabled = false;
        },
        { enableHighAccuracy: true, timeout: 15000, maximumAge: 0 },
      );
    });
  }
}

document.querySelectorAll("[data-location-picker]").forEach(init);
