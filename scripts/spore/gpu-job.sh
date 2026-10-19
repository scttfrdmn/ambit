#!/bin/bash
# Runs on a spore.host GPU instance (Deep Learning Base AMI, AL2023).
#
#   gpu-job.sh <stage-s3-prefix>
#
# Fetches the staged bundle (ambit binary, corpus, and RUNS: one line of
# `ambit run` arguments per run), fetches the pinned model weights, starts the
# model servers the runs need, runs each line in order, and pushes
# the log and results to <stage>/out/<host>/ every minute while it runs and
# once at the end. No secrets are used or written. The exit code is ambit's
# last non-zero exit, so spawn's --on-complete sees failures.
set +e
set -u

STAGE="$1"

W=/work
sudo mkdir -p "$W" && sudo chown "$(id -u):$(id -g)" "$W"
cd "$W" || exit 2
OUT="$STAGE/out/$(hostname)"
LOG="$W/job.log"
log() { echo "$(date -u +%FT%TZ) $*" | tee -a "$LOG"; }

push() {
	aws s3 sync "$W/results" "$OUT/results" --quiet --exclude 'spend.jsonl'
	aws s3 cp "$LOG" "$OUT/job.log" --quiet
	for f in llama.log strands.log d3.log; do [ -f "$W/$f" ] && aws s3 cp "$W/$f" "$OUT/$f" --quiet; done
}
( while true; do sleep 60; push; done ) &
PUSHER=$!
# On any exit, stop the background jobs and push what there is, so the
# instance completes instead of idling until its TTL.
trap 'kill $PUSHER 2>/dev/null; sudo docker rm -f llama d3 >/dev/null 2>&1; pkill -f d3_server.py 2>/dev/null; pkill -f "strands-decider serve" 2>/dev/null; push' EXIT

log "host $(hostname)"
nvidia-smi --query-gpu=name,memory.total,driver_version --format=csv,noheader | tee -a "$LOG"

aws s3 cp "$STAGE/bundle.tgz" "$W/bundle.tgz" --quiet && tar xzf bundle.tgz || { log "bundle fetch failed"; push; exit 3; }
mkdir -p results
log "runs:"; sed 's/^/  /' RUNS | tee -a "$LOG"
ARMS=$(grep -oE -- '-arm [a-z0-9-]+' RUNS | awk '{print $2}' | sort -u | tr '\n' ' ')
# One llama-server slot per concurrent request across the Jebadiah runs.
NP=$(grep -E -- '-arm (a-local|b-4b|b)( |$)' RUNS | grep -oE -- '-concurrency [0-9]+' | awk '{print $2}' | sort -n | tail -1)
NP=${NP:-1}

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
needs_d3() {
	grep -qE -- '-arm a-d3( |$)' RUNS
}
needs_strands() {
	for a in $ARMS; do case "$a" in a-strands | a-strands-* | b-strands) return 0 ;; esac; done
	return 1
}

# A --command instance has no Docker; install it and NVIDIA's container
# toolkit if the AMI lacks them.
ensure_docker() {
	command -v docker >/dev/null || sudo dnf install -y -q docker >>"$LOG" 2>&1
	if ! command -v nvidia-ctk >/dev/null; then
		curl -fsSL https://nvidia.github.io/libnvidia-container/stable/rpm/nvidia-container-toolkit.repo |
			sudo tee /etc/yum.repos.d/nvidia-container-toolkit.repo >/dev/null
		sudo dnf install -y -q nvidia-container-toolkit >>"$LOG" 2>&1
	fi
	sudo nvidia-ctk runtime configure --runtime=docker >>"$LOG" 2>&1
	sudo systemctl restart docker
}

wait_health() {
	for _ in $(seq 1 180); do curl -fsS "$1/health" >/dev/null 2>&1 && return 0; sleep 5; done
	return 1
}

if needs_llama; then
	ensure_docker
	log "fetching jebadiah"
	curl -fsSL -o jeb.gguf "$JEB_URL" && echo "$JEB_SHA  jeb.gguf" | sha256sum -c --quiet || { log "jebadiah fetch or hash failed"; push; exit 4; }
	# One slot per concurrent request, 4096 tokens each: every Arm A state
	# fits (max 1,748 tokens in G1). --cache-ram 0 turns off the host-RAM
	# prompt cache, which thrashed a 16 GiB g6.xlarge at 16 slots and hung the
	# server; per-slot prefix reuse (cache_prompt) does not need it.
	sudo docker run -d --gpus all --name llama -p 127.0.0.1:8080:8080 -v "$W:/models:ro" "$LLAMA_IMAGE" \
		-m /models/jeb.gguf -c $((NP * 4096)) -np "$NP" -ngl 999 --cache-ram 0 --host 0.0.0.0 --port 8080 >>"$LOG" 2>&1
	( sudo docker logs -f llama >"$W/llama.log" 2>&1 ) &
	wait_health http://127.0.0.1:8080 || { log "llama-server did not come up"; push; exit 5; }
	log "llama-server up"
fi

