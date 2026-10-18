.PHONY: build test corpus verify model serve run report

ARM ?= b0
# Outputs, weights and logs live on the external disk (~/.xtmp), not the repo.
RESULTS ?= $(HOME)/.xtmp/ambit-results
MODELS ?= $(HOME)/.xtmp/ambit-models
LLAMA ?= http://127.0.0.1:8080
GGUF_REPO = frontier-infra/jebadiah-4b-v2-GGUF
GGUF_REV = 446099f37ca1cea4324d8866b44a1dbc15f77c94
GGUF_FILE = jebadiah-4b-v2-Q8_0.gguf
GGUF_SHA256 = 36f4a54fe07ed79b186a2128bfb1598ba210b1ab0a945296069a4e5cdbb9875a

build:
	go build ./...

test:
	go test ./...

corpus:
	go run ./cmd/ambit corpus

verify:
	go run ./cmd/ambit verify

model:
	HF_HOME=$(MODELS)/hf-home hf download $(GGUF_REPO) --revision $(GGUF_REV) --include $(GGUF_FILE) --local-dir $(MODELS)/jebadiah-4b-v2-GGUF
	echo "$(GGUF_SHA256)  $(MODELS)/jebadiah-4b-v2-GGUF/$(GGUF_FILE)" | shasum -a 256 -c

serve:
	llama-server -m $(MODELS)/jebadiah-4b-v2-GGUF/$(GGUF_FILE) -c 8192 -np 1 --host 127.0.0.1 --port 8080

run:
	go run ./cmd/ambit run -arm $(ARM) -results $(RESULTS) -llama $(LLAMA)

report:
	go run ./cmd/ambit report -results $(RESULTS)
