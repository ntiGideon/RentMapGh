#!/usr/bin/env bash
# Point-in-time recovery with pgBackRest (config: deploy/pgbackrest/). The
# database container archives every WAL segment; this script takes the base
# backups, checks archiving, restores, and proves a restore works.
#
#   pitr.sh setup            once, after the first `up`: create the stanza, check, full backup
#   pitr.sh backup auto      from cron: full on Sundays, differential the other nights
#   pitr.sh backup full|diff one of those by hand (e.g. a full before a risky migration)
#   pitr.sh check            archiving works and the newest backup is recent (from cron)
#   pitr.sh info             backups, WAL range, repo sizes
#   pitr.sh restore <time>   restore the live database to a moment, e.g. "2026-10-04 14:30:00+00"
#   pitr.sh restore latest   restore everything archived (after losing the data volume)
#                            PITR_REPO=2 in front: restore from the off-site repo
#   pitr.sh test             restore repo1 into a throwaway container and check the data
#
# The daily pg_dump (backup.sh) stays: it's portable and independent of this.
set -euo pipefail

ROOT=${ROOT:-$(cd "$(dirname "$0")/../.." && pwd)}
PROJECT=${COMPOSE_PROJECT:-rentmap}
IMAGE=${DB_IMAGE:-rentmap/postgres:17-postgis-pgvector}
STANZA=rentmap
MAX_AGE_HOURS=${MAX_AGE_HOURS:-26} # a nightly backup, plus slack

dc() {
	local env=(--env-file "$ROOT/deploy/.env.prod")
	[ -f "$ROOT/deploy/.env.release" ] && env+=(--env-file "$ROOT/deploy/.env.release")
	docker compose -f "$ROOT/deploy/compose.prod.yml" "${env[@]}" "$@"
}
db() { docker ps -qf "label=com.docker.compose.project=$PROJECT" -f "label=com.docker.compose.service=db"; }
pgbr() {
	local c
	c=$(db)
	[ -n "$c" ] || { echo "pitr: db container not running" >&2; exit 1; }
	docker exec -u postgres "$c" pgbackrest --stanza="$STANZA" "$@"
}

cmd=${1:-}
case "$cmd" in
setup)
	pgbr stanza-create
	pgbr check
	pgbr --type=full backup
	pgbr info
	;;

backup)
	type=${2:?usage: pitr.sh backup auto|full|diff}
	if [ "$type" = auto ]; then
		if [ "$(date -u +%u)" = 7 ]; then type="full"; else type="diff"; fi
	fi
	pgbr --type="$type" backup
	pgbr expire
	echo "pitr: $type backup done"
	;;

check)
	# Forces a WAL switch and waits for that segment to reach every repo.
	pgbr check
	# Newest backup (any type) in any repo must be recent.
	last=$(pgbr info --output=json | grep -o '"stop":[0-9]*' | cut -d: -f2 | sort -n | tail -n 1)
	[ -n "$last" ] || { echo "pitr: FAIL no backups yet (run pitr.sh setup)" >&2; exit 1; }
	age=$(( ($(date +%s) - last) / 3600 ))
	[ "$age" -lt "$MAX_AGE_HOURS" ] || { echo "pitr: FAIL newest backup is ${age}h old" >&2; exit 1; }
	# archive-push-queue-max dropped WAL: recovery has a gap until the next backup.
	c=$(db)
	if docker logs --since 25h "$c" 2>&1 | grep -q "dropped WAL file"; then
		echo "pitr: FAIL WAL was dropped in the last day (repo unreachable?); see docker logs" >&2
		exit 1
	fi
	echo "pitr: OK archiving works, newest backup ${age}h old"
	;;

info)
	pgbr info
	;;

restore)
	target=${2:?usage: pitr.sh restore "<YYYY-MM-DD HH:MM:SS+00>"|latest}
	if [ "$target" = latest ]; then
		args=()
		what="everything archived"
	else
		args=(--type=time "--target=$target" --target-action=promote)
		what="$target"
	fi
	# PITR_REPO=2: restore from the off-site repo (the server's own disk is gone).
	[ -z "${PITR_REPO:-}" ] || args+=("--repo=$PITR_REPO")
	echo "This stops the site and rewinds the production database to $what."
	echo "Changes made after that moment are lost (they stay in the repo; a later restore can bring them back)."
	read -r -p 'Type "restore" to continue: ' answer
	[ "$answer" = restore ] || { echo "pitr: cancelled"; exit 1; }
	dc stop web db
	# --delta rewrites only the files that differ: quick, and works on an empty volume too.
	dc run --rm --no-deps -u postgres --entrypoint pgbackrest db --stanza="$STANZA" --delta "${args[@]}" restore
	dc up -d db
	echo "pitr: waiting for recovery to finish"
	for _ in $(seq 1 600); do
		if [ "$(dc exec -T db psql -U rentmap -d rentmap -tAc 'SELECT pg_is_in_recovery()' 2>/dev/null)" = f ]; then
			dc exec -T db psql -U rentmap -d rentmap -tAc "SELECT 'pitr: restored — ' || count(*) || ' users, newest sign-up ' || coalesce(max(created_at)::text, '-') FROM users"
			echo "pitr: check the data, then start the site: dc up -d web"
			exit 0
		fi
		sleep 1
	done
	echo "pitr: FAIL recovery didn't finish in 10 min; see: dc logs db" >&2
	exit 1
	;;

test)
	name=rentmap-pitr-test-$$
	vol=rentmap-pitr-test-$$
	trap 'docker rm -f "$name" > /dev/null 2>&1 || true; docker volume rm "$vol" > /dev/null 2>&1 || true' EXIT
	docker volume create "$vol" > /dev/null
	start=$(date +%s)
	# repo1 only, mounted read-only so the test can't touch the real archive.
	docker run --rm -u postgres -v "${PROJECT}_pgbackrest:/var/lib/pgbackrest:ro" -v "$vol:/var/lib/postgresql/data" \
		--entrypoint pgbackrest "$IMAGE" --stanza="$STANZA" --repo=1 --log-level-console=warn restore
	# archive_mode=off: a promoted copy must never push WAL into the real repo.
	docker run -d --init --name "$name" -v "${PROJECT}_pgbackrest:/var/lib/pgbackrest:ro" -v "$vol:/var/lib/postgresql/data" \
		"$IMAGE" postgres -c archive_mode=off > /dev/null
	q() { docker exec "$name" psql -U rentmap -d rentmap -tAc "$1" 2>/dev/null; }
	for _ in $(seq 1 600); do
		[ "$(q 'SELECT pg_is_in_recovery()')" = f ] && break
		sleep 1
	done
	[ "$(q 'SELECT pg_is_in_recovery()')" = f ] || { docker logs --tail 30 "$name" >&2; echo "pitr-test: FAIL recovery didn't finish" >&2; exit 1; }
	users=$(q "SELECT count(*) FROM users")
	listings=$(q "SELECT count(*) FROM listings")
	version=$(q "SELECT version FROM schema_migrations")
	newest=$(q "SELECT coalesce(max(created_at)::text, '-') FROM audit_events")
	[ "${users:-0}" -gt 0 ] || { echo "pitr-test: FAIL no users after restore" >&2; exit 1; }
	echo "pitr-test: OK in $(( $(date +%s) - start ))s — $users users, $listings listings, schema $version, newest audit entry $newest"
	;;

*)
	sed -n '2,15p' "$0" | sed 's/^# \{0,1\}//'
	exit 2
	;;
esac
