#!/usr/bin/env bash
# Runs a cron job, appends its output to /var/log/rentmap/<name>.log and
# reports to the job's heartbeat monitor: the URL on success, <URL>/fail
# (with the last lines of output) on failure. A job that stops running at
# all is caught by the heartbeat's period. URLs live in /etc/rentmap/ops.env
# as HEARTBEAT_<NAME> (dashes become underscores), e.g. HEARTBEAT_PITR_BACKUP.
#   job.sh pitr-backup /opt/rentmap/deploy/backup/pitr.sh backup auto
set -uo pipefail

name=${1:?usage: job.sh <name> <command> [args…]}
shift
if [ -f /etc/rentmap/ops.env ]; then
	set -a
	. /etc/rentmap/ops.env
	set +a
fi
var=HEARTBEAT_$(echo "$name" | tr 'a-z-' 'A-Z_')
url=${!var:-}
logdir=${LOG_DIR:-/var/log/rentmap}
log=$logdir/$name.log
mkdir -p "$logdir"

echo "== $(date -u +%FT%TZ) start" >> "$log"
"$@" >> "$log" 2>&1
rc=$?
echo "== $(date -u +%FT%TZ) exit $rc" >> "$log"

if [ -n "$url" ]; then
	if [ "$rc" -eq 0 ]; then
		curl -fsS -m 10 --retry 3 -o /dev/null "$url" || true
	else
		tail -n 40 "$log" | curl -fsS -m 10 --retry 3 -o /dev/null --data-binary @- "$url/fail" || true
	fi
fi
exit "$rc"
