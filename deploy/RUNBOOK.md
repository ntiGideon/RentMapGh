# RentMap operations runbook

Single VPS, Docker Compose (`deploy/compose.prod.yml`), Caddy for TLS, Cloudflare in front.
All commands run on the server from `/opt/rentmap` unless noted.

```sh
alias dc=/opt/rentmap/deploy/server/dc   # provision.sh adds this to the deploy user's .bashrc
```

`dc` is `docker compose` with `deploy/.env.prod` (secrets) and `deploy/.env.release` (the image
`deploy.sh` rolled out), so `dc up -d` never falls back to an old image.

## First deploy (new server)

1. Create an Ubuntu 24.04 VPS (2 vCPU / 4 GB is plenty to start) with your SSH key and provider
   snapshots on. Point the domain at it in Cloudflare (proxied).
2. From your laptop:
   ```sh
   scp deploy/server/provision.sh root@<ip>:
   ssh root@<ip> bash provision.sh git@github.com:ntiGideon/RentMapGh.git
   ```
   The first run prints a deploy key: add it to the repo (GitHub → Settings → Deploy keys,
   read-only) and run the same command again. It sets up security updates, UTC, swap, Docker
   (with capped logs), the `deploy` user, SSH keys-only with no root login, the firewall
   (22/80/443), fail2ban, `/opt/rentmap`, the cron jobs and log rotation. From then on log in as
   `deploy@<ip>`.
3. Fill `/opt/rentmap/deploy/.env.prod` (provision.sh copies the template,
   `deploy/env.prod.example`). For an off-site point-in-time archive also create
   `deploy/.env.pgbackrest` from `deploy/pgbackrest.env.example`.
4. `deploy/server/deploy.sh`: builds both images, starts the database, migrates, starts web and
   Caddy, and waits for `/healthz`.
5. `deploy/backup/pitr.sh setup`: starts WAL archiving and takes the first full backup.
6. Monitors, from your laptop or the server:
   `BETTERSTACK_TOKEN=… DOMAIN=<domain> deploy/monitor/betterstack.sh`, then paste the
   `HEARTBEAT_*` lines it prints into `/etc/rentmap/ops.env`. Set `RCLONE_REMOTE` there too, after
   `rclone config` as `deploy`.
7. Work through the go-live checklist below.

## Go-live checklist

- [ ] VPS with daily provider snapshots on, set up with `provision.sh` (SSH keys only, firewall
      open on 22/80/443 only).
- [ ] Cloudflare: DNS proxied, SSL "Full (strict)", cache rule bypassing `/admin*`, `/messages*`, `/events`.
- [ ] `deploy/.env.prod` filled (see README → Deploy). Copy `AUTH_SECRET`, `LOCATION_SECRET` and
      `DOCUMENT_KEY` into the team password manager — a restore is useless without them.
- [ ] `SUPPORT_EMAIL` set; legal pages reviewed by a lawyer and the "Draft" notices removed
      (`internal/views/pages/legal.templ`), entity name and Data Protection Commission number filled in.
- [ ] First admin: sign in once on the site, then `dc run --rm --entrypoint /app/admin web grant 024XXXXXXX admin`.
- [ ] `pitr.sh setup` done; `pitr.sh check`, `pitr.sh test` and `restore-test.sh` pass (below).
      `PGBACKREST_REPO2_CIPHER_PASS` is in the password manager if repo2 is on.
- [ ] Uptime checks and alerts on (below).
- [ ] `SENTRY_DSN` set (optional but recommended) and a test error seen in Sentry.
- [ ] Load test run against staging (below) and the numbers written down.

## Deploying a new version

```sh
deploy/server/deploy.sh               # origin/main
deploy/server/deploy.sh origin/dev    # or any branch, tag or commit (staging)
deploy/server/deploy.sh rollback      # back to the image that was live before
```

It checks out the ref and builds `rentmap/web:<sha>`. It also rebuilds the database image; the
database restarts only if that image changed. Then it runs the migrations (they're written to be
backwards compatible), recreates web (a few seconds of 502s) and waits for `/healthz`. The live
and previous images are kept and older ones deleted. Every rollout is logged in
`/var/log/rentmap/deploys.log`. If the new version doesn't come up healthy, the script says so;
run `deploy.sh rollback`.

A rollback never touches the database. Never roll back a migration on production by hand; ship a
fix forward. Before a migration that rewrites a lot of data, take `pitr.sh backup full`.