# d3 (registration #33, item 14): ggml-org's Q8_0 GGUF on llama.cpp, one
# slot (llama.cpp issue #30402), whole prompt in one ubatch; or, with
# "#d3-official" in RUNS, the model repository's own BF16 server.
D3_URL=https://huggingface.co/ggml-org/vllm-d3-GGUF/resolve/de9670136f5ddd83e677c89ce041d5b9bb3b0dcb/vllm-d3-Q8_0.gguf
D3_SHA=82130d83cb6f31d43a742a08f737a78c087e408776115b6f28e5d3bc27617805
if needs_d3 && grep -q '^#d3-official' RUNS; then
	log "installing the official d3 runtime"
	curl -LsSf https://astral.sh/uv/install.sh | sh >>"$LOG" 2>&1
	export PATH="$HOME/.local/bin:$PATH"
	uv venv --python 3.12 "$W/d3venv" >>"$LOG" 2>&1 && . "$W/d3venv/bin/activate"
	uv pip install "transformers==5.17.0" torch torchvision pillow opencv-python-headless safetensors accelerate "flash-linear-attention==0.5.2" huggingface_hub >>"$LOG" 2>&1
	hf download vllm-sr/d3 --revision cf15549a78fc52598ddd1951f321c58372bbd64f --local-dir "$W/d3" >>"$LOG" 2>&1
	( python "$W/d3/d3_server.py" --model "$W/d3" --device cuda:0 --host 127.0.0.1 --port 8091 >"$W/d3.log" 2>&1 ) &
	deactivate
	wait_health http://127.0.0.1:8091 || { log "official d3 server did not come up"; exit 7; }
	log "official d3 server up"
elif needs_d3; then
	log "fetching d3 Q8_0"
	curl -fsSL -o d3.gguf "$D3_URL" && echo "$D3_SHA  d3.gguf" | sha256sum -c --quiet || { log "d3 fetch or hash failed"; exit 8; }
	ensure_docker
	sudo docker run -d --gpus all --name d3 -p 127.0.0.1:8090:8080 -v "$W:/models:ro" "$LLAMA_IMAGE" \
		-m /models/d3.gguf -c 4096 -np 1 -ub 4096 -b 4096 -ngl 999 --cache-ram 0 --host 0.0.0.0 --port 8080 >>"$LOG" 2>&1
	( sudo docker logs -f d3 >"$W/d3.log" 2>&1 ) &
	wait_health http://127.0.0.1:8090 || { log "d3 llama-server did not come up"; exit 8; }
	log "d3 llama-server up"
fi

if needs_strands; then
	log "installing strands-decider"
	# AL2023's python3 is 3.9; strands-decider needs >= 3.10.
	curl -LsSf https://astral.sh/uv/install.sh | sh >>"$LOG" 2>&1
	export PATH="$HOME/.local/bin:$PATH"
	uv venv --python 3.12 "$W/sd" >>"$LOG" 2>&1 && . "$W/sd/bin/activate"
	uv pip install "torch==2.7.1" >>"$LOG" 2>&1
	uv pip install "strands-decider @ git+https://github.com/strands-labs/strands-decider@930e6d5de16309be32579f5631a7369a3322140c" "huggingface_hub" >>"$LOG" 2>&1
	# "#strands-cuda" in RUNS adds the package's optional CUDA kernels for the
	# linear-attention layers (its cuda extra and causal-conv1d); without them
	# it uses a slower PyTorch fallback.
	if grep -q '^#strands-cuda' RUNS; then
		export PATH="$PATH:/usr/local/cuda/bin"
		uv pip install "strands-decider[cuda] @ git+https://github.com/strands-labs/strands-decider@930e6d5de16309be32579f5631a7369a3322140c" >>"$LOG" 2>&1 &&
			uv pip install causal-conv1d --no-build-isolation >>"$LOG" 2>&1 &&
			log "strands-decider cuda kernels installed" || log "strands-decider cuda kernels failed to install; using the fallback"
	fi
	hf download StrandsAgents/strands-decider-2B-hobson-v21 --revision 2b52a6235c1b8306bbfa30b00b9d4b74b63a39f5 --local-dir "$W/v21" >>"$LOG" 2>&1
	( strands-decider serve "$W/v21" --host 127.0.0.1 --port 8000 --device cuda >"$W/strands.log" 2>&1 ) &
	deactivate
	wait_health http://127.0.0.1:8000 || { log "strands-decider did not come up"; push; exit 6; }
	log "strands-decider up"
fi

rc=0
while IFS= read -r line; do
	case "$line" in "" | "#"*) continue ;; esac
	log "run: $line"
	read -r -a args <<<"$line"
	./ambit run "${args[@]}" -results results >>"$LOG" 2>&1 </dev/null &
	pid=$!
	# Watchdog: a run whose output stops growing for 10 minutes is stuck;
	# kill it so the instance moves on instead of idling until its TTL.
	last=$(wc -c <"$LOG"); idle=0
	while kill -0 $pid 2>/dev/null; do
		sleep 30
		now=$(wc -c <"$LOG")
		if [ "$now" -eq "$last" ]; then idle=$((idle + 30)); else idle=0; last=$now; fi
		if [ $idle -ge 600 ]; then log "no progress for 10 minutes; killing: $line"; kill $pid; sleep 5; kill -9 $pid 2>/dev/null; fi
	done
	wait $pid
	r=$?
	log "exit $r: $line"
	[ $r -ne 0 ] && rc=$r
done <RUNS

log "done, exit $rc"
exit $rc
