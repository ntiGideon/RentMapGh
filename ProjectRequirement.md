# RentMap Ghana — Full Build Blueprint

*Go · templ · HTMX · Tailwind · Ent · PostgreSQL/PostGIS · Docker*

This is the head-to-tail plan: product principles, stack, architecture, data model, the hard technical designs, eleven build phases (0–10) with exit criteria, the "delight" layer that makes people love it, and the security, legal, testing and operations work that keeps it standing.

---

## 1. Product principles (the rules every feature is checked against)

1. **The map is the home screen.** Location is the primary interface, not a filter.
2. **No surprises.** Every cost (advance, deposit, agent fee, viewing fee, service charge) is shown before contact. The *total move-in cost* is always visible.
3. **Trust is a feature, not a page.** Verification, honest availability, scam warnings and duplicate detection are built into every flow.
4. **Respect people's data bundles.** Pages must be fast and light on a mid-range Android phone on 3G/4G. A "data saver" mode exists from day one.
5. **Speak Ghanaian property.** "Chamber and hall", "single room self-contained", "boys' quarters", prepaid vs shared meter, poly tank vs borehole — the taxonomy matches how people actually talk.
6. **Agents are partners, not enemies.** They bring supply. The platform makes honest agents look good and dishonest ones visible.
7. **Server-rendered first.** HTMX + templ for almost everything; JavaScript only where it earns its place (the map, small interactions).
8. **Beachhead first.** Launch around KNUST (Ayeduase, Bomso, Kotei, Ayigya, Oduom, Kentinkrono), then Kumasi-wide, then Accra.

---

## 2. Technology stack

| Layer | Choice | Why |
| --- | --- | --- |
| Language | Go (latest stable) | Fast, simple deployment, great concurrency |
| HTTP router | `go-chi/chi` v5 | Idiomatic, stdlib-compatible, rich middleware |
| Templates | `a-h/templ` | Type-safe, component-based HTML — perfect with HTMX |
| Interactivity | HTMX 2.x + extensions (`sse`, `response-targets`, `preload`, `head-support`) | Server-driven UI with partial swaps |
| Small client state | Alpine.js | Dropdowns, bottom sheets, carousels, form toggles |
| Styling | Tailwind CSS v4 (standalone CLI — no Node required) | Design tokens, fast builds |
| Animation | View Transitions API (`transition:true` in HTMX), Tailwind transitions, `@starting-style`, Alpine `x-transition`, `@formkit/auto-animate` for lists | Smooth without a SPA |
| Maps | MapLibre GL JS + OpenStreetMap vector tiles (self-hosted Protomaps PMTiles for Ghana, or MapTiler) | Free/cheap, beautiful, no Google Maps bills |
| Routing & commute | Valhalla (self-hosted, Ghana OSM extract) | Drive/walk times and **isochrones** ("within 20 min of…") |
| Points of interest | OSM data imported into PostGIS (`osm2pgsql`) | Hospitals, markets, schools, trotro stations |
| Database | PostgreSQL 17 + **PostGIS** + `pg_trgm` + `pgvector` (later) | Geo queries, fuzzy search, embeddings |
| ORM | Ent (`entgo.io`) with **Atlas** versioned migrations | Typed CRUD, graph relations, privacy policies |
| Geo/search SQL | `sqlc` + `pgx` for hot geo queries | Ent can't express PostGIS well; sqlc keeps it typed |
| Background jobs | **River** (Postgres-backed Go job queue) | No extra broker; transactional enqueue |
| Sessions | `alexedwards/scs` with Postgres store | Secure cookie sessions, easy |
| Cache / rate limit | Valkey (Redis-compatible), added in Phase 6 | Hot search caching, rate limits at scale |
| Object storage | MinIO (dev) → Cloudflare R2 (prod) | S3-compatible, no egress fees |
| Images | libvips via `davidbyttow/govips` | WebP/AVIF variants, EXIF stripping, resizing |
| Video | FFmpeg worker → HLS, or Bunny Stream / Cloudflare Stream | Walkthrough videos |
| SMS / OTP | Hubtel, Arkesel or mNotify (abstracted behind an interface) | Local delivery, low cost |
| WhatsApp | WhatsApp Business Cloud API | Notifications, availability confirmation, OTP fallback |
| Email | Resend / Postmark / SES | Receipts, digests |
| Payments | Paystack (Mobile Money: MTN MoMo, Telecel Cash, AT Money + cards) with Hubtel as alternative | Ghana-native payment rails |
| Identity (KYC) | Smile ID (Ghana Card verification + selfie liveness) or similar | Landlord/agent identity verification |
| Realtime | Server-Sent Events + Postgres `LISTEN/NOTIFY` | Chat, notifications, live viewing updates |
| AI | Claude API (structured JSON output) | Natural-language search → filters, listing quality checks |
| Validation | `go-playground/validator`, `nyaruka/phonenumbers` | Input correctness, +233 E.164 phones |
| Security | `justinas/nosurf` (CSRF), `go-chi/httprate`, `microcosm-cc/bluemonday` | CSRF, rate limiting, HTML sanitising |
| Observability | `log/slog` JSON logs, OpenTelemetry, Prometheus, Grafana, Loki, Sentry | Know what's happening |
| Product analytics | PostHog (self-hosted) or Umami | Funnels, retention |
| Dev tooling | `air` (hot reload), `templ generate --watch`, Taskfile, golangci-lint | Fast loop |
| Testing | `testing`, `testify`, `testcontainers-go`, Playwright | Unit, integration, E2E |
| Edge / proxy | Caddy (auto-HTTPS) behind Cloudflare | TLS, compression, caching, DDoS |
| Hosting | Docker Compose on a VPS (Hetzner/DigitalOcean) to start; Kubernetes only if ever needed | Cheap, simple, sufficient for years |

**Key decision — Ent + PostGIS:** use Ent for all normal CRUD and relations; store `lat`/`lng` as float fields in Ent; add PostGIS `geography` columns via Atlas SQL migrations as *generated columns*; run search/geo queries through `sqlc`. You get typed models *and* full PostGIS power.

---

## 3. Architecture

A **modular monolith**: one Go binary for web, one for workers, shared domain packages. Split into services only if a real scaling need appears.

```
                    ┌──────────────── Cloudflare (CDN, WAF, cache) ───────────────┐
                    │                                                              │
 Browser / PWA ─────►  Caddy (TLS, gzip/zstd)                                      │
 (HTMX + MapLibre)  │        │                                                     │
                    │        ▼                                                     │
                    │  ┌──────────────┐    SSE     ┌──────────────┐                │
                    │  │  web (Go)    │◄──────────►│  browser     │                │
                    │  │ chi + templ  │                                            │
                    │  └──────┬───────┘                                            │
                    │         │ enqueue (same tx)                                  │
                    │         ▼                                                    │
                    │  ┌──────────────┐   ┌──────────┐  ┌───────────┐ ┌─────────┐  │
                    │  │ PostgreSQL   │◄──│ worker   │─►│ R2/MinIO  │ │ Valhalla│  │
                    │  │ PostGIS,     │   │ (River)  │  │ media     │ │ routing │  │
                    │  │ pg_trgm,     │   └────┬─────┘  └───────────┘ └─────────┘  │
                    │  │ pgvector     │        │                                   │
                    │  └──────────────┘        ├─► SMS / WhatsApp / Email          │
                    │                          ├─► Paystack webhooks                │
                    │                          ├─► Smile ID (KYC)                   │
                    │                          └─► Claude API                       │
                    └──────────────────────────────────────────────────────────────┘
```

---

## 4. Project structure

```
rentmap/
├── cmd/
│   ├── web/main.go              # HTTP server
│   ├── worker/main.go           # River workers + schedulers
│   ├── migrate/main.go          # Atlas migrations runner
│   └── seed/main.go             # Neighbourhoods, amenities, demo data
├── internal/
│   ├── config/                  # env parsing, feature flags
│   ├── app/                     # dependency wiring (container)
│   ├── server/
│   │   ├── router.go
│   │   └── middleware/          # auth, csrf, ratelimit, htmx, locale, dataSaver, recover, requestID
│   ├── modules/                 # one folder per domain (handler + service + repo)
│   │   ├── auth/                # OTP, sessions, passkeys, Google sign-in
│   │   ├── users/               # profiles, roles, settings
│   │   ├── verification/        # phone, ID (Ghana Card), property, authority
│   │   ├── properties/          # property, units, amenities
│   │   ├── listings/            # listings, pricing, fees, availability
│   │   ├── media/               # uploads, processing, blurhash, pHash
│   │   ├── geo/                 # neighbourhoods, POIs, fuzzing, commute
│   │   ├── search/              # map/bbox/radius/polygon/filters, saved searches
│   │   ├── viewings/            # slots, requests, reminders, feedback
│   │   ├── messaging/           # conversations, messages, scam detection
│   │   ├── trust/               # reports, trust score, duplicates, moderation
│   │   ├── reviews/             # landlord/agent/property reviews
│   │   ├── notifications/       # in-app, SMS, WhatsApp, email, push
│   │   ├── billing/             # plans, subscriptions, promotions, invoices
│   │   ├── payments/            # Paystack, webhooks, ledger
│   │   ├── pm/                  # property management: tenants, leases, rent, maintenance
│   │   ├── insights/            # rent index, fair-price model, landlord analytics
│   │   ├── ai/                  # NL search parser, listing assistant
│   │   └── admin/               # back-office
│   ├── ent/
│   │   ├── schema/              # Ent schemas (source of truth)
│   │   └── ...                  # generated
│   ├── db/
│   │   ├── queries/*.sql        # sqlc queries (geo search etc.)
│   │   └── sqlc/                # generated
│   ├── platform/                # adapters to the outside world
│   │   ├── storage/  sms/  whatsapp/  email/  paystack/  kyc/
│   │   ├── routing/  llm/  cache/  queue/  telemetry/
│   └── views/                   # templ
│       ├── layouts/             # base, app shell, dashboard, admin
│       ├── components/          # button, input, card, badge, sheet, modal, toast, skeleton
│       ├── pages/               # full pages
│       └── partials/            # HTMX fragments
├── web/
│   ├── css/app.css              # Tailwind entry + design tokens
│   ├── js/                      # map.js, upload.js, alpine components (small, ES modules)
│   ├── static/                  # built assets, icons, fonts, manifest, sw.js
│   └── embed.go                 # go:embed for single-binary deploys
├── migrations/                  # Atlas versioned SQL
├── deploy/
│   ├── docker/                  # Dockerfiles (web, worker)
│   ├── compose.dev.yml
│   ├── compose.prod.yml
│   └── caddy/Caddyfile
├── e2e/                         # Playwright tests
├── Taskfile.yml
└── .github/workflows/
```

