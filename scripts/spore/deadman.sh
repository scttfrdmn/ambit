#!/bin/bash
# Local backstop for one spore.host instance:
#
#   scripts/spore/deadman.sh <name> <region> <ttl>
#
# <ttl> is a spawn duration (90m, 8h, 1h30m) or a number of minutes.
#
# Waits until 15 minutes past the instance's TTL, then terminates it through
# the EC2 API if it is still alive. spored enforces the TTL on the instance,
# but an instance starved of memory cannot run spored, and spawn's TTL reaper
# scans only us-west-2. Terminating needs no SSH and works on a hung host.
set -uo pipefail
NAME="$1"; REGION="$2"; TTL="$3"
minutes() {
	local s="$1" h=0 m=0
	if [[ "$s" =~ ^[0-9]+$ ]]; then echo "$s"; return; fi
	[[ "$s" =~ ([0-9]+)h ]] && h=${BASH_REMATCH[1]}
	[[ "$s" =~ ([0-9]+)m ]] && m=${BASH_REMATCH[1]}
	if [ $((h + m)) -eq 0 ]; then echo "cannot parse TTL '$s'" >&2; exit 2; fi
	echo $((10#$h * 60 + 10#$m))
}
TTLM=$(minutes "$TTL") || exit 2
sleep $(((TTLM + 15) * 60))
id=$(aws ec2 describe-instances --region "$REGION" \
	--filters "Name=tag:project,Values=ambit" "Name=tag:run,Values=$NAME" \
	"Name=instance-state-name,Values=pending,running,stopping,stopped" \
	--query 'Reservations[].Instances[].InstanceId' --output text)
if [ -n "$id" ]; then
	echo "$(date -u +%FT%TZ) $NAME ($id) outlived its TTL by 15 minutes; terminating"
	aws ec2 terminate-instances --region "$REGION" --instance-ids $id --output text >/dev/null
else
	echo "$(date -u +%FT%TZ) $NAME ended on its own"
fi
