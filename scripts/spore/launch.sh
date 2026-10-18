#!/bin/bash
# Stages a run on spore.host and launches one GPU instance for it.
#
#   scripts/spore/launch.sh <name> <corpus-dir> <runs-file> <ttl> <cost-limit>
#
# <runs-file> has one line of `ambit run` arguments per run (for example
# "-arm a-local -dir corpus/g2a -offset 0 -limit 20000 -concurrency 16
# -cache-prompt"); runs execute in order on one instance.
#
# Builds ambit for linux/amd64, bundles it with the corpus, uploads the
# bundle and gpu-job.sh to spawn's results bucket, and launches a g6.xlarge
# (AMBIT_INSTANCE, AMBIT_REGION, AMBIT_SPOT=0 for on-demand) tagged project=ambit with the given TTL and cost limit. The
# instance terminates when the job finishes. Fetch results with fetch.sh and
# check for stragglers with stragglers.sh.
set -euo pipefail

NAME="$1"; CORPUS="$2"; RUNS="$3"; TTL="$4"; LIMIT="$5"
[ -s "$RUNS" ] || { echo "runs file $RUNS is empty or missing" >&2; exit 1; }
# Law 6: $20 per run on AWS. spored enforces --cost-limit on the instance.
if ! [[ "$LIMIT" =~ ^[0-9]+(\.[0-9]+)?$ ]] || awk "BEGIN{exit !($LIMIT > 20)}"; then
	echo "cost limit must be a number no greater than 20 (Law 6), got '$LIMIT'" >&2
	exit 1
fi
[ -n "$TTL" ] || { echo "a TTL is required" >&2; exit 1; }
REGION="${AMBIT_REGION:-us-west-2}"
TYPE="${AMBIT_INSTANCE:-g6.xlarge}"
ACCOUNT=$(aws sts get-caller-identity --query Account --output text)
BUCKET="spawn-results-$ACCOUNT-$REGION"
STAGE="s3://$BUCKET/ambit/$NAME"
TMP=$(mktemp -d "${TMPDIR:-/tmp}/ambit-stage.XXXXXX")
trap 'rm -rf "$TMP"' EXIT

ROOT=$(git rev-parse --show-toplevel)
cd "$ROOT"
if [ -n "$(git status --porcelain --untracked-files=no)" ]; then
	echo "working tree is dirty; commit first so the manifests name the code that ran" >&2
	exit 1
fi
if [ -n "$(git ls-files --others --exclude-standard -- "$CORPUS")" ]; then
	echo "$CORPUS has untracked files; only committed corpora are run" >&2
	exit 1
fi
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o "$TMP/ambit" ./cmd/ambit
mkdir -p "$TMP/bundle/$(dirname "$CORPUS")"
cp "$TMP/ambit" "$TMP/bundle/ambit"
cp -R "$CORPUS" "$TMP/bundle/$CORPUS"
# The runner records the git commit; ship it, since the instance has no repo.
git rev-parse HEAD >"$TMP/bundle/GIT_COMMIT"
cp "$RUNS" "$TMP/bundle/RUNS"
tar czf "$TMP/bundle.tgz" -C "$TMP/bundle" .
aws s3 cp "$TMP/bundle.tgz" "$STAGE/bundle.tgz" --quiet
aws s3 cp scripts/spore/gpu-job.sh "$STAGE/gpu-job.sh" --quiet

CMD="aws s3 cp $STAGE/gpu-job.sh /tmp/gpu-job.sh --quiet && bash /tmp/gpu-job.sh $STAGE"
# Spot by default; AMBIT_SPOT=0 launches on-demand (spored's cost limit is
# computed at the on-demand rate either way).
PURCHASE=(--spot)
[ "${AMBIT_SPOT:-1}" = 0 ] && PURCHASE=()
[ -n "${AMBIT_AZ:-}" ] && PURCHASE+=(--az "$AMBIT_AZ")
spawn launch "$NAME" --instance-type "$TYPE" --region "$REGION" ${PURCHASE[@]+"${PURCHASE[@]}"} \
	--ttl "$TTL" --cost-limit "$LIMIT" --on-complete terminate \
	--volume-size 80 --tag project=ambit --tag "run=$NAME" \
	--s3-read "$BUCKET" --s3-write "$BUCKET" \
	--command "$CMD" -y
echo "staged at $STAGE; logs at $STAGE/out/<host>/job.log"
