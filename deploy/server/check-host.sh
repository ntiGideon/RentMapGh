#!/usr/bin/env bash
# Every 15 minutes from cron (through job.sh, so a failure alerts): disks
# below DISK_ALERT_PCT and the three long-running containers up.
set -euo pipefail

limit=${DISK_ALERT_PCT:-80}
PROJECT=${COMPOSE_PROJECT:-rentmap}
fail=0

for path in / "$(docker info -f '{{.DockerRootDir}}')" "${BACKUP_DIR:-/var/backups/rentmap}"; do
	[ -e "$path" ] || continue
	used=$(df --output=pcent "$path" | tail -n 1 | tr -dc 0-9)
	if [ "$used" -ge "$limit" ]; then
		echo "check-host: FAIL $path is ${used}% full (photos, WAL or backups growing?)"
		fail=1
	fi
done

for svc in db web caddy; do
	state=$(docker ps --filter "label=com.docker.compose.project=$PROJECT" --filter "label=com.docker.compose.service=$svc" --format '{{.Status}}')
	case "$state" in
	Up*unhealthy*) echo "check-host: FAIL $svc is unhealthy"; fail=1 ;;
	Up*) ;;
	*) echo "check-host: FAIL $svc is not running"; fail=1 ;;
	esac
done

[ "$fail" -eq 0 ] && echo "check-host: OK"
exit "$fail"