**Handler rule:** every handler checks `HX-Request`. If present → render the partial; otherwise → render the full page wrapping the same partial. Every URL works when opened directly or shared on WhatsApp.

---

## 5. Data model

Money is stored as **integer pesewas** (`int64`). Times in UTC (Ghana is UTC+0, so display is trivial). IDs are UUIDv7 (sortable). Every table has `created_at`, `updated_at`; soft-delete where legally/operationally useful.

### 5.1 Identity & roles

| Entity | Key fields |
| --- | --- |
| `User` | id, phone (E.164, unique), email, name, avatar, status (active/suspended), locale, data_saver, last_seen_at |
| `RoleAssignment` | user, role (renter, landlord, agent, field_verifier, moderator, admin) — a user can hold several |
| `LandlordProfile` | user, display_name, bio, response_rate, response_time_median |
| `AgentProfile` | user, agency (optional), license_number (Real Estate Agency Council), service_areas, commission_policy |
| `Agency` | name, logo, members, verified |
| `Verification` | subject (user/property), type (phone, identity, property, authority, license), status, provider_ref, evidence media, reviewed_by, expires_at |
| `Session`, `OTPCode`, `Passkey` | auth plumbing |

### 5.2 Property supply

| Entity | Key fields |
| --- | --- |
| `Property` | owner (landlord), name, category (residential/commercial/hostel/mixed), description, lat, lng, **approx_lat, approx_lng**, digital_address (GhanaPostGPS, e.g. `AK-039-5028`), street, neighbourhood, city, region, year_built |
| `Unit` | property, unit_type (see taxonomy), bedrooms, bathrooms, size_sqm, floor, furnished (none/semi/full), self_contained, meter_type (prepaid own/prepaid shared/postpaid), water_source (GWCL/borehole/poly tank/mixed), kitchen (private/shared/none), parking, security (gated/watchman/CCTV), internet, AC, pets_allowed |
| `AgentMandate` | agent, property/unit, granted_by (landlord), scope, commission, valid_until, status — **who is authorised to list what** |
| `Listing` | unit, lister (landlord or agent), status (draft/pending_review/active/paused/rented/expired/removed), available_from, headline, description, **last_confirmed_at**, quality_score, trust_score, views_count, promoted_until |
| `ListingTerms` | listing, rent_amount, rent_period (month/year/semester/academic_year), **monthly_equivalent** (stored, derived — used by all price filters), **advance_months_required**, deposit, agent_fee, viewing_fee, service_charge, other_fees (JSON with labels), negotiable, minimum_lease_months |
| `Amenity` + `UnitAmenity` | controlled vocabulary with icons |
| `Media` | owner, listing/unit/property, kind (photo/video/floorplan/document), storage_key, variants (JSON), width, height, blurhash, **phash**, captured_lat/lng (from in-app capture, private), sort_order, is_cover, moderation_status |
| `AvailabilityConfirmation` | listing, confirmed_by, channel (web/SMS/WhatsApp), confirmed_at |

**Unit type taxonomy (Ghana):** single room, single room self-contained, chamber and hall, chamber and hall self-contained, 1/2/3/4+ bedroom apartment, detached house, semi-detached, townhouse, boys' quarters, hostel room (1-in-1, 2-in-1, 4-in-1), compound house room, shop, store/warehouse, office, event space, land (later).

### 5.3 Geography

| Entity | Key fields |
| --- | --- |
| `Neighbourhood` | name, slug, city, **boundary (polygon)**, centroid, aliases (e.g. "Tech", "Kotei junction") |
| `POI` | name, category (university, hospital, market, supermarket, school, trotro station, church, mosque, bank, fuel), point, source (OSM/manual) |
| `Place` (user-saved) | user, label ("Work", "Campus"), point — used for commute search |
| `NeighbourhoodReport` | user, neighbourhood, water_days_per_week, power_reliability (1–5), flooding, safety, noise, road_condition — crowd-sourced |

### 5.4 Renter activity

`SavedListing`, `Collection` (named lists, shareable), `SavedSearch` (filters + geometry + alert frequency), `Comparison`, `ListingView` (event log), `SearchEvent`, `PriceAlert`.

### 5.5 Interaction

| Entity | Key fields |
| --- | --- |
| `ViewingSlot` | lister, listing (or all listings), weekday/time window or specific datetime, capacity |
| `ViewingRequest` | listing, renter, requested_at, proposed_times, confirmed_time, status (requested/proposed/confirmed/declined/cancelled/completed/no_show_renter/no_show_lister), viewing_fee_snapshot, **exact_location_unlocked_at** |
| `ViewingFeedback` | viewing, attended, interested, accuracy_rating (1–5), accuracy_issues (JSON), comment |
| `Conversation` | listing, participants |
| `Message` | conversation, sender, body, attachments, flags (scam_risk, contains_phone), read_at |
| `Report` | reporter, target (listing/user/message), reason (scam, already rented, wrong price, fake photos, harassment, duplicate), details, status, resolution |
| `Review` | author, subject (landlord/agent/property), viewing or lease (proof of interaction required), ratings, text, response |
| `DuplicateCandidate` | listing_a, listing_b, geo_distance, image_similarity, text_similarity, score, status |
| `Notification` | user, kind, payload, channels_sent, read_at |

### 5.6 Money

`Plan`, `Subscription`, `Promotion` (listing, area/neighbourhood, start, end, price), `Invoice`, `Payment` (provider, reference, amount, status, raw_webhook), `LedgerEntry` (double-entry for anything held or split), `Payout`.

### 5.7 Property management (Phase 9)

`Tenant`, `Lease` (unit, tenant, start, end, rent, advance_paid, deposit, terms document), `RentSchedule`, `RentCharge`, `RentPayment`, `Receipt`, `MaintenanceRequest` (photos, priority, status, assigned_to), `Expense`, `Inspection` (move-in/move-out checklist with photos), `Document`.

### 5.8 Governance

`AuditLog` (actor, action, target, before/after, IP), `FeatureFlag`, `ModerationAction`, `ConsentRecord` (data-protection consent, marketing opt-in).

### 5.9 Sample Ent schema (Listing)

```go
// internal/ent/schema/listing.go
package schema

import (
    "entgo.io/ent"
    "entgo.io/ent/schema/edge"
    "entgo.io/ent/schema/field"
    "entgo.io/ent/schema/index"
    "github.com/google/uuid"
)

type Listing struct{ ent.Schema }

func (Listing) Mixin() []ent.Mixin { return []ent.Mixin{TimeMixin{}} }

func (Listing) Fields() []ent.Field {
    return []ent.Field{
        field.UUID("id", uuid.UUID{}).Default(func() uuid.UUID { return uuid.Must(uuid.NewV7()) }),
        field.Enum("status").
            Values("draft", "pending_review", "active", "paused", "rented", "expired", "removed").
            Default("draft"),
        field.String("headline").MaxLen(120),
        field.Text("description"),
        field.Time("available_from"),
        field.Time("last_confirmed_at").Optional().Nillable(),
        field.Int("quality_score").Default(0),
        field.Int("trust_score").Default(0),
        field.Time("promoted_until").Optional().Nillable(),
    }
}

func (Listing) Edges() []ent.Edge {
    return []ent.Edge{
        edge.From("unit", Unit.Type).Ref("listings").Unique().Required(),
        edge.From("lister", User.Type).Ref("listings").Unique().Required(),
        edge.To("terms", ListingTerms.Type).Unique(),
        edge.To("media", Media.Type),
        edge.To("viewings", ViewingRequest.Type),
        edge.To("confirmations", AvailabilityConfirmation.Type),
    }
}

func (Listing) Indexes() []ent.Index {
    return []ent.Index{index.Fields("status", "last_confirmed_at")}
}
```

### 5.10 PostGIS migration (Atlas SQL)

