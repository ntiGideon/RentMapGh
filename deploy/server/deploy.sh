#!/usr/bin/env bash
# Build and roll out a version on the server (as the deploy user):
#   deploy.sh              the tip of origin/main
#   deploy.sh <ref>        a branch, tag or commit (e.g. origin/dev on staging)
#   deploy.sh rollback     back to the image that was live before the last deploy
# Migrations run before the new web container starts; they're written to be
# backwards compatible, so a rollback never touches the database.
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/../.." && pwd)
DC="$ROOT/deploy/server/dc"
RELEASE_FILE="$ROOT/deploy/.env.release"
LOG=${DEPLOY_LOG:-/var/log/rentmap/deploys.log}
cd "$ROOT"
[ -f deploy/.env.prod ] || { echo "deploy: fill deploy/.env.prod first (see deploy/env.prod.example)" >&2; exit 1; }

current=$(sed -n 's/^WEB_IMAGE=//p' "$RELEASE_FILE" 2>/dev/null || true)
previous=$(sed -n 's/^PREVIOUS_WEB_IMAGE=//p' "$RELEASE_FILE" 2>/dev/null || true)

if [ "${1:-}" = rollback ]; then
	[ -n "$previous" ] || { echo "deploy: nothing to roll back to" >&2; exit 1; }
	image=$previous
	previous=$current
else
	ref=${1:-origin/main}
	git fetch --prune --quiet origin
	git checkout --quiet --detach "$ref"
	sha=$(git rev-parse --short HEAD)
	echo "deploy: building $sha ($(git log -1 --format=%s))"
	docker build -q -f deploy/docker/db.Dockerfile -t rentmap/postgres:17-postgis-pgvector . > /dev/null
	docker build -q -f deploy/docker/web.Dockerfile -t "rentmap/web:$sha" . > /dev/null
	image=rentmap/web:$sha
	[ "$image" = "$current" ] || previous=$current
fi

write_release() {
	printf 'WEB_IMAGE=%s\nRELEASE=%s\nPREVIOUS_WEB_IMAGE=%s\n' "$1" "${1#rentmap/web:}" "$2" > "$RELEASE_FILE"
}
write_release "$image" "$previous"

# Recreates db only if its image or settings changed (a few seconds of 502s).
"$DC" up -d --wait db
[ "${1:-}" = rollback ] || "$DC" run --rm migrate
"$DC" up -d web caddy

echo "deploy: waiting for $image to pass /healthz"
for _ in $(seq 1 60); do
	if "$DC" exec -T caddy wget -qO- http://web:8080/healthz > /dev/null 2>&1; then
		echo "$(date -u +%FT%TZ) $image (was ${previous:-none})" >> "$LOG" 2> /dev/null || true
		# Keep the live and previous images; drop older builds.
		docker images rentmap/web --format '{{.Repository}}:{{.Tag}}' \
			| grep -vxF -e "$image" -e "${previous:-none}" | xargs -r docker rmi > /dev/null 2>&1 || true
		docker image prune -f > /dev/null
		echo "deploy: live — $image"
		exit 0
	fi
	sleep 2
done
echo "deploy: FAIL $image isn't healthy; see: dc logs --tail 100 web" >&2
echo "deploy: to go back: deploy.sh rollback" >&2
exit 1
