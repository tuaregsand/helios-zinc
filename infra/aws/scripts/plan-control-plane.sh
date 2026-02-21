#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
TF_DIR="$ROOT_DIR/infra/aws/terraform"

PROJECT="${PROJECT:-helios}"
ENVIRONMENT="${ENVIRONMENT:-prod}"
AWS_REGION="${AWS_REGION:-us-east-1}"
MANAGE_SECRETS="${MANAGE_SECRETS:-true}"
ENABLE_AWS_MSK="${ENABLE_AWS_MSK:-false}"
MSK_KAFKA_VERSION="${MSK_KAFKA_VERSION:-3.6.0}"
MSK_BROKER_COUNT="${MSK_BROKER_COUNT:-2}"
MSK_INSTANCE_TYPE="${MSK_INSTANCE_TYPE:-kafka.m5.large}"
MSK_EBS_VOLUME_SIZE="${MSK_EBS_VOLUME_SIZE:-100}"
MSK_CLIENT_BROKER="${MSK_CLIENT_BROKER:-PLAINTEXT}"
MSK_CLOUDWATCH_LOGS_ENABLED="${MSK_CLOUDWATCH_LOGS_ENABLED:-true}"

API_IMAGE="${API_IMAGE:-}"
EDGE_IMAGE="${EDGE_IMAGE:-}"
GATEWAY_IMAGE="${GATEWAY_IMAGE:-}"
DECODER_IMAGE="${DECODER_IMAGE:-}"
REPLAY_IMAGE="${REPLAY_IMAGE:-}"

KAFKA_BROKERS="${HELIOS_KAFKA_BROKERS:-}"
RAW_TX_TOPIC="${HELIOS_RAW_TX_TOPIC:-helios.raw_tx}"
PIPELINE_CLUSTER="${HELIOS_CLUSTER:-mainnet-beta}"
GATEWAY_SOURCE="${HELIOS_GATEWAY_SOURCE:-yellowstone}"
YELLOWSTONE_ENDPOINT="${HELIOS_YELLOWSTONE_ENDPOINT:-}"
HELIUS_ENDPOINT="${HELIUS_LASERSTREAM_ENDPOINT:-}"
CLICKHOUSE_URL="${HELIOS_CLICKHOUSE_URL:-}"
CLICKHOUSE_DB="${HELIOS_CLICKHOUSE_DB:-helios}"
CLICKHOUSE_USER="${HELIOS_CLICKHOUSE_USER:-helios}"
DECODER_GROUP="${HELIOS_DECODER_GROUP:-helios-decoder}"

if [[ -z "$API_IMAGE" || -z "$EDGE_IMAGE" || -z "$GATEWAY_IMAGE" || -z "$DECODER_IMAGE" || -z "$REPLAY_IMAGE" ]]; then
  echo "API_IMAGE EDGE_IMAGE GATEWAY_IMAGE DECODER_IMAGE REPLAY_IMAGE are required for plan" >&2
  exit 1
fi

if [[ "$ENABLE_AWS_MSK" != "true" && -z "$KAFKA_BROKERS" ]]; then
  echo "HELIOS_KAFKA_BROKERS is required for plan unless ENABLE_AWS_MSK=true" >&2
  exit 1
fi

if [[ -z "$CLICKHOUSE_URL" ]]; then
  echo "HELIOS_CLICKHOUSE_URL is required for plan" >&2
  exit 1
fi

if [[ "$GATEWAY_SOURCE" == "yellowstone" && -z "$YELLOWSTONE_ENDPOINT" && -z "$HELIUS_ENDPOINT" ]]; then
  echo "gateway source yellowstone requires HELIOS_YELLOWSTONE_ENDPOINT or HELIUS_LASERSTREAM_ENDPOINT" >&2
  exit 1
fi

cd "$TF_DIR"
terraform init
terraform plan \
  -var "aws_region=${AWS_REGION}" \
  -var "project=${PROJECT}" \
  -var "environment=${ENVIRONMENT}" \
  -var "api_image=${API_IMAGE}" \
  -var "edge_image=${EDGE_IMAGE}" \
  -var "gateway_image=${GATEWAY_IMAGE}" \
  -var "decoder_image=${DECODER_IMAGE}" \
  -var "replay_image=${REPLAY_IMAGE}" \
  -var "manage_secrets=${MANAGE_SECRETS}" \
  -var "enable_msk=${ENABLE_AWS_MSK}" \
  -var "msk_kafka_version=${MSK_KAFKA_VERSION}" \
  -var "msk_broker_count=${MSK_BROKER_COUNT}" \
  -var "msk_instance_type=${MSK_INSTANCE_TYPE}" \
  -var "msk_ebs_volume_size=${MSK_EBS_VOLUME_SIZE}" \
  -var "msk_client_broker=${MSK_CLIENT_BROKER}" \
  -var "msk_cloudwatch_logs_enabled=${MSK_CLOUDWATCH_LOGS_ENABLED}" \
  -var "cluster=${PIPELINE_CLUSTER}" \
  -var "kafka_brokers=${KAFKA_BROKERS}" \
  -var "raw_tx_topic=${RAW_TX_TOPIC}" \
  -var "gateway_source=${GATEWAY_SOURCE}" \
  -var "gateway_yellowstone_endpoint=${YELLOWSTONE_ENDPOINT}" \
  -var "gateway_helius_laserstream_endpoint=${HELIUS_ENDPOINT}" \
  -var "clickhouse_url=${CLICKHOUSE_URL}" \
  -var "clickhouse_db=${CLICKHOUSE_DB}" \
  -var "clickhouse_user=${CLICKHOUSE_USER}" \
  -var "decoder_group=${DECODER_GROUP}"