```sql
CREATE EXTENSION IF NOT EXISTS postgis;
CREATE EXTENSION IF NOT EXISTS pg_trgm;

ALTER TABLE properties
  ADD COLUMN geog geography(Point, 4326)
    GENERATED ALWAYS AS (ST_SetSRID(ST_MakePoint(lng, lat), 4326)::geography) STORED,
  ADD COLUMN approx_geog geography(Point, 4326)
    GENERATED ALWAYS AS (ST_SetSRID(ST_MakePoint(approx_lng, approx_lat), 4326)::geography) STORED;

CREATE INDEX properties_geog_gist        ON properties USING GIST (geog);
CREATE INDEX properties_approx_geog_gist ON properties USING GIST (approx_geog);
CREATE INDEX neighbourhoods_name_trgm    ON neighbourhoods USING GIN (name gin_trgm_ops);
```

---

## 6. The hard technical designs

### 6.1 Approximate vs exact location (privacy done right)

Blurring on the client is useless — anyone can read the page source. Rules:

1. At property creation, compute an **approximate point**: a deterministic offset of 150–400 m in a pseudo-random direction, seeded by `HMAC(secret, property_id)`. Deterministic means the offset never changes, so nobody can average many requests to find the real spot.
2. Only recompute if the exact point moves more than \~50 m.
3. Public endpoints (map GeoJSON, pages, OG tags, sitemap) **only ever read `approx_*` columns**. Enforce with an Ent privacy policy / separate query functions, plus a test that greps rendered HTML for exact coordinates.
4. The map shows a soft circle (\~300 m radius) rather than a pin for the listing detail.
5. Exact location and navigation ("Open in Google Maps / Waze") unlock only when `ViewingRequest.status = confirmed` for that renter, and it's logged.
6. Distances shown publicly ("1.3 km from KNUST") are computed from the **approximate** point and rounded (100 m under 1 km, 0.5 km above). Computing them from the exact point — even rounded to 100 m — lets anyone intersect distances to several POIs and recover the location more precisely than the 150–400 m offset hides it.
7. Photos: EXIF stripped on upload; any geotag from in-app capture is kept privately for verification only.

### 6.2 Map discovery + list, kept in sync with HTMX

- MapLibre renders the map. On `moveend` (debounced 250 ms), `map.js` calls `htmx.ajax('GET', '/search/results?bbox=...&filters...', '#results')` and fetches markers from `/search/markers.geojson` with the same query string.
- Result list = server-rendered templ partial; markers = GeoJSON (the one place JSON is right).
- `hx-push-url` keeps the URL as the source of truth: every search is shareable and back-button safe.
- Hovering a list card highlights the marker (custom event); tapping a marker opens a **bottom sheet** on mobile / side panel on desktop, loaded via `hx-get="/listings/{id}/card"`.
- Price pills as markers (`GHS 1,200`) with clustering: MapLibre's built-in GeoJSON clustering to \~5,000 points; beyond that, serve **vector tiles from PostGIS** (`ST_AsMVT`) with server-side grid clustering.
- "Search this area" button appears after panning instead of auto-refreshing on every move (option in settings).

### 6.3 Search queries (sqlc)

```sql
-- name: SearchListingsInBBox :many
SELECT l.id, l.headline, t.rent_amount, t.rent_period, t.advance_months_required,
       p.approx_lat, p.approx_lng, u.unit_type, u.bedrooms,
       l.last_confirmed_at, l.trust_score
FROM listings l
JOIN units u           ON u.id = l.unit_id
JOIN properties p      ON p.id = u.property_id
JOIN listing_terms t   ON t.listing_id = l.id
WHERE l.status = 'active'
  AND p.approx_geog && ST_MakeEnvelope(@min_lng, @min_lat, @max_lng, @max_lat, 4326)::geography
  AND (sqlc.narg(max_monthly)::bigint IS NULL OR t.monthly_equivalent <= sqlc.narg(max_monthly))
  AND (sqlc.narg(unit_types)::text[] IS NULL OR u.unit_type = ANY(sqlc.narg(unit_types)))
  AND (sqlc.narg(min_bedrooms)::int IS NULL OR u.bedrooms >= sqlc.narg(min_bedrooms))
ORDER BY (l.promoted_until > now()) DESC NULLS LAST, l.trust_score DESC, l.last_confirmed_at DESC
LIMIT @lim OFFSET @off;
```

Variants: **radius** (`ST_DWithin(p.approx_geog, @point, @meters)`), **drawn polygon** (`ST_Within(p.approx_geog::geometry, ST_GeomFromGeoJSON(@polygon))`) — public filters must use `approx_geog`, otherwise repeated shrinking radius/polygon queries reveal the exact point (see §16), **neighbourhood** (`ST_Within` against stored boundary), **commute isochrone** (polygon from Valhalla, cached per place+mode+minutes).

Add a stored `monthly_equivalent` column on `listing_terms` so price filters are consistent across monthly and yearly rents. Text search: `pg_trgm` on neighbourhood/landmark aliases ("Tech junction", "Kotei") + Postgres FTS on headline/description.

### 6.4 Total move-in cost

Computed in one Go function used everywhere (card, detail page, compare, alerts):

```
rent × advance_months_required
+ deposit
+ agent_fee
+ service_charge (upfront portion)
+ other_fees
= total_to_move_in
```

Shown with a breakdown, a "per month equivalent", and an optional **affordability check** ("with a monthly income of X, this is Y% of income"). Listings that don't fill in fees can't be published — honesty is enforced by the form.

### 6.5 Media pipeline

1. Browser requests a **presigned upload URL** → uploads directly to R2/MinIO (the Go server never buffers big files).
2. Client-side compression before upload (canvas resize to \~2000 px) saves the lister's data.
3. River job: validate type → strip EXIF → generate variants (320, 640, 1280, 2000 px; AVIF + WebP + JPEG fallback) → compute **blurhash** (instant placeholders) and **perceptual hash** (duplicate detection) → moderation check.
4. `<picture>` with `srcset`; data-saver mode serves only the 320/640 variants and no autoplay video.
5. Video: FFmpeg → 480p/720p HLS + poster frame, or offload to Bunny/Cloudflare Stream.
6. **In-app "verified capture" mode**: photos taken through the app camera record GPS and timestamp privately; if they match the property location, the listing gets a "Photos taken on site" signal.

### 6.6 Authentication

- **Phone OTP first** (most users have a phone, not all use email). 6 digits, 5-minute TTL, hashed at rest, rate-limited per phone and per IP, WhatsApp fallback when SMS is slow.
- Optional Google sign-in and **passkeys** (`go-webauthn`) for returning users.
- Renters can browse, save (stored in a signed cookie) and compare **without an account**; the account is requested only at "Request viewing" or "Message", and anonymous favourites merge on sign-up.
- Sessions via `scs`, `HttpOnly`, `Secure`, `SameSite=Lax`, rotated on login and role change.

### 6.7 Availability freshness engine

| State | Rule | Display |
| --- | --- | --- |
| Fresh | confirmed ≤ 3 days | 🟢 Available · confirmed 2 hours ago |
| Ageing | 3–14 days | 🟠 Not recently confirmed |
| Paused | > 14 days | Hidden from search; lister notified |
| Expired | > 30 days paused | Archived |

A scheduled River job sends a **one-tap confirmation** via WhatsApp/SMS: *"Is your 2-bed at Oduom still available? Tap: Yes / Rented / Pause"* — signed short links, no login needed. Renter reports of "already rented" instantly move the listing to Ageing and ask the lister to confirm.

### 6.8 Viewing system

1. Lister sets weekly availability windows (e.g. Sat 9–13) or specific slots.
2. Renter picks a slot; the viewing fee (if any) is shown again and must be acknowledged.
3. Lister accepts / proposes a new time / declines (with reason).
4. On confirm: exact location unlocks, calendar file (`.ics`) attached, reminders at 24 h and 2 h via WhatsApp/SMS.
5. "On my way" and "I've arrived" buttons (live status via SSE).
6. After the slot: feedback prompt (attended? interested? accurate?). Accuracy ratings feed the listing's trust score; repeated "not as described" triggers moderation.
7. No-show tracking both ways — reliability shows on profiles.
8. **Viewing Day Planner** (Phase 6): book several viewings in one area and get an optimised route and timetable.

### 6.9 Messaging & scam shield

- Conversations tied to a listing; realtime via SSE; unread counts in the nav.
- Phone numbers are hidden until a viewing is confirmed (configurable by lister).
- **Scam detection rules** on each message: requests to "send MoMo", pay before viewing, pay a "booking fee", move to another platform, pressure language. Flagged messages show a gentle inline warning to the renter: *"Never pay rent or fees before you've seen the property and met the owner or verified agent."*
- Report button on every message; moderators see context.

### 6.10 Trust score (0–100)

Weighted sum of: identity verified, phone verified, property/authority verified, agent license verified, mandate from landlord, account age, response rate, viewing accuracy ratings, reports upheld (negative), duplicate flags (negative), on-site photos, availability freshness. Shown to renters as simple badges, not a raw number. Used for ranking and for auto-holding suspicious new listings for review.

### 6.11 Duplicate detection

Candidate generation: listings within 150 m of each other → compare **pHash Hamming distance** on photos (≤ 10 bits is a likely match), text similarity (trigram), same unit attributes, same phone. Score > threshold → `DuplicateCandidate` for admin. Resolution options: merge into one canonical listing with **multiple authorised agents shown**, or remove the unauthorised one. Price discrepancies between agents for the same unit are themselves a signal worth showing ("Also listed by 2 other agents").

### 6.12 Notifications

