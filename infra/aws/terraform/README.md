# AWS Terraform Stack

This stack deploys Helios services on AWS ECS Fargate.

## What it creates

- VPC with public/private subnets across 2-3 AZs
- NAT and routing for private ECS tasks
- Public ALB for inbound traffic to `edge-go`
- Internal ALB for private fanout to `api-go`
- ECS cluster and services:
  - `edge-go`
  - `api-go`
  - `gateway-rs`
  - `decoder-rs`
- Replay run-task task definition (`replay-go`) for ad-hoc replay jobs
- Target groups and health checks
- Autoscaling policies (CPU + memory)
- Security groups with least-privilege service boundaries
- Secrets Manager integration for sensitive env vars
- CloudWatch log groups

## Traffic model

- Internet -> Public ALB -> `edge-go`
- `edge-go` -> Internal ALB -> `api-go`
- `gateway-rs` -> Kafka/Redpanda
- `decoder-rs` -> ClickHouse
- `replay-go` -> Postgres (run-task)

## Prerequisites

- Terraform `>= 1.6`
- AWS credentials with permissions for VPC, ECS, ALB, IAM, Logs, AutoScaling, ECR, Secrets Manager
- Container images pushed to ECR (or use deploy script)
- External dependencies reachable from private subnets:
  - ClickHouse
  - Postgres/Neon
  - Yellowstone/Helius endpoint (if gateway source is `yellowstone`)
- Kafka dependency:
  - AWS MSK when `enable_msk=true`
  - External Kafka/Redpanda only when `enable_msk=false`

## Deploy

```bash
cd infra/aws/terraform
cp terraform.tfvars.example terraform.tfvars
terraform init
terraform plan
terraform apply
```

Scripted deployment:

```bash
PROJECT=helios \
ENVIRONMENT=prod \
AWS_REGION=us-east-1 \
ENABLE_AWS_MSK=true \
HELIOS_CLICKHOUSE_URL='https://clickhouse-host:8443' \
HELIOS_CLICKHOUSE_DB='helios' \
HELIOS_CLICKHOUSE_USER='helios' \
HELIOS_PG_DSN='postgres://user:pass@host/db?sslmode=require' \
HELIUS_LASERSTREAM_ENDPOINT='https://mainnet.helius-rpc.com/?api-key=replace-me' \
HELIUS_LASERSTREAM_API_KEY='replace-me' \
HELIOS_CLICKHOUSE_PASSWORD='replace-me' \
HELIOS_API_KEYS='replace-with-strong-key' \
HELIOS_OPENAPI_TOKEN='replace-with-openapi-token' \
HELIOS_EDGE_ADMIN_TOKEN='replace-with-edge-admin-token' \
./infra/aws/scripts/deploy-all.sh
```

If `ENABLE_AWS_MSK=false`, set `HELIOS_KAFKA_BROKERS='b-1.kafka:9092,b-2.kafka:9092'`.

## Secrets setup

If `manage_secrets=true`, Terraform creates secret containers only. Set values after apply.

Example:

```bash
aws secretsmanager put-secret-value --secret-id /helios/prod/api/pg_dsn --secret-string 'postgres://...'
aws secretsmanager put-secret-value --secret-id /helios/prod/gateway/helius_laserstream_api_key --secret-string 'replace-me'
aws secretsmanager put-secret-value --secret-id /helios/prod/decoder/clickhouse_password --secret-string 'replace-me'
```

Then force a new deployment so tasks pick up secret updates:

```bash
aws ecs update-service --cluster helios-prod-cluster --service helios-prod-api --force-new-deployment
aws ecs update-service --cluster helios-prod-cluster --service helios-prod-edge --force-new-deployment
aws ecs update-service --cluster helios-prod-cluster --service helios-prod-gateway --force-new-deployment
aws ecs update-service --cluster helios-prod-cluster --service helios-prod-decoder --force-new-deployment
```

## Notes

- `api_pg_dsn_secret_arn` is mandatory (directly or via managed secret container).
- If `api_rate_limit_store=redis`, set `api_rate_limit_redis_addr`.
- If `gateway_source=yellowstone`, set at least one of:
  - `gateway_yellowstone_endpoint`
  - `gateway_helius_laserstream_endpoint`
- For TLS ingress, set `enable_https=true` and `acm_certificate_arn`.
