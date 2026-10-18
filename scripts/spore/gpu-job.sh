#!/bin/bash
# Runs on a spore.host GPU instance (Deep Learning Base AMI, AL2023).
#
#   gpu-job.sh <stage-s3-prefix> "<arms>" [extra ambit run flags...]
#
# Fetches the staged bundle (ambit binary + corpus), fetches the pinned model
# weights, starts the model servers the arms need, runs each arm, and pushes
# the log and results to <stage>/out/<host>/ every minute while it runs and
# once at the end. No secrets are used or written. The exit code is ambit's
# last non-zero exit, so spawn's --on-complete sees failures.
set +e
set -u

STAGE="$1"
ARMS="$2"
shift 2
EXTRA=("$@")

W=/work
sudo mkdir -p "$W" && sudo chown "$(id -u):$(id -g)" "$W"
cd "$W" || exit 2
OUT="$STAGE/out/$(hostname)"
LOG="$W/job.log"
log() { echo "$(date -u +%FT%TZ) $*" | tee -a "$LOG"; }

push() {
	aws s3 sync "$W/results" "$OUT/results" --quiet --exclude 'spend.jsonl'
	aws s3 cp "$LOG" "$OUT/job.log" --quiet
	for f in llama.log strands.log; do [ -f "$W/$f" ] && aws s3 cp "$W/$f" "$OUT/$f" --quiet; done
}
( while true; do sleep 60; push; done ) &
PUSHER=$!
# On any exit, stop the background jobs and push what there is, so the
# instance completes instead of idling until its TTL.
trap 'kill $PUSHER 2>/dev/null; docker rm -f llama >/dev/null 2>&1; pkill -f "strands-decider serve" 2>/dev/null; push' EXIT

log "host $(hostname); arms: $ARMS; extra: ${EXTRA[*]:-}"
nvidia-smi --query-gpu=name,memory.total,driver_version --format=csv,noheader | tee -a "$LOG"

aws s3 cp "$STAGE/bundle.tgz" "$W/bundle.tgz" --quiet && tar xzf bundle.tgz || { log "bundle fetch failed"; push; exit 3; }
mkdir -p results

# Jebadiah 4B v2, pinned (see internal/jeb.Jebadiah4BV2).
JEB_URL=https://huggingface.co/frontier-infra/jebadiah-4b-v2-GGUF/resolve/446099f37ca1cea4324d8866b44a1dbc15f77c94/jebadiah-4b-v2-Q8_0.gguf
JEB_SHA=36f4a54fe07ed79b186a2128bfb1598ba210b1ab0a945296069a4e5cdbb9875a
# llama.cpp build 11669, the build G1 ran locally.
LLAMA_IMAGE=ghcr.io/ggml-org/llama.cpp:server-cuda-b11669@sha256:e4cfff28678327b0ae7eda129e700e56a0fa55aa23db2f016256ea155c15c2b5

# needs_llama / needs_strands: whether any requested arm uses that server.
needs_llama() {
	for a in $ARMS; do case "$a" in a-local | a-local-* | b | b-4b) return 0 ;; esac; done
	return 1
}
needs_strands() {
	for a in $ARMS; do case "$a" in a-strands | a-strands-* | b-strands) return 0 ;; esac; done
	return 1
}

wait_health() {
	for _ in $(seq 1 180); do curl -fsS "$1/health" >/dev/null 2>&1 && return 0; sleep 5; done
	return 1
}

if needs_llama; then
	log "fetching jebadiah"
	curl -fsSL -o jeb.gguf "$JEB_URL" && echo "$JEB_SHA  jeb.gguf" | sha256sum -c --quiet || { log "jebadiah fetch or hash failed"; push; exit 4; }
	# 8 slots x 4096 tokens: every Arm A state fits (max 1,748 tokens in G1).
	docker run -d --gpus all --name llama -p 127.0.0.1:8080:8080 -v "$W:/models:ro" "$LLAMA_IMAGE" \
		-m /models/jeb.gguf -c 32768 -np 8 -ngl 999 --host 0.0.0.0 --port 8080 >>"$LOG" 2>&1
	( docker logs -f llama >"$W/llama.log" 2>&1 ) &
	wait_health http://127.0.0.1:8080 || { log "llama-server did not come up"; push; exit 5; }
	log "llama-server up"
fi

if needs_strands; then
	log "installing strands-decider"
	# AL2023's python3 is 3.9; strands-decider needs >= 3.10.
	curl -LsSf https://astral.sh/uv/install.sh | sh >>"$LOG" 2>&1
	export PATH="$HOME/.local/bin:$PATH"
	uv venv --python 3.12 "$W/sd" >>"$LOG" 2>&1 && . "$W/sd/bin/activate"
	uv pip install "torch==2.7.1" >>"$LOG" 2>&1
	uv pip install "strands-decider @ git+https://github.com/strands-labs/strands-decider@930e6d5de16309be32579f5631a7369a3322140c" "huggingface_hub" >>"$LOG" 2>&1
	hf download StrandsAgents/strands-decider-2B-hobson-v21 --revision 2b52a6235c1b8306bbfa30b00b9d4b74b63a39f5 --local-dir "$W/v21" >>"$LOG" 2>&1
	( strands-decider serve "$W/v21" --host 127.0.0.1 --port 8000 --device cuda >"$W/strands.log" 2>&1 ) &
	deactivate
	wait_health http://127.0.0.1:8000 || { log "strands-decider did not come up"; push; exit 6; }
	log "strands-decider up"
fi

rc=0
for arm in $ARMS; do
	log "run $arm"
	./ambit run -arm "$arm" -results results "${EXTRA[@]}" >>"$LOG" 2>&1
	r=$?
	log "run $arm exit $r"
	[ $r -ne 0 ] && rc=$r
done

log "done, exit $rc"
exit $rc