One `Notify(user, kind, payload)` API; a preferences matrix per user × kind × channel (in-app, WhatsApp, SMS, email, web push). Quiet hours. Digest batching for saved-search alerts. All sends are River jobs with retries and idempotency keys. Costs tracked per channel.

### 6.13 HTMX conventions

- `hx-boost` on the app shell for SPA-like navigation.
- `hx-indicator` + skeleton components on every async region.
- `HX-Trigger` response headers to fire toasts (`showToast`), refresh counters, close modals.
- `response-targets` extension to route 4xx/5xx into inline error regions.
- Forms validate server-side; on error, re-render the form partial with messages (field-level `aria-invalid`).
- `hx-preload` on listing cards (hover/touchstart) for instant-feeling detail pages.
- `hx-swap="... transition:true"` for View Transitions between list → detail with shared-element image morph (`view-transition-name: listing-{id}`).
- CSRF token injected via `hx-headers` on `<body>`.

### 6.14 Design system & motion

- **Tokens** in `app.css` (`@theme`): brand palette **Sky Mint `#B8F7E4`** + **Graphite `#25272C`** with derived scales (full spec, contrast rules and usage in **§15**), semantic colours (success/warn/danger/verified), radii, shadows, spacing, type scale.
- Fonts: one variable sans (e.g. Plus Jakarta Sans or Inter) self-hosted, `font-display: swap`.
- Components (templ): Button, IconButton, Input, Select, Chip/FilterPill, Badge (Verified, Owner, Agent, Fresh), Card, ListingCard, PriceTag, BottomSheet, Modal, Drawer, Tabs, Toast, Skeleton, EmptyState, Stepper, Avatar, RatingStars, MapPin, Gallery/Lightbox.
- Motion: 150–250 ms ease-out for UI, spring-like for bottom sheets, staggered fade-in for results, heart "pop" on favourite, subtle confetti when a listing is rented or a viewing confirmed. Everything respects `prefers-reduced-motion`.
- Dark mode via `class` strategy.
- Accessibility: WCAG 2.2 AA contrast, focus rings, keyboard-operable map alternative (the list), 44 px touch targets, ARIA on dynamic regions (`aria-live` for result counts).

### 6.15 Performance budget

| Metric | Target |
| --- | --- |
| TTFB (Kumasi, 4G) | \< 300 ms |
| LCP on mid-range Android | \< 2.5 s |
| JS on non-map pages | \< 50 KB gzipped (HTMX + Alpine) |
| Listing card image | \< 30 KB (AVIF/WebP, 640 px) |
| Search query p95 | \< 80 ms |

Tactics: Brotli/zstd, immutable hashed assets, Cloudflare edge caching for anonymous pages, lazy-load MapLibre only on map views, HTTP cache headers, DB indexes checked with `EXPLAIN ANALYZE`, connection pooling (`pgxpool`).

---

## 7. Build phases

Each phase ends with a demo-able, deployable increment. Rough durations assume one strong full-stack engineer part-time-to-full-time; compress with a team.

### Phase 0 — Foundation & validation (2–3 weeks)

**Goal:** prove demand cheaply and set up the engineering spine.

Product & market:

- [ ] Interview 15–20 renters (students and workers around KNUST) and 10 landlords/agents: pain points, current fees, how they find places, willingness to list.
- [ ] Map the local fee landscape (typical advance, viewing fees, agent commission) per neighbourhood.
- [ ] Recruit 5–10 **founding landlords/agents** willing to list at launch.
- [ ] Final name, domain, WhatsApp Business number, social handles.
- [ ] Legal groundwork (see §9): data-protection registration, terms, privacy policy draft.

Design:

- [ ] Low-fi wireframes: map home, listing card, detail, filters, viewing flow, landlord listing wizard, dashboard.
- [ ] Design tokens and component inventory in Figma.

Engineering:

- [ ] Repo, Go module, folder structure from §4.
- [ ] Docker Compose dev: Postgres+PostGIS, MinIO, Mailpit, Valhalla (can come later).
- [ ] `air` + `templ --watch` + Tailwind CLI watcher in one `task dev`.
- [ ] Ent + Atlas wired; first migration with extensions.
- [ ] sqlc wired with pgx.
- [ ] Config via env, slog logging, request IDs, graceful shutdown, `/healthz` and `/readyz`.
- [ ] Base layout, design tokens, first 10 components.
- [ ] CI: build, lint, test, templ/sqlc/ent drift check (fail if generated code is stale).
- [ ] Staging environment on a VPS with Caddy + Cloudflare.

**Exit:** `task dev` boots everything; a styled "coming soon + join waitlist" page is live on staging and collecting phone numbers.

---

### Phase 1 — Identity, roles & accounts (2–3 weeks)

- [x] Phone OTP sign-in/up (SMS provider adapter + mock provider for dev). *Providers: Mailpit (dev), log (tests), Africa's Talking sandbox/live.*
- [x] Sessions, logout, device list, "log out everywhere".
- [x] Role selection on onboarding ("I'm looking for a place" / "I own property" / "I'm an agent") — roles addable later.
- [x] Profiles: renter, landlord, agent (+ agency). *Agency is a name on the agent profile until team accounts need an `Agency` entity.*
- [x] Phone verification badge (automatic from OTP).
- [x] Identity verification flow: Ghana Card + selfie via KYC provider (or manual upload + moderator review as v1 fallback). *Manual review v1; `method` field ready for Smile ID.*
- [x] Agent licence number capture + manual verification queue.
- [x] Authorisation layer: Ent privacy rules + middleware (`RequireRole`, `RequireVerified`). *Privacy rules on `verifications` and `verification_files`; `RequireVerified` ready for Phase 2 publishing.*
- [x] Audit log for sensitive actions.
- [x] Rate limiting on auth endpoints; OTP abuse protection.
- [x] Account settings: name, photo, notification preferences skeleton, data saver toggle, delete account.

**Exit:** a landlord can sign up with a phone, submit ID, and be verified by an admin.

---

### Phase 2 — Properties, units & listings (3–4 weeks)

- [x] **Listing wizard** (multi-step HTMX form with autosave drafts): location → property → unit details → amenities → photos/video → pricing & terms → availability → review & publish. *Photos step added in slice 2b (video to follow); availability lives in the details step.*
- [x] Location step: drop a pin on the map, "use my current location", GhanaPostGPS digital address field, landmark description ("Behind Ayigya Zongo mosque").
- [x] Deterministic approximate location generation (§6.1).
- [x] Unit taxonomy and amenities seeded. *Defined in Go (`listings/taxonomy.go`); amenities stored as a JSONB array on the unit.*
- [x] Media upload with presigned URLs, drag-to-reorder, cover selection, client-side compression, progress bars. *Photos upload through the server, one per request after shrinking on the phone (not presigned; see §16.9). Walk-through video is the next slice.*
- [x] Media worker: variants, EXIF strip, blurhash, pHash. *Processed in the request (bounded to 2 at a time), not a River job yet; JPEG 320/800/1600.*
- [x] Pricing & terms with mandatory fee disclosure; live **move-in cost preview** while typing.
- [x] Properties with multiple units (hostels, apartment blocks, compound houses) — one property, many units, many listings.
- [ ] Agent listing on behalf of a landlord: mandate request → landlord approves via SMS/WhatsApp link. Unmandated agent listings are allowed but labelled "Agent listing — owner authority not confirmed".
- [x] Listing statuses & transitions (state machine in Go, tested).
- [x] Listing quality score (photo count, description length, fees completed, video present) with tips: *"Add 3 more photos to get 2× more views"*.
- [x] New listings from unverified accounts go to `pending_review`. *Moderator queue at `/admin/listings`.*

**Exit:** 20+ real listings from founding landlords in the database with photos and full terms.

---

### Phase 3 — Map discovery, search & listing pages (3–4 weeks)

- [ ] Map home (MapLibre + Ghana tiles), price-pill markers, clustering, user location.
- [ ] Split view on desktop (map + list), toggle on mobile (map ↔ list) with a floating button.
- [ ] Bottom-sheet listing preview on marker tap.
- [ ] Filters: price (monthly equivalent) range slider, unit type, bedrooms, bathrooms, furnished, self-contained, meter type, water source, parking, security, kitchen, AC, pets, commercial types, available from, verified only, owner-only.
- [ ] Sort: recommended, newest, price ↑/↓, nearest, recently confirmed.
- [ ] Location search box with autocomplete (neighbourhoods, landmarks, POIs, universities).
- [ ] Radius search ("within 3 km of KNUST").
- [ ] URL-driven state; shareable searches.
- [ ] Listing detail page: gallery + lightbox, video, key facts grid, amenities, **move-in cost breakdown**, approximate-area map, distances to nearby POIs (rounded), lister card with badges, freshness badge, similar listings nearby.
- [ ] Rich OpenGraph tags + generated share image (photo + price + area) so WhatsApp previews look great.
- [ ] Anonymous favourites (cookie) and compare tray (up to 4).
- [ ] SEO pages: `/kumasi/ayeduase/rooms-for-rent`, `/kumasi/knust/hostels` — server-rendered, indexable, with structured data.
- [ ] Empty states that help ("No self-contained rooms under GHS 800 here — widen the area?" with one-tap actions).

**Exit:** a renter can find, filter, inspect and shortlist real properties without calling anybody.

---

### Phase 4 — Contact, viewings, availability & trust basics (3–4 weeks)

