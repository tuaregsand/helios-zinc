SHELL := /bin/bash

.PHONY: run-gateway run-decoder run-api run-replay run-edge fmt lint test-rust test-rust-it infra-aws-init infra-aws-plan infra-aws-apply infra-aws-deploy-control-plane infra-aws-deploy-all infra-aws-plan-control-plane infra-aws-run-replay load-test-k6 load-test-baseline

run-gateway:
	cd services/gateway-rs && cargo run -- --source yellowstone

run-decoder:
	cd services/decoder-rs && cargo run

run-api:
	cd services/api-go && go run ./cmd/api

run-replay:
	cd services/replay-go && go run ./cmd/replay -start 1 -end 100

run-edge:
	cd services/edge-go && go run ./cmd/edge

fmt:
	cd services/gateway-rs && cargo fmt
	cd services/decoder-rs && cargo fmt
	cd services/api-go && gofmt -w .
	cd services/edge-go && gofmt -w .
	cd services/replay-go && gofmt -w .

lint:
	cd services/gateway-rs && cargo clippy -- -D warnings
	cd services/decoder-rs && cargo clippy -- -D warnings
	cd services/api-go && go test ./...
	cd services/edge-go && go test ./...
	cd services/replay-go && go test ./...

test-rust:
	cd services/gateway-rs && cargo test
	cd services/decoder-rs && cargo test

test-rust-it:
	cd services/gateway-rs && HELIOS_IT_RUN=$${HELIOS_IT_RUN:-0} cargo test integration_
	cd services/decoder-rs && HELIOS_IT_RUN=$${HELIOS_IT_RUN:-0} cargo test integration_

infra-aws-init:
	cd infra/aws/terraform && terraform init

infra-aws-plan:
	cd infra/aws/terraform && terraform plan

infra-aws-apply:
	cd infra/aws/terraform && terraform apply

infra-aws-plan-control-plane:
	./infra/aws/scripts/plan-control-plane.sh

infra-aws-deploy-control-plane:
	./infra/aws/scripts/deploy-control-plane.sh

infra-aws-deploy-all:
	./infra/aws/scripts/deploy-all.sh

infra-aws-run-replay:
	@if [ -z "$$START_SLOT" ] || [ -z "$$END_SLOT" ]; then echo "START_SLOT and END_SLOT are required"; exit 1; fi
	./infra/aws/scripts/run-replay-task.sh -start "$$START_SLOT" -end "$$END_SLOT"

load-test-k6:
	@if ! command -v k6 >/dev/null 2>&1; then echo "k6 is required (https://k6.io/docs/get-started/installation/)"; exit 1; fi
	k6 run load/k6/control-plane.js

load-test-baseline:
	./load/ab/run-baseline.sh
