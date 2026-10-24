#!/bin/bash
# Copies a spore.host run's outputs to the local results directory.
#
#   scripts/spore/fetch.sh <name> [results-dir]
set -euo pipefail
NAME="$1"
DEST="${2:-$HOME/.xtmp/ambit-results}"
ACCOUNT=$(aws sts get-caller-identity --query Account --output text)
# A run is staged in every region an attempt tried, and runs in the one
# that launched, so fetch every region that has outputs, not the first that
# has a stage.
found=0
for REGION in ${AMBIT_REGIONS:-us-east-1 us-west-2}; do
	STAGE="s3://spawn-results-$ACCOUNT-$REGION/ambit/$NAME"
	hosts=$(aws s3 ls "$STAGE/out/" 2>/dev/null | awk '/PRE/{print $2}' | tr -d /) || true
	[ -n "$hosts" ] || continue
	found=1
	mkdir -p "$DEST/spore-logs/$NAME"
	for host in $hosts; do
		aws s3 sync "$STAGE/out/$host/results" "$DEST" --quiet
		aws s3 cp "$STAGE/out/$host/job.log" "$DEST/spore-logs/$NAME/$host.job.log" --quiet || true
		for f in llama.log strands.log d3.log; do aws s3 cp "$STAGE/out/$host/$f" "$DEST/spore-logs/$NAME/$host.$f" --quiet 2>/dev/null || true; done
	done
done
[ $found = 1 ] || { echo "no outputs for $NAME" >&2; exit 1; }
echo "fetched $NAME into $DEST"
