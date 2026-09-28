// k6 load test for the public search paths: the search page (full and
// htmx fragment), map markers, place suggestions, area pages and listing
// pages. Read-only; safe against staging or production.
//
//   k6 run -e BASE_URL=https://staging.rentmap.gh deploy/loadtest/search.js
//   k6 run -e BASE_URL=http://10.0.0.5:8080 -e SPREAD_IPS=1 deploy/loadtest/search.js
//
// Search is rate limited to 240 requests a minute per IP. To measure the
// app rather than the limiter, point BASE_URL at the web container itself
// (not through Caddy/Cloudflare, which set the client address) with
// TRUST_PROXY=true, and pass SPREAD_IPS=1: each virtual user then sends
// its own X-Real-IP.
//
// Targets (ProjectRequirement §6.15): responses under 300 ms (the TTFB
// budget) at p95 for search and markers at the expected launch peak (~50
// concurrent users), no errors. The budget's "search query p95 < 80 ms" is
// the database query alone — read it from the app's logs, not from k6.
import http from "k6/http";
import { check, group, sleep } from "k6";

const BASE = (__ENV.BASE_URL || "http://localhost:8080").replace(/\/$/, "");
const SPREAD = __ENV.SPREAD_IPS === "1";

export const options = {
  scenarios: {
    browse: {
      executor: "ramping-vus",
      startVUs: 0,
      stages: [
        { duration: "1m", target: 20 },
        { duration: "3m", target: 50 }, // launch peak
        { duration: "2m", target: 100 }, // headroom
        { duration: "1m", target: 0 },
      ],
    },
  },
  thresholds: {
    http_req_failed: ["rate<0.01"],
    "http_req_duration{kind:search}": ["p(95)<300"],
    "http_req_duration{kind:markers}": ["p(95)<300"],
    "http_req_duration{kind:places}": ["p(95)<150"],
    "http_req_duration{kind:page}": ["p(95)<500"],
  },
};

const places = ["knust", "tech-junction", "kstu", "aamusted", "csuc"];
const kinds = ["rooms-for-rent", "hostels", "apartments-for-rent"];
const types = ["single_room", "single_room_sc", "chamber_hall_sc"];
const sorts = ["recommended", "newest", "price_asc"];
const queries = ["kn", "ayed", "bom", "kote", "tech", "kentin"];
const pick = (a) => a[Math.floor(Math.random() * a.length)];

function headers(extra) {
  const h = Object.assign({ "User-Agent": "Mozilla/5.0 (k6 load test) Chrome/131" }, extra || {});
  if (SPREAD) h["X-Real-IP"] = `10.${__VU % 250}.${Math.floor(__VU / 250)}.${1 + (__ITER % 250)}`;
  return h;
}

export default function () {
  const place = pick(places);
  let listing = null;

  group("search", () => {
    const q = `near=${place}&radius=${pick([1, 2, 3])}&sort=${pick(sorts)}` +
      (Math.random() < 0.5 ? `&max=${pick([1500, 3000, 6000])}` : "") +
      (Math.random() < 0.3 ? `&type=${pick(types)}` : "");
    const page = http.get(`${BASE}/search?${q}`, { headers: headers(), tags: { kind: "search" } });
    check(page, { "search 200": (r) => r.status === 200 });
    const m = /href="(\/l\/[0-9a-f-]{36}\/[a-z0-9-]+)"/.exec(page.body || "");
    if (m) listing = m[1];

    // Panning the map: the htmx fragment and the markers for a bbox.
    const frag = http.get(`${BASE}/search?${q}`, { headers: headers({ "HX-Request": "true" }), tags: { kind: "search" } });
    check(frag, { "fragment 200": (r) => r.status === 200 });
    const lat = 6.67 + (Math.random() - 0.5) * 0.04, lng = -1.57 + (Math.random() - 0.5) * 0.04;
    const bbox = [lng - 0.02, lat - 0.015, lng + 0.02, lat + 0.015].map((v) => v.toFixed(4)).join(",");
    const mk = http.get(`${BASE}/search/markers.geojson?bbox=${bbox}`, { headers: headers(), tags: { kind: "markers" } });
    check(mk, { "markers 200": (r) => r.status === 200 });
  });

  group("suggest", () => {
    const r = http.get(`${BASE}/places?q=${pick(queries)}`, { headers: headers({ "HX-Request": "true" }), tags: { kind: "places" } });
    check(r, { "places 200": (x) => x.status === 200 });
  });

  group("pages", () => {
    const area = http.get(`${BASE}/kumasi/${place}/${pick(kinds)}`, { headers: headers(), tags: { kind: "page" } });
    check(area, { "area 200/404": (r) => r.status === 200 || r.status === 404 });
    if (listing) {
      const l = http.get(`${BASE}${listing}`, { headers: headers(), tags: { kind: "page" } });
      check(l, { "listing 200": (r) => r.status === 200 });
    }
  });

  sleep(1 + Math.random() * 3); // people read between taps
}
