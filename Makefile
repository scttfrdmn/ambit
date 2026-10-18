.PHONY: build test corpus run report

ARM ?= b0

build:
	go build ./...

test:
	go test ./...

corpus:
	go run ./cmd/ambit corpus

run:
	go run ./cmd/ambit run -arm $(ARM)

report:
	go run ./cmd/ambit report