## Backups

There are two independent layers:

| | What | Recovery point | Kept |
| --- | --- | --- | --- |
| Point in time (pgBackRest) | every WAL segment (at least every 5 min) + base backups: full on Sundays, differential the other nights | any moment in the last 23 days | `pgbackrest` volume, plus the optional encrypted R2 repo2 |
| Nightly dump (`backup.sh`) | `pg_dump` + the uploads and media volumes | last night | 30 days, local + optional rclone copy |

The dump is portable (any Postgres 17 with PostGIS loads it) and holds the only copy of the
uploads and media volumes. pgBackRest gives minute-level recovery of the database. Neither keeps
anything older than 30 days, as the privacy policy promises.

The cron jobs are in `/etc/cron.d/rentmap`, installed from `deploy/server/rentmap.cron`; times are
UTC, which is Ghana time. Each job runs through `deploy/server/job.sh`, which logs to
`/var/log/rentmap/<job>.log`. On success it pings the job's heartbeat; on failure it calls
`<url>/fail` with the last lines of output.

| Job | When | Does |
| --- | --- | --- |
| `pitr-backup` | 02:00 daily | `pitr.sh backup auto` (full on Sundays), then expires old backups |
| `dump-backup` | 03:15 daily | `backup.sh` |
| `pitr-check` | 06:00 daily | archiving works, newest backup < 26 h old, no WAL dropped |
| `restore-test` | Mon 04:30 | `restore-test.sh`: the newest dump loads |
| `pitr-test` | Mon 05:30 | `pitr.sh test`: repo1 restores into a throwaway container and the data is there |
| `host-check` | every 15 min | disks < 80 % full, db/web/caddy running and healthy |

### Point-in-time recovery

```sh
deploy/backup/pitr.sh info                                 # backups and the WAL range
deploy/backup/pitr.sh restore "2026-10-04 14:29:00+00"     # data was deleted at 14:30
deploy/backup/pitr.sh restore latest                       # the data volume is gone or corrupt
PITR_REPO=2 deploy/backup/pitr.sh restore latest           # the same, from R2 (the server's disk is gone)
```

`restore` asks you to type "restore", stops web and the database, and rewinds with `--delta`.
It waits for recovery to finish and prints a user count. Check the data, then `dc up -d web`.
Times are UTC. The WAL after the target stays in the repo, so a second restore to a later moment
still works.

On a new server: provision it, copy `.env.prod` and `.env.pgbackrest`, run `deploy.sh`, then
`PITR_REPO=2 pitr.sh restore latest`.

If archiving can't reach a repo (e.g. R2 is down), WAL waits in `pg_wal`. Past 4 GiB pgBackRest
drops it to protect the disk, and `pitr-check` fails. Once the repo is back, take
`pitr.sh backup full` to close the gap.

### Nightly dump

`deploy/backup/backup.sh` writes a `pg_dump` (custom format) plus the `uploads` (encrypted ID
evidence) and `media` (listing photos) volumes to `/var/backups/rentmap/<timestamp>/` with
checksums, keeps 30 days (the privacy policy promises no more), and copies each backup off the
server when `RCLONE_REMOTE` is set (e.g. an R2 or B2 bucket configured with `rclone config`).

`RCLONE_REMOTE` (e.g. `r2:rentmap-backups`) and `KEEP_DAYS` come from `/etc/rentmap/ops.env`.

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

`deploy/monitor/betterstack.sh` creates these checks in Better Stack, plus one heartbeat per cron
job above. Running it again only adds what's missing. The free plan alerts by email; for texts,
add the on-call phone in Better Stack and set `ALERT_SMS=true` (paid plans). Any other monitor
works too: `job.sh` only needs a URL that alerts when it isn't pinged, and that accepts
`<url>/fail`.

| Check | URL | Every | Alert when |
| --- | --- | --- | --- |
| Site up | `https://<domain>/healthz` | 1 min | 2 failures in a row |
| Database reachable | `https://<domain>/readyz` | 1 min | 2 failures (returns 503 when the DB is down) |
| Search works | `https://<domain>/search?near=knust` | 5 min | not 200, or slower than 2 s |
| TLS | certificate expiry | daily | < 14 days left |

Disk space and container health are covered by the `host-check` heartbeat. A backup job that
stops running is caught by its heartbeat's period (1 day plus 2 hours' grace).

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
