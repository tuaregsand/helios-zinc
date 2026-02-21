# AWS Deployment Guide

This guide deploys the full Helios service set on AWS:

- `edge-go`
- `api-go`
- `gateway-rs`
- `decoder-rs`
- `replay-go` (as ad-hoc ECS run-task)

## 1) Prerequisites

- AWS account and IAM credentials configured (`aws sts get-caller-identity` works)
- Docker installed and running
- Terraform `>= 1.6`
- External dependencies reachable from private subnets:
  - Kafka/Redpanda (only if `ENABLE_AWS_MSK=false`)
  - ClickHouse
  - Postgres/Neon
  - Yellowstone or Helius Laserstream endpoint
- ACM cert in your target region if you want HTTPS ALB listener

## 2) One-command deploy

Set required env and run:

```bash
export PROJECT=helios
export ENVIRONMENT=prod
export AWS_REGION=us-east-1

# Option A: use in-house AWS MSK (recommended)
export ENABLE_AWS_MSK=true

# Option B: use external Kafka
# export ENABLE_AWS_MSK=false
# export HELIOS_KAFKA_BROKERS='b-1.kafka:9092,b-2.kafka:9092'

export HELIOS_RAW_TX_TOPIC='helios.raw_tx'
export HELIOS_CLUSTER='mainnet-beta'

export HELIOS_GATEWAY_SOURCE='yellowstone'
export HELIUS_LASERSTREAM_ENDPOINT='https://mainnet.helius-rpc.com/?api-key=replace-me'
export HELIUS_LASERSTREAM_API_KEY='replace-me'
# optional alternative:
# export HELIOS_YELLOWSTONE_ENDPOINT='https://your-yellowstone-endpoint:443'
# export HELIOS_YELLOWSTONE_X_TOKEN='replace-me'

export HELIOS_CLICKHOUSE_URL='https://clickhouse-host:8443'
export HELIOS_CLICKHOUSE_DB='helios'
export HELIOS_CLICKHOUSE_USER='helios'
export HELIOS_CLICKHOUSE_PASSWORD='replace-me'

export HELIOS_PG_DSN='postgres://user:pass@host/db?sslmode=require'
export HELIOS_API_KEYS='replace-with-strong-key'
export HELIOS_OPENAPI_TOKEN='replace-with-openapi-token'
export HELIOS_EDGE_ADMIN_TOKEN='replace-with-edge-admin-token'

./infra/aws/scripts/deploy-all.sh
```

What this script does:

- creates ECR repos if missing
- builds and pushes images for `api`, `edge`, `gateway`, `decoder`, `replay`
- applies Terraform with those image tags and runtime variables
- writes provided secret values to Secrets Manager
- forces ECS rolling deployments for running services
- prints ALB ingress DNS

## 3) Optional: plan before apply

If you already have image URIs:

```bash
export API_IMAGE=123456789012.dkr.ecr.us-east-1.amazonaws.com/helios-prod-api:abc123
export EDGE_IMAGE=123456789012.dkr.ecr.us-east-1.amazonaws.com/helios-prod-edge:abc123
export GATEWAY_IMAGE=123456789012.dkr.ecr.us-east-1.amazonaws.com/helios-prod-gateway:abc123
export DECODER_IMAGE=123456789012.dkr.ecr.us-east-1.amazonaws.com/helios-prod-decoder:abc123
export REPLAY_IMAGE=123456789012.dkr.ecr.us-east-1.amazonaws.com/helios-prod-replay:abc123

export ENABLE_AWS_MSK=true
# or export HELIOS_KAFKA_BROKERS='b-1.kafka:9092,b-2.kafka:9092' when ENABLE_AWS_MSK=false
export HELIOS_CLICKHOUSE_URL='https://clickhouse-host:8443'
export HELIUS_LASERSTREAM_ENDPOINT='https://mainnet.helius-rpc.com/?api-key=replace-me'

./infra/aws/scripts/plan-control-plane.sh
```

## 4) Run replay task

Replay is deployed as a task definition and executed on demand:

```bash
./infra/aws/scripts/run-replay-task.sh -start 320000000 -end 320000500 -cluster mainnet-beta
```

Or with make:

```bash
START_SLOT=320000000 END_SLOT=320000500 make infra-aws-run-replay
```

## 5) TLS + domain

1. Set `enable_https=true` and `acm_certificate_arn` in `infra/aws/terraform/terraform.tfvars`.
2. Point Route53 (or external DNS) record to `public_alb_dns_name` output.
3. Keep HTTP listener if desired, or remove it in Terraform.

## 6) Post-deploy checks

Use the ALB DNS (or your domain):

```bash
ALB_DNS=$(cd infra/aws/terraform && terraform output -raw public_alb_dns_name)

curl -i "http://${ALB_DNS}/healthz"
curl -i "http://${ALB_DNS}/readyz"
curl -i -H 'X-API-Key: <key>' "http://${ALB_DNS}/v1/watermarks"
curl -i -H 'X-API-Key: <key>' -H 'X-OpenAPI-Token: <token>' "http://${ALB_DNS}/openapi.yaml"
```

## 7) Scaling controls

Terraform variables control autoscaling:

- API: `api_min_count`, `api_max_count`, `api_target_cpu`, `api_target_memory`
- Edge: `edge_min_count`, `edge_max_count`, `edge_target_cpu`, `edge_target_memory`
- Gateway: `gateway_min_count`, `gateway_max_count`, `gateway_target_cpu`, `gateway_target_memory`
- Decoder: `decoder_min_count`, `decoder_max_count`, `decoder_target_cpu`, `decoder_target_memory`
