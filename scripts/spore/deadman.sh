#!/bin/bash
# Local backstop for one spore.host instance:
#
#   scripts/spore/deadman.sh <name> <region> <ttl>
#
# <ttl> is a spawn duration (90m, 8h, 1h30m) or a number of minutes.
#
# Waits until 15 minutes past the instance's TTL, then terminates every
# instance of the run still alive, in any region, through the EC2 API.
# spored enforces the TTL on the instance, but an instance starved of memory
# cannot run spored, and spawn's TTL reaper scans only us-west-2.
# Terminating needs no SSH and works on a hung host.
set -uo pipefail
NAME="$1"; REGION="$2"; TTL="$3"
. "$(dirname "$0")/ttl.sh"
TTLM=$(ttl_minutes "$TTL") || exit 2
sleep $(((TTLM + 15) * 60))
# Every region, not only <region>: a launch attempt that reported failure can
# leave an instance of the same run elsewhere. Run names are unique per
# launch (fleet.sh numbers them), so nothing else carries this name.
out=$("$(dirname "$0")/sweep.sh" "$NAME" "$REGION" ${AMBIT_REGIONS:-us-east-1 us-west-2})
rc=$?
if [ -n "$out" ]; then
	echo "$out" | sed 's/terminated$/outlived its TTL by 15 minutes; terminated/'
elif [ $rc -ne 0 ]; then
	echo "$(date -u +%FT%TZ) $NAME: could not check every region; run stragglers.sh"
else
	echo "$(date -u +%FT%TZ) $NAME ended on its own"
fi
