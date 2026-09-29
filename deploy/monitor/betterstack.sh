#!/usr/bin/env bash
# Creates RentMap's uptime monitors and cron heartbeats in Better Stack
# (free plan: 10 monitors, 10 heartbeats, email alerts) and prints the
# heartbeat URLs for /etc/rentmap/ops.env. Safe to run again: anything that
# already exists (same name) is left as it is.
#
#   BETTERSTACK_TOKEN=<Uptime API token> DOMAIN=rentmap.com.gh deploy/monitor/betterstack.sh
#
# Token: Better Stack → Uptime → Settings → API tokens. Set ALERT_SMS=true
# (paid plans) to text the on-call person too. Needs curl and jq.
set -euo pipefail

: "${BETTERSTACK_TOKEN:?set BETTERSTACK_TOKEN}"
: "${DOMAIN:?set DOMAIN, e.g. rentmap.com.gh}"
SMS=${ALERT_SMS:-false}
API=${BETTERSTACK_API:-https://uptime.betterstack.com/api/v2}

call() {
	local method=$1 path=$2
	shift 2
	curl -fsS -X "$method" -H "Authorization: Bearer $BETTERSTACK_TOKEN" -H "Content-Type: application/json" "$@" "$API$path"
}

# Every page of a list endpoint, as one array of items.
list() {
	local url="$API/$1" out='[]' page
	while [ -n "$url" ] && [ "$url" != null ]; do
		page=$(curl -fsS -H "Authorization: Bearer $BETTERSTACK_TOKEN" "$url")
		out=$(jq -c --argjson acc "$out" '$acc + .data' <<< "$page")
		url=$(jq -r '.pagination.next // empty' <<< "$page")
	done
	echo "$out"
}

monitors=$(list monitors)
monitor() {
	local name=$1 body=$2
	if jq -e --arg n "$name" 'any(.[]; .attributes.pronounceable_name == $n)' <<< "$monitors" > /dev/null; then
		echo "monitor exists: $name" >&2
		return
	fi
	call POST /monitors -d "$(jq -c --arg n "$name" --argjson sms "$SMS" '. + {pronounceable_name: $n, email: true, sms: $sms}' <<< "$body")" > /dev/null
	echo "monitor created: $name" >&2
}

# Two failed checks in a row before an alert (confirmation_period), as in the runbook.
monitor "RentMap site" '{"monitor_type":"status","url":"https://'"$DOMAIN"'/healthz","check_frequency":60,"confirmation_period":60,"request_timeout":10,"ssl_expiration":14,"domain_expiration":30}'
monitor "RentMap database" '{"monitor_type":"expected_status_code","expected_status_codes":[200],"url":"https://'"$DOMAIN"'/readyz","check_frequency":60,"confirmation_period":60,"request_timeout":10}'
monitor "RentMap search" '{"monitor_type":"keyword","required_keyword":"RentMap","url":"https://'"$DOMAIN"'/search?near=knust","check_frequency":300,"confirmation_period":60,"request_timeout":3}'

heartbeats=$(list heartbeats)
# name period grace (seconds). The name is the job.sh name.
jobs=(
	"pitr-backup 86400 7200"
	"dump-backup 86400 7200"
	"pitr-check 86400 7200"
	"restore-test 604800 21600"
	"pitr-test 604800 21600"
	"host-check 900 900"
)
echo
echo "# Paste into /etc/rentmap/ops.env on the server:"
for j in "${jobs[@]}"; do
	read -r name period grace <<< "$j"
	title="RentMap $name"
	url=$(jq -r --arg n "$title" 'first(.[] | select(.attributes.name == $n) | .attributes.url) // empty' <<< "$heartbeats")
	if [ -z "$url" ]; then
		url=$(call POST /heartbeats -d "$(jq -nc --arg n "$title" --argjson p "$period" --argjson g "$grace" --argjson sms "$SMS" \
			'{name: $n, period: $p, grace: $g, email: true, sms: $sms}')" | jq -r '.data.attributes.url')
		echo "heartbeat created: $title" >&2
	fi
	echo "HEARTBEAT_$(echo "$name" | tr 'a-z-' 'A-Z_')=$url"
done
