#!/bin/bash
# Copies a spore.host run's outputs to the local results directory.
#
#   scripts/spore/fetch.sh <name> [results-dir]
set -euo pipefail
NAME="$1"
DEST="${2:-$HOME/.xtmp/ambit-results}"
REGION="${AMBIT_REGION:-us-west-2}"
ACCOUNT=$(aws sts get-caller-identity --query Account --output text)
STAGE="s3://spawn-results-$ACCOUNT-$REGION/ambit/$NAME"
mkdir -p "$DEST/spore-logs/$NAME"
for host in $(aws s3 ls "$STAGE/out/" | awk '/PRE/{print $2}' | tr -d /); do
	aws s3 sync "$STAGE/out/$host/results" "$DEST" --quiet
	aws s3 cp "$STAGE/out/$host/job.log" "$DEST/spore-logs/$NAME/$host.job.log" --quiet || true
	for f in llama.log strands.log; do aws s3 cp "$STAGE/out/$host/$f" "$DEST/spore-logs/$NAME/$host.$f" --quiet 2>/dev/null || true; done
done
echo "fetched $NAME into $DEST"
