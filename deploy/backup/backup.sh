#!/usr/bin/env bash
# Daily backup of the production stack: a compressed pg_dump of the database
# plus the uploads (encrypted ID evidence) and media (listing photos)
# volumes. Keeps KEEP_DAYS of backups locally and, if RCLONE_REMOTE is set,
# copies each one off the server (Cloudflare R2, Backblaze B2, …).
#
# Cron on the VPS (03:15 every night, Ghana time = UTC):
#   15 3 * * * /opt/rentmap/deploy/backup/backup.sh >> /var/log/rentmap-backup.log 2>&1
#
# Restore: deploy/backup/restore.sh. Prove it works: deploy/backup/restore-test.sh.
set -euo pipefail

BACKUP_DIR=${BACKUP_DIR:-/var/backups/rentmap}
KEEP_DAYS=${KEEP_DAYS:-30}          # the privacy policy promises no more than 30 days
PROJECT=${COMPOSE_PROJECT:-rentmap} # volume names are <project>_<volume>
DB_CONTAINER=${DB_CONTAINER:-$(docker ps -qf "label=com.docker.compose.project=$PROJECT" -f "label=com.docker.compose.service=db")}
VOLUMES=${VOLUMES:-"uploads media"}

[ -n "$DB_CONTAINER" ] || { echo "backup: db container not running" >&2; exit 1; }
stamp=$(date -u +%Y%m%dT%H%M%SZ)
dir="$BACKUP_DIR/$stamp"
mkdir -p "$dir"

echo "backup: $stamp database"
docker exec "$DB_CONTAINER" pg_dump -U rentmap -d rentmap -Fc -Z 6 > "$dir/rentmap.dump"
docker exec -i "$DB_CONTAINER" pg_restore --list < "$dir/rentmap.dump" > /dev/null # readable?

for v in $VOLUMES; do
	if docker volume inspect "${PROJECT}_$v" > /dev/null 2>&1; then
		echo "backup: $stamp volume $v"
		docker run --rm -v "${PROJECT}_$v:/src:ro" -v "$dir:/out" alpine:3 tar -czf "/out/$v.tar.gz" -C /src .
	fi
done
(cd "$dir" && sha256sum ./* > SHA256SUMS)

if [ -n "${RCLONE_REMOTE:-}" ]; then
	echo "backup: $stamp upload to $RCLONE_REMOTE"
	rclone copy "$dir" "$RCLONE_REMOTE/$stamp"
	rclone delete --min-age "${KEEP_DAYS}d" "$RCLONE_REMOTE" && rclone rmdirs --leave-root "$RCLONE_REMOTE"
fi

find "$BACKUP_DIR" -mindepth 1 -maxdepth 1 -type d -mtime "+$KEEP_DAYS" -exec rm -rf {} +
echo "backup: $stamp done ($(du -sh "$dir" | cut -f1))"
