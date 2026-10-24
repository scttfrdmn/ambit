#!/bin/bash
# Runs a queue of shard runs on spore.host until every shard has a finished
# manifest, relaunching shards whose instance ends early (spot interruptions
# are routine at this capacity).
#
#   scripts/spore/fleet.sh <fleet-name> <corpus-dir> <queue-file> <results-dir> \
#       <instances> <shards-per-instance> <ttl> <cost-limit>
#
# MAXTRIES (default 3) bounds the finished-with-errors runs of any one shard.
#
# <queue-file> has one shard per line, in `ambit run` arguments, each with an
# explicit -offset and -limit. A shard is done when <results-dir> holds a
# finished manifest with the same arm, range and corpus. The fleet log goes
# to stdout; instances are named <fleet-name>-<n>.
set -uo pipefail
FLEET="$1"; CORPUS="$2"; QUEUE="$3"; RESULTS="$4"; W="$5"; S="$6"; TTL="$7"; LIMIT="$8"
ROOT=$(git rev-parse --show-toplevel)
cd "$ROOT" || exit 1
STATE="$HOME/.xtmp/ambit-fleet/$FLEET"
mkdir -p "$STATE"
HASH=$(go run ./cmd/ambit verify -dir "$CORPUS") || exit 1
log() { echo "$(date -u +%FT%TZ) $*"; }

# key prints a shard's identity: arm name (with any ablation or -name),
# range start and range end.
key() {
	local line="$1" arm name="" abl="" off lim
	arm=$(sed -nE 's/.*-arm ([a-z0-9-]+).*/\1/p' <<<"$line")
	[[ "$line" =~ -name\ ([^ ]+) ]] && name=${BASH_REMATCH[1]}
	[[ "$line" =~ -ablation\ ([^ ]+) ]] && abl=1
	off=$(sed -nE 's/.*-offset ([0-9]+).*/\1/p' <<<"$line")
	lim=$(sed -nE 's/.*-limit ([0-9]+).*/\1/p' <<<"$line")
	[ -n "$name" ] && arm=$name || { [ -n "$abl" ] && arm="$arm-ablation"; }
	echo "$arm ${off:-0} $((${off:-0} + ${lim:-0}))"
}

# server prints which model server a shard needs.
server() {
	case "$1" in
	*"-arm a-strands"* | *"-arm b-strands"*) echo strands ;;
	*"-arm a-local"* | *"-arm b-4b"* | *"-arm b "*) echo llama ;;
	*) echo none ;;
	esac
}

# done_keys lists the keys of finished, complete runs on this corpus with no
# repairable gate errors (run.Repairable: transport and capacity failures).
# A run whose model server crashed decides every record as such an error;
# counting it done (as on 2026-10-19) hid 5,000 errors per shard. A shard
# with one is re-run in full, which re-asks exactly those errors. Other gate
# errors stay denials and repeat on a re-run, so they do not block a shard.
# Manifests from before the repairable count fall back to all errors.
done_keys() {
	for m in "$RESULTS"/*/manifest.json; do
		[ -f "$m" ] || continue
		jq -r --arg h "$HASH" 'select(.corpus_hash == $h and .finished != null and .decided == .records and (.repairable_errors // .errors) == 0 and (.stopped // "") == "" and ((.repair_of // []) | length) == 0) | "\(.arm) \(.range_start) \(.range_end)"' "$m"
	done | sort -u
}

# errored_keys lists, once per run, the keys of finished, complete runs that
# still have repairable errors.
errored_keys() {
	for m in "$RESULTS"/*/manifest.json; do
		[ -f "$m" ] || continue
		jq -r --arg h "$HASH" 'select(.corpus_hash == $h and .finished != null and .decided == .records and (.repairable_errors // .errors) > 0 and (.stopped // "") == "" and ((.repair_of // []) | length) == 0) | "\(.arm) \(.range_start) \(.range_end)"' "$m"
	done
}

