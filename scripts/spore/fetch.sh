#!/bin/bash
# Copies a spore.host run's outputs to the local results directory.
#
#   scripts/spore/fetch.sh <name> [results-dir]
set -euo pipefail
NAME="$1"
DEST="${2:-$HOME/.xtmp/ambit-results}"
ACCOUNT=$(aws sts get-caller-identity --query Account --output text)
# The run was staged in the region it launched in; find it.
STAGE=""
for REGION in ${AMBIT_REGION:-us-east-1 us-west-2}; do
	if aws s3 ls "s3://spawn-results-$ACCOUNT-$REGION/ambit/$NAME/" >/dev/null 2>&1; then
		STAGE="s3://spawn-results-$ACCOUNT-$REGION/ambit/$NAME"
		break
	fi
done
[ -n "$STAGE" ] || { echo "no staged run $NAME" >&2; exit 1; }
mkdir -p "$DEST/spore-logs/$NAME"
for host in $(aws s3 ls "$STAGE/out/" | awk '/PRE/{print $2}' | tr -d /); do
	aws s3 sync "$STAGE/out/$host/results" "$DEST" --quiet
	aws s3 cp "$STAGE/out/$host/job.log" "$DEST/spore-logs/$NAME/$host.job.log" --quiet || true
	for f in llama.log strands.log d3.log; do aws s3 cp "$STAGE/out/$host/$f" "$DEST/spore-logs/$NAME/$host.$f" --quiet 2>/dev/null || true; done
done
echo "fetched $NAME into $DEST"