- [ ] Account gate at first contact; anonymous favourites merge.
- [ ] Messaging (SSE realtime, read receipts, unread badges, attachment support).
- [ ] Scam-shield rules + inline warnings (§6.9).
- [ ] Viewing slots, requests, accept/propose/decline, `.ics`, reminders (§6.8).
- [ ] Exact location unlock on confirmation + "Navigate" deep links.
- [ ] Post-viewing feedback prompts.
- [ ] Availability freshness engine + one-tap WhatsApp/SMS confirmations (§6.7).
- [ ] "Mark as rented" flow (with optional "Rented through RentMap?" question — your key success metric).
- [ ] Report listing / user / message flows.
- [ ] Notification centre (in-app) + SMS/WhatsApp/email channels + preferences.
- [ ] Trust score v1 and badges.

**Exit:** end-to-end: discover → message → book viewing → attend → feedback → mark rented.

---

### Phase 5 — Dashboards, admin & MVP launch (2–3 weeks)

Landlord/agent dashboard:

- [ ] Overview: active listings, views, saves, messages, viewing requests, response time.
- [ ] Listings table with quick actions (pause, confirm availability, edit price, mark rented).
- [ ] Viewing calendar (week view).
- [ ] Inbox.
- [ ] Per-listing stats (views/day, saves, contact rate).
- [ ] Agent: mandates, clients (landlords), leads pipeline.

Admin back-office:

- [ ] Verification queue (ID, property, licence) with evidence viewer.
- [ ] Moderation queue (new listings, flagged media, flagged messages, reports).
- [ ] Duplicate candidates review (manual matching v1).
- [ ] Users & roles management, suspensions, audit log viewer.
- [ ] Neighbourhood & POI editor (draw polygons).
- [ ] Platform metrics dashboard.
- [ ] Impersonation ("view as user") with audit logging for support.

Launch readiness:

- [ ] Production environment, backups (daily `pg_dump` + WAL archiving with pgBackRest, tested restore), uptime monitoring, Sentry alerts.
- [ ] Terms, privacy policy, community guidelines, "How we verify" page, safety tips page.
- [ ] PWA manifest + installable icon.
- [ ] Load test search endpoints (k6).
- [ ] Soft launch around KNUST: campus ambassadors, WhatsApp groups, flyers at junctions, founding-landlord onboarding days. Time it before the academic year/semester intake.

**Exit (MVP live):** ≥ 150 active listings in the beachhead, ≥ 500 registered renters, first rentals attributed to the platform.

---

### Phase 6 — Delight & growth (4–6 weeks)

- [ ] **Draw-to-search**: draw a polygon/circle on the map; show count and price range live.
- [ ] **Commute search**: save "Work"/"Campus"; filter "≤ 20 min by car / walking"; isochrone overlay from Valhalla; commute time on every card.
- [ ] Saved searches with alerts (instant / daily digest) via WhatsApp/email/push.
- [ ] Price-drop and "back on market" alerts on favourites.
- [ ] Collections: named shortlists shareable with family/roommates ("Mum, look at these 3") — view without an account, vote with 👍.
- [ ] Compare page: side-by-side facts, move-in cost, commute, trust.
- [ ] **Viewing Day Planner** with optimised route.
- [ ] Neighbourhood pages with crowd reports: water days per week, power reliability, flooding, safety, road condition, plus nearby POIs and price ranges.
- [ ] Reviews (only after a completed viewing or lease), with lister responses.
- [ ] Web push notifications; full PWA with offline favourites and recently viewed.
- [ ] Referral programme (renters invite friends; landlords invite landlords → free boost).
- [ ] Landlord response-time badge ("Usually replies within 1 hour").
- [ ] Valkey for hot-search caching and distributed rate limiting.
- [ ] Twi-language UI strings for key flows (i18n framework ready from Phase 0).

**Exit:** measurable lift in return visits and saved-search alert click-throughs.

---

### Phase 7 — Monetisation & payments (4–5 weeks)