alive() { # instance names of this fleet that are not terminated
	spawn list --tag project=ambit --state all -o json 2>/dev/null |
		jq -r --arg f "$FLEET-" '.[] | select((.name | startswith($f)) and ((.state // "") | ascii_downcase) != "terminated") | .name'
}

n=0
while :; do
	for inst in $(ls "$STATE" 2>/dev/null | grep -E '^[0-9]+$'); do
		scripts/spore/fetch.sh "$FLEET-$inst" "$RESULTS" >/dev/null 2>&1
	done
	done_keys >"$STATE/done"
	pending=()
	while IFS= read -r line; do
		[ -z "$line" ] && continue
		grep -qxF "$(key "$line")" "$STATE/done" || pending+=("$line")
	done <"$QUEUE"
	live=$(alive)
	# Shards held by a live instance are not pending.
	held=()
	for inst in $(ls "$STATE" | grep -E '^[0-9]+$'); do
		if grep -qx "$FLEET-$inst" <<<"$live"; then
			while IFS= read -r l; do held+=("$(key "$l")"); done <"$STATE/$inst"
		fi
	done
	# A shard that finished MAXTRIES times with repairable errors (a model
	# server that crashes on it every time) is given up on, not relaunched
	# forever. Spot interruptions leave no finished run, so they do not count:
	# at G2 a shard took up to 9 launches to finish.
	errored=$(errored_keys)
	todo=()
	gaveup=0
	for l in "${pending[@]+"${pending[@]}"}"; do
		k=$(key "$l")
		printf '%s\n' "${held[@]+"${held[@]}"}" | grep -qxF "$k" && continue
		if [ "$(grep -cxF "$k" <<<"$errored")" -ge "${MAXTRIES:-3}" ]; then
			gaveup=$((gaveup + 1))
			continue
		fi
		todo+=("$l")
	done
	nlive=$(grep -c . <<<"$live")
	total=$(grep -c . "$QUEUE")
	log "done $((total - ${#pending[@]}))/$total, pending ${#pending[@]}, unassigned ${#todo[@]}, given up $gaveup, live instances $nlive"
	if [ ${#pending[@]} -eq 0 ]; then
		log "all shards done"
		break
	fi
	if [ ${#todo[@]} -eq 0 ] && [ "$nlive" -eq 0 ] && [ "$gaveup" -gt 0 ]; then
		log "stopping: $gaveup shards finished ${MAXTRIES:-3} times each with repairable errors; see their runs"
		break
	fi
	while [ $nlive -lt "$W" ] && [ ${#todo[@]} -gt 0 ]; do
		n=$(( $(ls "$STATE" | grep -E '^[0-9]+$' | sort -n | tail -1 || echo 0) + 1 ))
		# An instance runs one model server: batch shards that need the same one.
		kind=$(server "${todo[0]}")
		batch=()
		rest=()
		for l in "${todo[@]}"; do
			if [ ${#batch[@]} -lt "$S" ] && [ "$(server "$l")" = "$kind" ]; then batch+=("$l"); else rest+=("$l"); fi
		done
		todo=("${rest[@]+"${rest[@]}"}")
		printf '%s\n' "${batch[@]}" >"$STATE/$n"
		runs="$STATE/$n.runs"
		{ grep -q -- '-arm [ab]-strands\|-arm b-strands' "$STATE/$n" && echo "#strands-cuda"; cat "$STATE/$n"; } >"$runs"
		if scripts/spore/try-launch.sh "$FLEET-$n" "$CORPUS" "$runs" "$TTL" "$LIMIT" 2>&1 | grep -q LAUNCHED; then
			log "launched $FLEET-$n with ${#batch[@]} shards"
			nlive=$((nlive + 1))
		else
			log "no capacity for $FLEET-$n; will retry"
			rm -f "$STATE/$n" "$runs"
			todo=("${batch[@]}" "${todo[@]+"${todo[@]}"}")
			break
		fi
	done
	sleep 300
done
scripts/spore/stragglers.sh
