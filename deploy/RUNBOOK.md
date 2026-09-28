# RentMap operations runbook

Single VPS, Docker Compose (`deploy/compose.prod.yml`), Caddy for TLS, Cloudflare in front.
All commands run on the server from `/opt/rentmap` unless noted.

```sh
alias dc='docker compose -f deploy/compose.prod.yml --env-file deploy/.env.prod'
```

## Go-live checklist

- [ ] VPS with daily provider snapshots on, SSH keys only, firewall open on 22/80/443 only.
- [ ] Cloudflare: DNS proxied, SSL "Full (strict)", cache rule bypassing `/admin*`, `/messages*`, `/events`.
- [ ] `deploy/.env.prod` filled (see README → Deploy). Copy `AUTH_SECRET`, `LOCATION_SECRET` and
      `DOCUMENT_KEY` into the team password manager — a restore is useless without them.
- [ ] `SUPPORT_EMAIL` set; legal pages reviewed by a lawyer and the "Draft" notices removed
      (`internal/views/pages/legal.templ`), entity name and Data Protection Commission number filled in.
- [ ] First admin: sign in once on the site, then `dc run --rm --entrypoint /app/admin web grant 024XXXXXXX admin`.
- [ ] Backups scheduled and one restore test passed (below).
- [ ] Uptime checks and alerts on (below).
- [ ] `SENTRY_DSN` set (optional but recommended) and a test error seen in Sentry.
- [ ] Load test run against staging (below) and the numbers written down.

## Deploying a new version

```sh
docker build -f deploy/docker/web.Dockerfile -t rentmap/web:$(git rev-parse --short HEAD) .
export WEB_IMAGE=rentmap/web:$(git rev-parse --short HEAD) RELEASE=$(git rev-parse --short HEAD)
dc run --rm migrate          # migrations first; they're written to be backwards compatible
dc up -d web                 # recreates the container: a few seconds of 502s; Caddy health-checks /healthz
```

Rollback: set `WEB_IMAGE` to the previous tag and `dc up -d web`. Never roll back a migration on
production by hand; ship a fix forward.

## Backups

`deploy/backup/backup.sh` writes a `pg_dump` (custom format) plus the `uploads` (encrypted ID
evidence) and `media` (listing photos) volumes to `/var/backups/rentmap/<timestamp>/` with
checksums, keeps 30 days (the privacy policy promises no more), and copies each backup off the
server when `RCLONE_REMOTE` is set (e.g. an R2 or B2 bucket configured with `rclone config`).

```cron
# /etc/cron.d/rentmap (times are UTC = Ghana time). cron mails the output of
# a failing job to MAILTO; point the uptime monitor's heartbeat at it too.
MAILTO=ops@<domain>
15 3 * * *  root RCLONE_REMOTE=r2:rentmap-backups /opt/rentmap/deploy/backup/backup.sh >> /var/log/rentmap-backup.log 2>&1 || echo "backup failed"
30 4 * * 1  root /opt/rentmap/deploy/backup/restore-test.sh >> /var/log/rentmap-backup.log 2>&1 || echo "restore test failed"
```

Recovery point: up to 24 hours of data can be lost. Point-in-time recovery (WAL archiving with
pgBackRest) is the next step once there's real volume — see ProjectRequirement §16.22.

### Restore test (weekly, and after any change to the scripts)

```sh
deploy/backup/restore-test.sh            # newest backup
deploy/backup/restore-test.sh /var/backups/rentmap/20261001T031500Z
```

It loads the dump into a throwaway Postgres with the production image, checks users, listings,
pinned properties and the schema version, and checks the volume archives are readable.
Expected output: `restore-test: OK … — N users, N listings, …`.

### Restoring for real

```sh
dc stop web
deploy/backup/restore.sh /var/backups/rentmap/<timestamp>   # asks you to type "restore"
dc up -d web
```

Needs the same `DOCUMENT_KEY` and `LOCATION_SECRET` as when the backup was made.

## Uptime monitoring

Use an external monitor (Better Stack, UptimeRobot or similar) with alerts to the on-call phone
(SMS/WhatsApp) and email:

| Check | URL | Every | Alert when |
| --- | --- | --- | --- |
| Site up | `https://<domain>/healthz` | 1 min | 2 failures in a row |
| Database reachable | `https://<domain>/readyz` | 1 min | 2 failures (returns 503 when the DB is down) |
| Search works | `https://<domain>/search?near=knust` | 5 min | not 200, or slower than 2 s |
| TLS | certificate expiry | daily | < 14 days left |

Also alert on: disk above 80 % (`df -h`; photos and backups grow), and the backup log not
updating for 26 hours.

## Errors (Sentry)

Set `SENTRY_DSN` in `.env.prod`. Every `ERROR` log line — including recovered panics with their
stack — becomes a Sentry event, grouped by log message and tagged with the request ID (search the
JSON logs with `dc logs web | grep <req_id>`). No request bodies, cookies or phone numbers are sent.

## Load testing

k6 script: `deploy/loadtest/search.js` (search page and fragment, markers, place suggestions,
area and listing pages; read-only). Run it from a machine near the server, against **staging**:

```sh
k6 run -e BASE_URL=https://staging.<domain> deploy/loadtest/search.js
```

Search is rate limited to 240 requests/minute per IP. To measure the app rather than the limiter,
target the web container directly (`BASE_URL=http://<vps-private-ip>:8080`, with `TRUST_PROXY=true`)
and add `-e SPREAD_IPS=1`. Thresholds: p95 < 300 ms for search and markers, < 1 % errors.

## Common support tasks

- **Scam or fake listing:** Admin → Listings → open it → "Take down this listing" (the lister is told why).
- **Bad actor:** Admin → Users → search the phone → Suspend (signs them out, pauses their listings).
- **"It doesn't work for me":** Admin → Users → the person → "View as this user" (admins; read-only, 30 min, audited).
- **Who did what:** Admin → Audit log (admins), filter by phone or listing ID.
- **Someone wants their data deleted:** they can do it from Account → Delete account; staff can't
  delete accounts for them yet — suspend first if urgent, then ask them to delete.