- [ ] Plans: Free landlord (e.g. up to 2 active listings), Pro landlord, Agent Pro, Agency — limits enforced in services, not templates.
- [ ] Paystack integration: Mobile Money + card, subscriptions, webhooks with signature verification, idempotent processing, reconciliation job.
- [ ] Double-entry ledger for all money movements.
- [ ] Promoted listings: pay to boost within a neighbourhood for N days; clearly labelled "Promoted"; ranking still requires minimum trust score.
- [ ] Paid enhanced verification (field visit by a RentMap verifier) — badge states exactly what was checked and when.
- [ ] Field-verifier mobile flow: assigned visits, on-site photo capture with GPS, checklist, submit.
- [ ] Invoices & receipts (PDF), VAT/levy handling per Ghana tax rules (confirm with an accountant).
- [ ] Optional **viewing fee through the platform**: held and released to the agent after the viewing happens; refunded automatically if the agent no-shows. (Check licensing/escrow rules — may need to use the payment provider's split/hold features rather than holding funds yourself.)
- [ ] Billing dashboard: plan, usage, invoices, payment methods.

**Exit:** recurring revenue from ≥ 20 paying landlords/agents; zero unreconciled payments.

---

### Phase 8 — Intelligence (4–6 weeks, can overlap)

- [ ] **Natural-language search**: *"self-contained room near KNUST under 1,000 a month with parking"* → Claude API returns a strict JSON filter object (validated against a Go struct; unknown fields rejected) → normal search runs. Show the parsed filters as editable chips so users stay in control. Summary line: *"23 found · 8 match everything · 5 within 10 min of KNUST."*
- [ ] **Listing assistant** for landlords: turn rough notes/voice note transcripts into a clean description; flag missing information; suggest photo improvements.
- [ ] **Duplicate detection v2**: automated pHash + geo + text scoring (§6.11), auto-merge suggestions.
- [ ] **Fraud scoring**: new account + low price for area + stock-looking photos + pay-first language + many listings in 24 h → hold for review.
- [ ] **Fair-price indicator**: hedonic regression per city (rent \~ unit type + bedrooms + amenities + neighbourhood + distance to key POIs + meter/water type), refitted weekly. On the detail page: *"Priced about 12% below similar places nearby."* Publish only when the model has enough data for that segment; show confidence honestly.
- [ ] **RentMap Rent Index**: monthly median asking rent by neighbourhood and unit type, with trend charts — public pages (great for press and SEO) and a landlord insights view ("Similar 2-beds in Oduom rent for GHS 1,300–1,700").
- [ ] Semantic search / "more like this" with `pgvector` embeddings of listing text + image features.
- [ ] Personalised ranking from saves, views and feedback (start with simple heuristics; don't over-engineer).

**Exit:** NL search used in ≥ 15% of sessions; fraud holds catching the majority of later-confirmed scams.

---

### Phase 9 — Property management SaaS (6–8 weeks)

The marketplace acquires renters; this retains landlords.

- [ ] Portfolio: properties → units → occupancy view (vacant/occupied/notice given).
- [ ] "Convert viewing to tenant": when a listing is rented, create the tenant and lease in one step.
- [ ] Tenants: profiles, documents, emergency contacts, tenant portal login.
- [ ] Leases: terms, start/end, advance paid, deposit, renewal reminders, e-signature (simple typed/drawn signature with audit trail), downloadable PDF agreement from templates.
- [ ] Rent schedules for monthly and multi-year advance models; charges, partial payments, arrears.
- [ ] Rent collection via Mobile Money payment links; automatic receipts via WhatsApp/email.
- [ ] Reminders to tenants (friendly, configurable).
- [ ] Maintenance requests from tenants with photos, priority, status, artisan assignment, cost logging.
- [ ] Expenses and simple financial reports: income, expenses, net per property, occupancy rate, arrears aging; export CSV/PDF.
- [ ] **Move-in / move-out inspection reports** with timestamped photos, signed by both parties — protects the deposit and prevents disputes.
- [ ] Utility tracking (meter readings for shared meters, water bills split).
- [ ] Caretaker role (limited access for the person who manages the compound day-to-day).
- [ ] Tenant rental history / reference (opt-in, tenant-controlled) — helps good tenants get their next place.
- [ ] Automatic re-listing: when a tenant gives notice, one tap puts the unit back on the marketplace.

**Exit:** ≥ 50 landlords managing ≥ 300 units; monthly active landlord retention ≥ 70%.

---

### Phase 10 — Scale & expansion (ongoing)

- [ ] Accra launch (East Legon, Madina, Adenta, Spintex, Tema, Kasoa…), then Takoradi, Cape Coast, Tamale, Ho.
- [ ] Vector-tile serving from PostGIS for national-scale maps.
- [ ] Read replica for search; partition event tables by month.
- [ ] Public partner API (agencies syncing their inventory) with API keys and webhooks.
- [ ] Native wrappers (Capacitor) if app-store presence becomes important; the PWA carries most of the value.
- [ ] Roommate / shared-housing matching.
- [ ] Short-stay / furnished monthly rentals.
- [ ] Commercial-property specialisation (shops, offices, warehouses with footfall and road-access data).
- [ ] Partnerships: universities (off-campus housing office), employers relocating staff, movers, furniture sellers, internet providers, banks (rent-advance loans — refer only; careful with lending regulation).
- [ ] USSD / SMS search for feature-phone users (stretch goal).

---

## 8. The "loving" layer — details people remember

These are small to build and disproportionately loved. Many slot into Phases 3–6.

1. **Total move-in cost everywhere** — the number people actually worry about.
2. **"Last confirmed 2 hours ago"** — honesty made visible.
3. **Beautiful WhatsApp previews** — every share is an advert.
4. **Share a shortlist with family** — no account needed to view and vote.
5. **Viewing Day Planner** — see 5 places in one morning, in the right order.
6. **Water & light reality check** — crowd-sourced water days and power reliability per area.
7. **Data saver mode** — small images, no autoplay, shows "\~0.4 MB for this page".
8. **"On my way" button** during viewings — no awkward calls.
9. **Scam shield** — calm, well-timed warnings, not scary banners everywhere.
10. **Fair-price indicator** — renters feel protected, honest landlords look good.
11. **Commute time on every card** once "Work"/"Campus" is saved.
12. **Move-in inspection photos** — deposit disputes solved before they start.
13. **Landlord tips that increase results** — "Listings with video get 3× more viewing requests."
14. **Warm micro-interactions** — heart pop, confetti on "Rented!", friendly empty states, human copy ("Akwaaba, Gideon" style greetings using the user's name).
15. **Semester mode** around campuses — highlight hostels and rooms available for the coming academic year.
16. **Recently viewed** and **"You looked at this 3 times"** gentle nudges.
17. **Safety centre** — how to view safely, what a real agent will never ask for, how to report.
18. **Honest badges** — each badge opens a sheet saying exactly what was checked, by whom, and when.

---

## 9. Security, privacy & legal

**Security**

- CSRF on all state-changing requests; `SameSite=Lax` cookies; strict CSP (nonce-based for inline scripts, MapLibre workers allowed).
- Rate limits: auth, search, messaging, reporting, uploads.
- Presigned uploads with content-type/size limits; server re-validates file magic bytes.
- Sanitise all user HTML (descriptions allow only minimal formatting).
- Authorisation tested per endpoint (table-driven tests: role × action).
- Secrets via environment/secret manager, never in the repo; dependency scanning (`govulncheck`) in CI.
- Admin behind 2FA/passkeys and IP allow-list.
- Backups encrypted; restore drill quarterly.
- Webhook signature verification and replay protection.

**Privacy**

- Exact location protection (§6.1); EXIF stripping; phone masking.
- Data minimisation: collect only what each feature needs.
- User data export and account deletion.
- Retention policy (e.g. messages 2 years, KYC evidence per provider/legal requirements).

**Legal (confirm current requirements with a Ghanaian lawyer)**

- **Data Protection Act, 2012 (Act 843)** — register with the Data Protection Commission as a data controller; consent records; privacy notice.
- **Real Estate Agency Act, 2020 (Act 1047)** — agent licensing through the Real Estate Agency Council; use licence verification as part of agent badges.
- **Rent Act, 1963 (Act 220)** and any newer rent legislation — rules on rent advance and rent control; consider showing a neutral "Know your rights" info link rather than making legal judgements on listings.
- Payment regulation (Bank of Ghana) — avoid holding customer funds yourself; rely on licensed providers' split/hold features.
- Terms of service, acceptable-use policy, listing guidelines, dispute process, takedown process.
- Business registration (Office of the Registrar of Companies) and tax registration.

---

## 10. Testing & quality

| Level | What | Tools |
| --- | --- | --- |
| Unit | move-in cost, fuzzing, state machines, trust score, scam rules, filter parsing | `testing`, `testify` |
| Integration | repositories, sqlc geo queries, Ent privacy rules, River jobs | `testcontainers-go` with PostGIS image |
| HTTP | handlers return correct partial vs full page, auth/role checks | `httptest` |
| Contract | SMS/WhatsApp/Paystack/KYC adapters against recorded fixtures | fakes + golden files |
| E2E | sign up → list → search → message → viewing → rented | Playwright (mobile viewport too) |
| Visual | key components/pages snapshot | Playwright screenshots |
| Performance | search and map endpoints | k6 |
| Accessibility | automated checks on key pages | axe-core in Playwright |
| Security | "exact coordinates never in public HTML" test; authz matrix | custom tests, `govulncheck`, ZAP baseline |

Definition of done for any feature: tests, mobile-checked, empty/loading/error states designed, analytics event added, audit log where sensitive, docs updated.

---

## 11. DevOps & observability

**Environments:** local (Compose) → staging (auto-deploy from `main`) → production (tagged releases).

**Docker Compose (dev excerpt)**

```yaml
services:
  db:
    image: postgis/postgis:17-3.5
    environment: { POSTGRES_USER: rentmap, POSTGRES_PASSWORD: rentmap, POSTGRES_DB: rentmap }
    ports: ["5432:5432"]
    volumes: [pgdata:/var/lib/postgresql/data]
  minio:
    image: minio/minio
    command: server /data --console-address ":9001"
    ports: ["9000:9000", "9001:9001"]
  mailpit:
    image: axllent/mailpit
    ports: ["8025:8025", "1025:1025"]
  valhalla:
    image: ghcr.io/gis-ops/docker-valhalla/valhalla:latest
    environment: { tile_urls: "https://download.geofabrik.de/africa/ghana-latest.osm.pbf" }
    ports: ["8002:8002"]
volumes: { pgdata: {} }
```

**Production:** multi-stage Dockerfiles (distroless final image, static Go binary with embedded assets), Caddy reverse proxy, Cloudflare in front, separate `web` and `worker` containers, zero-downtime deploys (start new → health check → switch), migrations run as a one-off job before rollout. Host in a region with good latency to West Africa and put static assets on the CDN.

**Observability:** structured logs with request/user IDs → Loki; metrics (request latency, search latency, job queue depth, OTP success rate, SMS cost, payment failures) → Prometheus/Grafana; traces via OpenTelemetry; errors → Sentry; uptime checks and alerting to WhatsApp/Slack.

**CI/CD (GitHub Actions):** lint → generate & drift check → unit → integration (testcontainers) → build images → E2E on staging → manual promote to production.

---

## 12. Metrics that matter

| Area | Metric |
| --- | --- |
| Supply | Active listings per neighbourhood; % with ≥ 8 photos; % fresh (confirmed ≤ 3 days) |
| Demand | Weekly active renters; searches per session; saves per renter |
| Liquidity | Viewing requests per listing per week; listing-to-viewing and viewing-to-rented conversion; time-to-rent |
| Trust | Reports per 1,000 listings; scam confirmations; accuracy rating average; % verified listers |
| Speed | Lister median response time; viewing confirmation time |
| Money | MRR, paying listers, ARPU, promotion fill rate, churn |
| North star | **Rentals completed through the platform per month** |

---

## 13. Key risks & mitigations

| Risk | Mitigation |
| --- | --- |
| Chicken-and-egg (no listings → no renters) | Beachhead around KNUST; founding landlords; agents onboarded as supply partners; field team lists properties for landlords who can't |
| Scams damage trust | Verification tiers, mandates, fraud scoring, scam shield, fast takedown, visible safety centre |
| Stale listings | Freshness engine with one-tap confirmation; auto-pause |
| Disintermediation (users go direct) | Value beyond introduction: viewings, verification, PM tools, receipts, reviews; approximate location until viewing confirmed |
| Agents resist | Give them better tools (lead management, verified profile, analytics) and fair visibility |
| Data costs for users | Data saver, AVIF, tiny JS, PWA caching |
| Map/API costs | Self-hosted tiles and routing |
| Regulatory surprises | Early legal review; licensed payment providers only |
| Solo-builder burnout | Strict phase scope; ship MVP (Phases 0–5) before anything else |

---

## 14. First 30 days — concrete checklist

1. Week 1: interviews, founding-landlord list, wireframes, repo + Compose + Ent/Atlas/sqlc + templ/Tailwind skeleton + CI.
2. Week 2: design tokens and components; phone OTP auth; roles; staging deploy; waitlist page live.
3. Week 3: Property/Unit/Listing/Terms/Media schemas and migrations with PostGIS; approximate-location function with tests; listing wizard steps 1–3.
4. Week 4: media pipeline (presigned upload → variants → blurhash/pHash); pricing step with live move-in cost; first real listings entered with founding landlords.

Then continue with Phase 3 (map discovery) — the moment the product starts to feel like itself.

---

## 15. Brand colours & visual identity

### 15.1 Primary colours

| Name | Hex | Role |
| --- | --- | --- |
| **Sky Mint** | `#B8F7E4` | Brand accent: highlights, selected states, accent buttons, active map pins, badges, illustrations |
| **Graphite** | `#25272C` | Brand base: text, primary buttons, nav/app bar, dark-mode surfaces, map price pills |

**Contrast facts (WCAG 2.2) — these drive every rule below:**

| Pair | Ratio | Verdict |
| --- | --- | --- |
| Graphite on white | ~15.0 : 1 | ✅ any text |
| Graphite on Sky Mint | ~12.4 : 1 | ✅ any text |
| Sky Mint on Graphite | ~12.4 : 1 | ✅ any text |
| Sky Mint on white | ~1.2 : 1 | ❌ never text, icons, borders or focus rings |
| White on Sky Mint | ~1.2 : 1 | ❌ never |

Sky Mint is a *light* colour, so it always pairs with Graphite, never with white.

### 15.2 Derived scales

Sky Mint is step 200 of its scale; Graphite is step 900. Darker mint steps exist so we have a text-safe "mint" for links and icons on light backgrounds. (Mid steps are derived — verify final values with a contrast checker during Phase 0 design.)

```css
/* web/css/app.css */
@import "tailwindcss";

@custom-variant dark (&:where(.dark, .dark *));   /* class-based dark mode in Tailwind v4 */

@theme {
  --color-mint-50:  #F0FDF8;
  --color-mint-100: #DCFCF1;
  --color-mint-200: #B8F7E4;  /* Sky Mint — brand */
  --color-mint-300: #86EDD0;
  --color-mint-400: #4FDBB6;
  --color-mint-500: #22C39C;
  --color-mint-600: #13A081;  /* ≥3:1 on white — large text / icons only */
  --color-mint-700: #0F7F68;  /* ≥4.5:1 on white — links, small text */
  --color-mint-800: #106554;
  --color-mint-900: #0F5346;

  --color-graphite-50:  #F7F8F9;
  --color-graphite-100: #F1F2F4;
  --color-graphite-200: #E3E5E8;
  --color-graphite-300: #C7CAD0;
  --color-graphite-400: #A0A4AD;
  --color-graphite-500: #7A7F8A;
  --color-graphite-600: #5C616C;  /* muted text on white, ~6:1 */
  --color-graphite-700: #454952;
  --color-graphite-800: #33363D;
  --color-graphite-900: #25272C;  /* Graphite — brand */
  --color-graphite-950: #17181B;

  /* Status colours — all ≥4.5:1 on white, deliberately NOT mint so "brand" ≠ "status" */
  --color-success: #15803D;
  --color-warning: #B45309;
  --color-danger:  #B91C1C;
  --color-verified:#1D4ED8;
}

/* Semantic tokens: components use these, never raw scale values */
:root {
  --bg: #FFFFFF;            --surface: var(--color-graphite-50);  --surface-raised: #FFFFFF;
  --text: var(--color-graphite-900);  --text-muted: var(--color-graphite-600);
  --border: var(--color-graphite-200);
  --primary: var(--color-graphite-900);  --on-primary: #FFFFFF;
  --accent:  var(--color-mint-200);      --on-accent:  var(--color-graphite-900);
  --link:    var(--color-mint-700);      --focus: var(--color-mint-700);
}
.dark {
  --bg: var(--color-graphite-950);  --surface: var(--color-graphite-900);  --surface-raised: var(--color-graphite-800);
  --text: var(--color-graphite-50); --text-muted: var(--color-graphite-400);
  --border: var(--color-graphite-700);
  --primary: var(--color-mint-200);  --on-primary: var(--color-graphite-900);  /* mint becomes the CTA in dark mode */
  --accent:  var(--color-mint-200);  --on-accent:  var(--color-graphite-900);
  --link:    var(--color-mint-300);  --focus: var(--color-mint-200);
}
@theme inline {
  --color-bg: var(--bg); --color-surface: var(--surface); --color-surface-raised: var(--surface-raised);
  --color-fg: var(--text); --color-fg-muted: var(--text-muted); --color-line: var(--border);
  --color-primary: var(--primary); --color-on-primary: var(--on-primary);
  --color-accent: var(--accent);   --color-on-accent: var(--on-accent);
  --color-link: var(--link);       --color-focus: var(--focus);
}
```

### 15.3 Usage rules

- **Proportion (60-30-10):** ~60% white / graphite-50 surfaces, ~30% Graphite (text, nav, primary buttons), ~10% Sky Mint (the thing you want the eye to land on). Mint is precious — overuse makes it wallpaper.
- **Buttons:** Primary = Graphite fill + white text (light) / Sky Mint fill + Graphite text (dark). Accent/secondary highlight = Sky Mint fill + Graphite text. Ghost = transparent + Graphite text + graphite-200 border.
- **Links & active icons** on light backgrounds use `mint-700`, never Sky Mint.
- **Focus ring:** 2 px `--focus` with 2 px offset — visible in both themes.
- **Chips / filter pills:** off = white + graphite-200 border; on = Sky Mint fill + Graphite text + check icon (don't rely on colour alone).
- **Map markers:** price pill = Graphite fill + white text (`GHS 1,200`); hovered/selected = Sky Mint fill + Graphite text with a scale-up; visited = graphite-200 fill; promoted = Sky Mint ring. Clusters = Graphite circle with Sky Mint count.
- **Basemap style:** desaturated neutral greys, water in muted blue-grey, parks in `mint-50`, so Graphite/Mint markers pop and the map feels on-brand.
- **Freshness badges (§6.7):** Fresh = `success`, Ageing = `warning` — status colours, not brand mint.
- **Hero / empty states / illustrations:** Sky Mint backgrounds with Graphite line illustrations; a soft `mint-50 → white` gradient is the signature surface.
- **Skeletons:** graphite-100 (light) / graphite-800 (dark) shimmer; blurhash placeholders for images.
- **Browser chrome:** `<meta name="theme-color" content="#25272C">`; PWA manifest `theme_color: #25272C`, `background_color: #FFFFFF`; app icon = Graphite glyph on Sky Mint.
- **Typography:** Inter 4 (variable `wght` 400–700 + `opsz`, self-hosted, subset to Latin + ₵ U+20B5, `font-display: swap`, ~50 KB). Chosen over Geist/Manrope/Figtree etc. because most of those lack the cedi sign. Headings semibold with tight tracking; body 15 px; prices use tabular figures.

---

## 16. Review notes — gaps, corrections & open decisions

### 16.1 Corrections already applied to this document

| Where | Problem | Fix |
| --- | --- | --- |
| §5.9 Ent schema | `Default(uuid.Must(uuid.NewV7))` does not compile — `uuid.Must` takes `(UUID, error)` and `Default` needs a `func() uuid.UUID` | `Default(func() uuid.UUID { return uuid.Must(uuid.NewV7()) })` |
| §5.2 `ListingTerms` | `monthly_equivalent` used in the §6.3 query but missing from the model | Added as stored derived field |
| §5.2 `rent_period` | Only month/year, but KNUST hostels are priced per **semester / academic year** | Added `semester`, `academic_year` |
| §6.3 radius/polygon | Used exact `geog` — an attacker can shrink a radius/polygon repeatedly and binary-search the exact location, defeating §6.1 | Public queries use `approx_geog` |
| §6.1 rule 6 | Distances from the *exact* point rounded to 100 m can be triangulated across several POIs (more precise than the 150–400 m fuzz) | Compute from approx point |
| §6.14 | Colour tokens said "deep green or terracotta; gold accent" | Replaced by Sky Mint + Graphite (§15) |

### 16.2 Technical risks the plan doesn't address

1. **Map-as-home vs. performance budget.** MapLibre GL is ~200 KB+ gzipped plus tiles, but principle 1 makes it the home screen and principle 4 says respect data bundles. Plan: server-render the result list and a lightweight map placeholder first (LCP = list/first card), then load MapLibre after first paint (`requestIdleCallback` / on interaction). In data-saver mode, default to list view with a "Show map" button. Set a separate budget for map pages (e.g. ≤ 300 KB JS gz, first tiles ≤ 150 KB).
2. **Strict CSP vs. HTMX/Alpine.** Standard Alpine.js requires `unsafe-eval`, and HTMX `hx-on`/`hx-vals="js:…"` use `eval`. Use the **Alpine CSP build** (`@alpinejs/csp`), set `htmx.config.allowEval = false`, `htmx.config.includeIndicatorStyles = false` (inline styles break a strict `style-src`), and avoid `hx-on`. MapLibre needs `worker-src blob:`.
3. **Atlas + generated PostGIS columns.** If Atlas diffs against the Ent schema, it will see the `geog` columns/indexes as drift and try to drop them. Decide early: Atlas composite schema (Ent + a SQL file), or declare the columns in Ent via `field.Other` with `entsql` annotations. Prove this in Phase 0.
4. **Transactional enqueue with Ent + River.** Ent uses `database/sql`; River's default driver is `pgx`. To enqueue inside an Ent transaction, use River's `riverdatabasesql` driver (or run Ent on the pgx stdlib driver sharing one pool). Also state the plan for one `pgxpool` shared by sqlc, River and `scs`.
5. **pgvector isn't in `postgis/postgis` images.** You'll need a custom DB Dockerfile (PostGIS + pgvector) before Phase 8 — do it in Phase 0 so dev/prod match.
6. **Valhalla in dev Compose** downloads Ghana OSM and builds tiles (slow, several GB RAM). Put it behind a Compose `profiles: [routing]` so `task dev` stays fast.
7. **Progressive enhancement.** View Transitions and `@starting-style` aren't supported on older Android WebViews/Chrome versions common on budget phones. Treat them as enhancements; everything must work without them. Define a browser support matrix (e.g. last 2 years of Chrome Android, Samsung Internet, Opera Mini fallback = list view only).
8. **Anonymous favourites cookie** — cookies cap at ~4 KB (~90 UUIDs). Cap favourites at ~50 or store them server-side keyed by an anonymous ID.
9. **GhanaPostGPS** has no reliable public API. Treat the digital address as validated-format free text (`XX-XXX-XXXX`) plus the map pin as the source of truth; don't depend on conversion.
10. **Move-in cost with yearly/semester rent.** `rent × advance_months_required` needs the period normalised (use `monthly_equivalent × advance_months`, or store advance in periods). Write the rule + unit tests before the pricing step.
11. **Tile hosting.** PMTiles need HTTP range requests — confirm Cloudflare caching of range requests from R2 and set long cache headers.

### 16.3 Product/security gaps

1. **Phone-number recycling.** Ghanaian telcos reassign inactive numbers — phone-only auth means a new SIM owner can take over an old account (with its listings and messages). Add: account recovery via secondary factor (email/passkey/Ghana Card re-check), "number changed" flow, and re-verification for landlords after long inactivity.
2. **SMS pumping / OTP toll fraud.** Restrict OTP to `+233` numbers by default, add per-prefix and global daily spend caps, CAPTCHA-style challenge (e.g. Turnstile) after N attempts.
3. **Account deletion vs. retention.** Deleting a user conflicts with reviews, ledger entries, leases and audit logs. Define anonymisation (keep records, strip PII) per entity.
4. **i18n** is referenced ("ready from Phase 0") but not in the Phase 0 checklist — add it (string catalogue in templ, locale middleware already listed).
5. **Currency display** — decide `GH₵` vs `GHS` and one formatter (`GHS 1,200`, no pesewas unless non-zero).
6. **Listing slugs / URL scheme** — SEO pages need `slug` on Listing/Neighbourhood and a canonical URL rule (e.g. `/l/{short-id}/{slug}`), plus redirects when headlines change.
7. **Error, 404, offline and maintenance pages** — design them in Phase 0 alongside the components.
8. **Moderation SLAs & who moderates** — a pending_review queue with no staffing plan blocks supply. Define target review time (e.g. < 4 h during business hours).
9. **Backup targets** — state RPO/RTO (e.g. RPO 15 min via WAL, RTO 2 h).
10. **Analytics & consent** — PostHog + Act 843 means a consent banner/record before non-essential tracking.
11. **Rent Act advance limits** — the Act caps advance rent (commonly cited as 6 months) while 1–2 years is market norm. Keep the neutral "Know your rights" link, but get legal advice before the platform *enforces* or *highlights* anything.

### 16.4 Repository state vs. the plan

- `go.mod` declares `module RentMapGh`; prefer a lowercase, import-safe path such as `github.com/<you>/rentmapgh` before any packages exist.
- `Main.go` at the repo root is the GoLand template and should become `cmd/web/main.go` (§4). It also has a bug: `fmt.Println("Hello and welcome, %s!", s)` — `Println` doesn't format; it should be `Printf`.

### 16.5 Decisions to make before Phase 0 coding

- [ ] Tile source: self-hosted Protomaps PMTiles vs MapTiler (cost vs effort).
- [ ] SMS provider (Hubtel / Arkesel / mNotify) and WhatsApp BSP.
- [ ] Atlas schema strategy for PostGIS columns (16.2 #3).
- [ ] Font final choice and brand logo on Sky Mint / Graphite.
- [ ] Browser support matrix and map-page performance budget.

### 16.6 Decisions taken while building Phase 0 (deviations from §2)

| Area | Plan said | Built | Why |
| --- | --- | --- | --- |
| CSRF | `justinas/nosurf` | Go's `http.CrossOriginProtection` (Sec-Fetch-Site / Origin) | No per-user token in HTML, so anonymous pages stay edge-cacheable (§6.15); no token plumbing in every form |
| CSP | nonce-based | static `script-src 'self'; style-src 'self'` | Nonces make every response unique (uncacheable). No inline scripts/styles at all; the theme boot is a 200 B file |
| Migrations | Atlas CLI | Ent's built-in Atlas diff engine → golang-migrate files, embedded and applied by `cmd/migrate` / `MIGRATE_ON_BOOT` | No external CLI in dev or prod; Ent's diff never drops unknown columns, which solves 16.2 #3 |
| Alpine.js | from day one | not loaded yet (~1.5 KB vanilla `app.js` instead) | Nothing needs it yet; add the CSP build when bottom sheets/dropdowns arrive |
| Object storage (dev) | MinIO | deferred to Phase 2 | MinIO stopped publishing Docker Hub images; use SeaweedFS or Garage |
| sqlc | wired in Phase 0 | deferred to the first geo query (Phase 2/3) | Nothing to query yet; no cgo toolchain locally, so run sqlc via its Docker image |
| Module path | `rentmap` | `rentmapgh` | Rename with `go mod edit -module github.com/<you>/rentmapgh` once the repo is hosted |

### 16.7 Decisions taken while building Phase 1

| Area | Plan said | Built | Why |
| --- | --- | --- | --- |
| Sessions | `scs` | own `sessions` table (Ent): SHA-256 of a random cookie token, sliding expiry, `revoked_at` | The device list and "log out everywhere" need queryable per-device rows; scs stores opaque blobs |
| SMS provider | Hubtel / Arkesel / mNotify | `sms.Sender` interface with Mailpit (dev), log (tests) and Africa's Talking (free sandbox + live, covers Ghana) | Africa's Talking is the only one with a free sandbox and phone simulator; others are one small adapter each |
| OTP sends | River jobs | synchronous, 12 s timeout | The user is waiting on the code anyway and River isn't in the stack yet; move to River with WhatsApp fallback |
| Evidence storage | R2/MinIO presigned uploads | server-side upload to an encrypted (AES-256-GCM) disk store on a Docker volume, behind a `storage.Store` interface | Object storage arrives with the Phase 2 media pipeline; ID evidence should be encrypted and never publicly addressable anyway |
| Admin UI | Phase 5 | verification queue + evidence viewer now | The Phase 1 exit ("verified by an admin") needs it; staff roles granted with `cmd/admin` until a users screen exists |
| Account deletion | — | anonymise in place: phone → NULL (number reusable), PII cleared, evidence purged, sessions revoked | Keeps audit/ledger references intact (risk #3) |
| OTP storage | "hashed at rest" | HMAC-SHA256(AUTH_SECRET, phone + code); rows double as the send log for rate limits | A plain hash of a 6-digit code is brute-forceable offline |

### 16.8 Decisions taken while building Phase 2 (slice 2a)

| Area | Plan said | Built | Why |
| --- | --- | --- | --- |
| Amenities | `Amenity` + `UnitAmenity` tables | Go vocabulary + JSONB array on `units`, GIN-indexed | One source of truth in code, no seeding, `@>` filters are fast |
| Advance rent | `advance_months_required` | `advance_periods` (payments upfront in the listing's own period) | Hostels bill per semester/academic year; "months" doesn't fit them |
| Rent periods | month/year/semester/academic_year | same, with semester = 4 and academic year = 8 months for `monthly_equivalent` | KNUST calendar; one constant table in `taxonomy.go` |
| Location secret | `HMAC(secret, id)` | dedicated `LOCATION_SECRET`, validated, documented as never-rotate | Rotating a shared secret would give each property two offsets and leak the real point |
| Map | MapLibre | MapLibre 6 ESM, vendored; OpenFreeMap tiles (no key); CSP adds only `connect-src`/`img-src` for the tile host | No per-request cost, strict CSP kept |

### 16.9 Decisions taken while building Phase 2 (slice 2b — photos)

| Area | Plan said | Built | Why |
| --- | --- | --- | --- |
| Upload path | presigned URL, browser → bucket | browser → Go server, one photo per request, shrunk to ≤1600 px on the phone first (~0.3–0.6 MB) | The server decodes and re-encodes every photo anyway; no bucket CORS or extra `connect-src`; the same code works on the disk store. Revisit for video, where files are large |
| Processing | River job | synchronous in the upload request, at most 2 decodes at once | River isn't in the stack yet; a photo takes ~0.5 s; the lister gets "too small" / "already added" on that photo's tile immediately |
| Variants | 320/640/1280/2000 px, AVIF + WebP + JPEG | 320/800/1600 px JPEG (q76–82) | Go has no WebP/AVIF encoder without cgo/libvips. Add AVIF/WebP with a libvips worker later, re-rendering from the 1600 px copy |
| Originals | kept for re-processing | never stored; only re-encoded renditions | The upload can carry EXIF GPS that points at the exact building (§6.1) |
| Object storage | R2 / MinIO | `MEDIA_STORE=disk` or `s3`; SeaweedFS 3.97 in dev (`:latest` shipped a broken image), R2/Garage in production; own ~200-line SigV4 client | Three verbs don't justify the AWS SDK; the signer is tested against AWS's published signatures and a real SeaweedFS |
| Serving | bucket/CDN URLs | `/media/{id}/w320.jpg` etc. through Go, `Cache-Control: immutable` for a year, Cloudflare in front | Keeps `img-src 'self'`; the IDs are unguessable UUIDv7s. A deleted photo can linger in the CDN cache until purged — add a Cloudflare purge on delete at launch |
| Duplicates | pHash | 64-bit DCT pHash, ≤4 bits apart **and** the same aspect ratio → "already added" (within one listing) | Re-encoding the same photo drifts up to 4 bits; the aspect check stops portrait/landscape look-alikes. Cross-listing matching is Phase 5 (§6.11) |
| Photo rules | — | 3 to publish, 8+ for the 25 quality points, 30 max; "Add photos later" skips the step, but Review blocks publishing | Enough for room, bathroom and compound without blocking listers who shoot later |

Phase 0 engineering status: repo skeleton, Compose dev stack (PostGIS + pgvector image, Mailpit, Valhalla behind a profile), config, slog, request IDs, graceful shutdown, `/healthz` + `/readyz`, Ent + migrations, design tokens + first components, landing page with waitlist, tests, CI and the production Dockerfile/Compose/Caddy are done. Still open: staging VPS + Cloudflare, i18n scaffolding, product/legal tasks.