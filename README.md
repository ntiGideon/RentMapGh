# RentMap Ghana

Honest rentals, on a map. Go · templ · HTMX · Tailwind v4 · Ent · PostgreSQL/PostGIS.
The full product plan lives in [ProjectRequirement.md](ProjectRequirement.md).

## Quick start

Prerequisites: Go 1.27+, Docker Desktop, and [Task](https://taskfile.dev):

```sh
go install github.com/go-task/task/v3/cmd/task@latest
```

```sh
task setup   # Tailwind CLI → bin/, .env from .env.example, start Postgres + Mailpit + SeaweedFS
task dev     # templ + Tailwind + Go server, all with live reload → http://localhost:8080
```

`templ` and `ent` are pinned in `go.mod` as Go tools (`go tool templ …`), so there's nothing else to install.

| Service  | URL                     |
| -------- | ----------------------- |
| App      | http://localhost:8080   |
| Mailpit  | http://localhost:8025   |
| Postgres | `localhost:5432` (rentmap / rentmap) |
| S3 (SeaweedFS) | http://localhost:8333 (rentmap / rentmap-dev-secret) |

## Signing in locally (SMS)

Sign-in is phone + one-time code. In development every SMS is delivered to **Mailpit**:
open http://localhost:8080/login, enter any Ghana mobile number (e.g. `024 123 4567`),
then read the code in the Mailpit inbox at http://localhost:8025. The code step links there too.

| `SMS_PROVIDER` | Use | Setup |
| --- | --- | --- |
| `mailpit` | local dev (default in `.env.example`) | nothing — Mailpit runs with `task db:up` |
| `log` | tests / CI | codes are printed to the server log |
| `africastalking` | real-API testing and production | free sandbox: create an account at africastalking.com, open the **sandbox** app, generate an API key, set `AT_USERNAME=sandbox`, `AT_API_KEY=…`, `AT_SANDBOX=true`, then launch the phone simulator at simulator.africastalking.com with the same number. Production: your live app username + key, `AT_SANDBOX=false` and a registered `SMS_SENDER_ID`. |

Abuse limits (see `internal/modules/auth/otp.go`): Ghana mobile numbers only; 60 s resend
cooldown; 5 codes/hour and 10/day per number; 5 guesses per code; codes live 5 minutes and are
stored only as an HMAC; a global `SMS_DAILY_CAP`; plus per-IP rate limits on every auth endpoint.

## Verification & the admin back office

Landlords and agents verify from their account page: Ghana Card number, photos of both sides and a
selfie (`/verify/identity`), and agents add their licence (`/verify/licence`). A moderator reviews
each one at **`/admin/verifications`** and approves or rejects with a reason; the user gets an SMS
and the badge appears on their account.

Staff roles can't be self-assigned. Give yourself access (sign in once first):

```sh
task admin:grant PHONE=0241234567 ROLE=moderator   # or ROLE=admin
task admin:revoke PHONE=0241234567 ROLE=moderator
```

Evidence photos are re-encoded (EXIF/GPS stripped), encrypted with AES-256-GCM (`DOCUMENT_KEY`)
and written under `STORAGE_DIR` — never `web/static`. Ent privacy rules stop any query from reading
another user's verification unless the viewer is a moderator/admin. Photos and card numbers are
deleted `EVIDENCE_RETENTION` (default 90 days) after a decision; the decision is kept.

## Listing a property

Landlords and agents list from **Your listings** (`/listings`). The wizard goes location → property →
the space → amenities → photos → pricing → details → review, autosaving as you type; "Continue"
validates the step. The pricing step shows the live move-in cost from `listings.ComputeMoveIn` — the one function
every page uses. A listing can't be published until every fee is answered (0 is an answer).

- **Location privacy:** the exact pin stays private. Every property also gets an approximate point
  150–400 m away, derived from `HMAC(LOCATION_SECRET, property id)` (`internal/platform/geo`), and
  all public features must read `approx_*` / `approx_geog`. Never rotate `LOCATION_SECRET`.
- **Map:** MapLibre 6 (ES modules, vendored under `web/static/vendor/`, loaded only on the location
  step) with free OpenFreeMap tiles — no API key.
- **Photos:** at least 3 to publish (8+ for full quality points), up to 30. `photo-manager.js` shrinks
  each photo to ≤1600 px on the phone, uploads them one at a time with progress, and handles
  drag-to-reorder; without JS the step still works with a multi-file form and arrow buttons. The
  server decodes every upload, bakes in the EXIF rotation and re-encodes it to 320/800/1600 px JPEGs
  (all metadata, including GPS, is dropped). The upload itself is never stored. It also keeps a
  blurhash and a perceptual hash (`internal/platform/imaging`); the hash refuses the same photo twice.
  Files are served from `/media/{id}/w800.jpg` with a one-year immutable cache.
- **Media store:** `MEDIA_STORE=disk` (`MEDIA_DIR`) or `s3`, any S3-compatible bucket. Dev uses
  SeaweedFS; production can use Cloudflare R2 (`S3_REGION=auto`) or Garage. The S3 client is a small
  built-in SigV4 signer (`internal/platform/storage/s3.go`), checked against AWS's published examples.
- **Review:** listers with a verified ID publish instantly; everyone else goes to the moderator queue
  at `/admin/listings`.
- **Money** is always integer pesewas (`internal/platform/money`).

## Everyday commands

| Command | What it does |
| --- | --- |
| `task dev` | Run with live reload |
| `task test` | Unit + integration tests (integration tests use the `rentmap_test` DB, one package at a time, and SeaweedFS) |
| `task lint` | `go vet` + golangci-lint |
| `task generate` | Regenerate templ and Ent code |
| `task db:diff NAME=add_users` | Generate a migration from the Ent schema |
| `task db:migrate` / `task db:rollback` | Apply / roll back migrations |
| `task db:hash` | Re-hash after hand-editing a migration (PostGIS, extensions) |
| `task db:psql` | Open psql |
| `task build` | Production binaries in `bin/` |

## How it's put together

```
cmd/web          HTTP server            cmd/migrate   migrations CLI
internal/config  env config             internal/db   pgx pool shared by Ent (+ sqlc/River later)
internal/ent     Ent schema + generated internal/modules/<domain>  handler + service per domain
internal/server  router, middleware, htmx + render helpers
internal/views   templ: layouts, components, pages, partials
web/             Tailwind entry (css/), static assets (embedded into the binary)
migrations/      versioned SQL (golang-migrate format, embedded)
deploy/          Dockerfiles, compose (dev/prod), Caddyfile
```

**Conventions**

- **Handler rule:** htmx requests get the fragment and everything else gets the full page (`render.Page`), so every URL works when shared.
- **Colours:** components use only the semantic tokens (`bg-primary`, `text-fg`, `bg-accent`, …) defined in `web/css/app.css`. Never put Sky Mint (`#B8F7E4`) text on white. See §15 of the requirements.
- **CSP is strict and static:** no inline `<script>`, no inline `style=""`, no `hx-on`/eval. Put behaviour in `web/static/js/*.js` and styling in Tailwind classes.
- **CSRF:** Go's `http.CrossOriginProtection` (Sec-Fetch-Site/Origin), so forms need no tokens and anonymous HTML stays edge-cacheable.
- **Migrations:** change the Ent schema, then run `task db:diff NAME=…`. Ent never drops columns it doesn't know about, so hand-written PostGIS columns are safe.
- **Static assets:** reference them with `reqctx.Asset(ctx, "path")`. That gives a content-hashed URL, cached immutable.

## Deploy (single VPS)

A fresh Ubuntu VPS is set up with `deploy/server/provision.sh`; after that each release is one
command, `deploy/server/deploy.sh` (build, migrate, roll out, health check; `rollback` to go back).
The steps are in [deploy/RUNBOOK.md → First deploy](deploy/RUNBOOK.md#first-deploy-new-server).

`deploy/.env.prod` (template: `deploy/env.prod.example`) must set `BASE_URL` (https), `SITE_ADDRESS` (the domain for Caddy), `POSTGRES_PASSWORD`,
`AUTH_SECRET` (`openssl rand -base64 48`), `LOCATION_SECRET` (`openssl rand -base64 48`, never rotate), `DOCUMENT_KEY` (`openssl rand -base64 32`; back it up —
evidence can't be decrypted without it) and the Africa's Talking settings (`SMS_PROVIDER=africastalking`,
`AT_USERNAME`, `AT_API_KEY`, `AT_SANDBOX=false`, `SMS_SENDER_ID`). The server refuses to start in
production with the dev secret, without a document key or with a dev SMS provider. Uploads live on
the `uploads` volume and listing photos on the `media` volume: back both up together with the database.
To keep photos in R2 instead, set `MEDIA_STORE=s3`, `S3_ENDPOINT=https://<account>.r2.cloudflarestorage.com`,
`S3_BUCKET` and an R2 API token's `S3_ACCESS_KEY_ID` / `S3_SECRET_ACCESS_KEY`.

Operations — point-in-time recovery (pgBackRest) and nightly dumps, restore tests, cron
heartbeats and uptime checks, Sentry, load testing, the go-live checklist and common support
tasks — are in [deploy/RUNBOOK.md](deploy/RUNBOOK.md).
