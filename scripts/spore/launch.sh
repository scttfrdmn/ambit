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
# spawn reads a cost limit of 0 as "no limit", so 0 is refused too.
if ! [[ "$LIMIT" =~ ^[0-9]+(\.[0-9]+)?$ ]] || awk "BEGIN{exit !($LIMIT > 20 || $LIMIT <= 0)}"; then
	echo "cost limit must be a number above 0 and no greater than 20 (Law 6), got '$LIMIT'" >&2
	exit 1
fi
. "$(dirname "$0")/ttl.sh"
ttl_minutes "$TTL" >/dev/null || { echo "a TTL spawn and the dead-man watcher can both read is required (e.g. 8h, 1d12h)" >&2; exit 1; }
REGION="${AMBIT_REGION:-us-west-2}"
TYPE="${AMBIT_INSTANCE:-g6.xlarge}"
ACCOUNT=$(aws sts get-caller-identity --query Account --output text)
BUCKET="spawn-results-$ACCOUNT-$REGION"
STAGE="s3://$BUCKET/ambit/$NAME"
# The sweep after a failed launch terminates every instance tagged with this
# run name, so a name that is already live is refused rather than swept.
if [ -n "$(aws ec2 describe-instances --region "$REGION" \
	--filters "Name=tag:project,Values=ambit" "Name=tag:run,Values=$NAME" \
	"Name=instance-state-name,Values=pending,running,stopping,stopped" \
	--query 'Reservations[].Instances[].InstanceId' --output text)" ]; then
	echo "an instance of $NAME is already live in $REGION; choose another name" >&2
	exit 1
fi
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
if ! spawn launch "$NAME" --instance-type "$TYPE" --region "$REGION" ${PURCHASE[@]+"${PURCHASE[@]}"} \
	--ttl "$TTL" --cost-limit "$LIMIT" --on-complete terminate \
	--volume-size "${AMBIT_VOLUME:-80}" --tag project=ambit --tag "run=$NAME" \
	--s3-read "$BUCKET" --s3-write "$BUCKET" \
	--command "$CMD" -y; then
	# spawn can create the instance and still report failure; nothing would
	# watch it (on 2026-10-20 one ran 74 h past its TTL). Give EC2 a minute
	# to show it, then terminate whatever this attempt left.
	sleep 60
	scripts/spore/sweep.sh "$NAME" "$REGION" >&2
	exit 1
fi
# Local backstop: terminate the instance through the EC2 API 15 minutes past
# its TTL if it is still alive (see deadman.sh).
mkdir -p "$HOME/.xtmp/ambit-logs"
# Run private copies: bash reads a script as it executes, so editing
# deadman.sh during the watcher's sleep would corrupt it.
DM="$HOME/.xtmp/ambit-logs/deadman-$NAME"
mkdir -p "$DM" && cp scripts/spore/deadman.sh scripts/spore/ttl.sh scripts/spore/sweep.sh "$DM/"
nohup "$DM/deadman.sh" "$NAME" "$REGION" "$TTL" >"$DM.log" 2>&1 &
echo "staged at $STAGE; logs at $STAGE/out/<host>/job.log; dead-man watcher pid $!"
