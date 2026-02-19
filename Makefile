SHELL := /bin/bash

.PHONY: run-gateway run-decoder run-api run-replay fmt lint

run-gateway:
	cd services/gateway-rs && cargo run -- --source yellowstone

run-decoder:
	cd services/decoder-rs && cargo run

run-api:
	cd services/api-go && go run ./cmd/api

run-replay:
	cd services/replay-go && go run ./cmd/replay -start 1 -end 100

fmt:
	cd services/gateway-rs && cargo fmt
	cd services/decoder-rs && cargo fmt
	cd services/api-go && gofmt -w .
	cd services/replay-go && gofmt -w .

lint:
	cd services/gateway-rs && cargo clippy -- -D warnings
	cd services/decoder-rs && cargo clippy -- -D warnings
	cd services/api-go && go test ./...
	cd services/replay-go && go test ./...
