#!/bin/bash
# Straggler check after a spore.host run: anything still running or left
# behind under project=ambit. Read-only. Exits 1 if anything is found.
set -uo pipefail
found=0
# Any state but terminated counts: pending and stopping instances still cost.
# An instance that is shutting down gets up to five minutes to finish.
for _ in 1 2 3 4 5 6; do
	listing=$(spawn list --tag project=ambit --state all -o json 2>/dev/null) || break
	echo "$listing" | jq -e '[.[] | select((.state // .State // "") | ascii_downcase == "shutting-down")] | length == 0' >/dev/null && break
	sleep 50
done
if ! listing=$(spawn list --tag project=ambit --state all -o json 2>/dev/null); then
	echo "spawn list failed; cannot confirm there are no stragglers" >&2
	exit 2
fi
live=$(echo "$listing" | jq -r '.[] | select((.state // .State // "") | ascii_downcase != "terminated") | "\(.name // .Name) \(.state // .State) \(.instance_id // .InstanceID // "")"') || {
	echo "could not parse spawn list output; cannot confirm there are no stragglers" >&2
	exit 2
}
if [ -n "$live" ]; then
	echo "instances tagged project=ambit that are not terminated:"
	echo "$live"
	found=1
fi
spawn orphans --all-regions 2>/dev/null | grep -i ambit && found=1
[ $found -eq 0 ] && echo "no stragglers"
exit $found
