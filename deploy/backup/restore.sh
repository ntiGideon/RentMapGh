#!/usr/bin/env bash
# Restores a backup made by backup.sh INTO THE RUNNING STACK, replacing the
# current database and volumes. Stop the web service first:
#   docker compose -f deploy/compose.prod.yml --env-file deploy/.env.prod stop web
#   deploy/backup/restore.sh /var/backups/rentmap/20261001T031500Z
#   docker compose -f deploy/compose.prod.yml --env-file deploy/.env.prod up -d web
# The same DOCUMENT_KEY and LOCATION_SECRET must be in .env.prod, or the
# restored ID evidence can't be read and approximate pins would move.
set -euo pipefail

dir=${1:?usage: restore.sh <backup dir>}
PROJECT=${COMPOSE_PROJECT:-rentmap}
DB_CONTAINER=${DB_CONTAINER:-$(docker ps -qf "label=com.docker.compose.project=$PROJECT" -f "label=com.docker.compose.service=db")}
[ -n "$DB_CONTAINER" ] || { echo "restore: db container not running" >&2; exit 1; }
(cd "$dir" && sha256sum -c --quiet SHA256SUMS)

if [ "${YES:-}" != "1" ]; then
	read -r -p "Replace the rentmap database and volumes with $dir? Type 'restore': " answer
	[ "$answer" = restore ] || { echo "restore: cancelled"; exit 1; }
fi

echo "restore: database"
docker exec "$DB_CONTAINER" psql -U rentmap -d postgres -v ON_ERROR_STOP=1 \
	-c "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = 'rentmap' AND pid <> pg_backend_pid()" \
	-c "DROP DATABASE IF EXISTS rentmap" -c "CREATE DATABASE rentmap OWNER rentmap TEMPLATE template0" # empty: the dump recreates the extensions
docker exec -i "$DB_CONTAINER" pg_restore -U rentmap -d rentmap --no-owner --exit-on-error < "$dir/rentmap.dump"

for f in "$dir"/*.tar.gz; do
	[ -e "$f" ] || continue
	v=$(basename "$f" .tar.gz)
	echo "restore: volume $v"
	docker run --rm -v "${PROJECT}_$v:/dst" -v "$dir:/in:ro" alpine:3 sh -c "rm -rf /dst/* /dst/..?* /dst/.[!.]* 2>/dev/null; tar -xzf /in/$v.tar.gz -C /dst"
done
echo "restore: done"
