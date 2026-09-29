#!/usr/bin/env bash
# Proves the newest backup can be restored: loads it into a throwaway
# Postgres (same image as production) and checks the data is there. Run it
# weekly from cron and alert on a non-zero exit:
#   30 4 * * 1 /opt/rentmap/deploy/backup/restore-test.sh >> /var/log/rentmap-backup.log 2>&1
set -euo pipefail

BACKUP_DIR=${BACKUP_DIR:-/var/backups/rentmap}
IMAGE=${DB_IMAGE:-rentmap/postgres:17-postgis-pgvector}
dir=${1:-$(ls -1d "$BACKUP_DIR"/*/ | sort | tail -n 1)}
dir=${dir%/}
[ -f "$dir/rentmap.dump" ] || { echo "restore-test: no dump in $dir" >&2; exit 1; }
(cd "$dir" && sha256sum -c --quiet SHA256SUMS)

name=rentmap-restore-test-$$
trap 'docker rm -f "$name" > /dev/null 2>&1 || true' EXIT
docker run -d --name "$name" -e POSTGRES_USER=rentmap -e POSTGRES_PASSWORD=restore-test -e POSTGRES_DB=rentmap "$IMAGE" > /dev/null
# The entrypoint runs init scripts on a temporary server first; wait for
# the real one.
for _ in $(seq 1 120); do
	if docker logs "$name" 2>&1 | grep -q "init process complete" && docker exec "$name" pg_isready -U rentmap -d rentmap > /dev/null 2>&1; then
		break
	fi
	sleep 1
done

# An empty database (template0): the image pre-installs PostGIS into new
# ones, and the dump recreates the extensions itself.
docker exec "$name" psql -U rentmap -d postgres -qc "CREATE DATABASE restored TEMPLATE template0"
start=$(date +%s)
docker exec -i "$name" pg_restore -U rentmap -d restored --no-owner --exit-on-error < "$dir/rentmap.dump"
q() { docker exec "$name" psql -U rentmap -d restored -tAc "$1"; }
users=$(q "SELECT count(*) FROM users")
listings=$(q "SELECT count(*) FROM listings")
version=$(q "SELECT version FROM schema_migrations")
postgis=$(q "SELECT count(*) FROM properties WHERE approx_geog IS NOT NULL")
[ "$users" -gt 0 ] || { echo "restore-test: FAIL no users in $dir" >&2; exit 1; }
for f in "$dir"/*.tar.gz; do
	[ -e "$f" ] || continue
	tar -tzf "$f" > /dev/null || { echo "restore-test: FAIL unreadable $f" >&2; exit 1; }
done
echo "restore-test: OK $(basename "$dir") in $(( $(date +%s) - start ))s — $users users, $listings listings, $postgis pinned properties, schema $version"
