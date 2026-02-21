#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
TF_DIR="$ROOT_DIR/infra/aws/terraform"

PROJECT="${PROJECT:-helios}"
ENVIRONMENT="${ENVIRONMENT:-prod}"
AWS_REGION="${AWS_REGION:-us-east-1}"
TAG="${TAG:-$(git -C "$ROOT_DIR" rev-parse --short HEAD 2>/dev/null || date +%Y%m%d%H%M%S)}"
TF_AUTO_APPROVE="${TF_AUTO_APPROVE:-false}"
MANAGE_SECRETS="${MANAGE_SECRETS:-true}"
ENABLE_AWS_MSK="${ENABLE_AWS_MSK:-false}"
MSK_KAFKA_VERSION="${MSK_KAFKA_VERSION:-3.6.0}"
MSK_BROKER_COUNT="${MSK_BROKER_COUNT:-2}"
MSK_INSTANCE_TYPE="${MSK_INSTANCE_TYPE:-kafka.m5.large}"
MSK_EBS_VOLUME_SIZE="${MSK_EBS_VOLUME_SIZE:-100}"
MSK_CLIENT_BROKER="${MSK_CLIENT_BROKER:-PLAINTEXT}"
MSK_CLOUDWATCH_LOGS_ENABLED="${MSK_CLOUDWATCH_LOGS_ENABLED:-true}"

API_REPO="${API_REPO:-${PROJECT}-${ENVIRONMENT}-api}"
EDGE_REPO="${EDGE_REPO:-${PROJECT}-${ENVIRONMENT}-edge}"
GATEWAY_REPO="${GATEWAY_REPO:-${PROJECT}-${ENVIRONMENT}-gateway}"
DECODER_REPO="${DECODER_REPO:-${PROJECT}-${ENVIRONMENT}-decoder}"
REPLAY_REPO="${REPLAY_REPO:-${PROJECT}-${ENVIRONMENT}-replay}"

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

require_cmd() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "missing required command: $1" >&2
    exit 1
  fi
}

require_cmd aws
require_cmd docker
require_cmd terraform

if [[ "$ENABLE_AWS_MSK" != "true" && -z "$KAFKA_BROKERS" ]]; then
  echo "HELIOS_KAFKA_BROKERS is required" >&2
  exit 1
fi

if [[ -z "$CLICKHOUSE_URL" ]]; then
  echo "HELIOS_CLICKHOUSE_URL is required" >&2
  exit 1
fi

if [[ "$GATEWAY_SOURCE" == "yellowstone" && -z "$YELLOWSTONE_ENDPOINT" && -z "$HELIUS_ENDPOINT" ]]; then
  echo "gateway source yellowstone requires HELIOS_YELLOWSTONE_ENDPOINT or HELIUS_LASERSTREAM_ENDPOINT" >&2
  exit 1
fi

aws sts get-caller-identity --output json >/dev/null

ACCOUNT_ID="$(aws sts get-caller-identity --query Account --output text)"
ECR_REGISTRY="${ACCOUNT_ID}.dkr.ecr.${AWS_REGION}.amazonaws.com"
API_IMAGE="${ECR_REGISTRY}/${API_REPO}:${TAG}"
EDGE_IMAGE="${ECR_REGISTRY}/${EDGE_REPO}:${TAG}"
GATEWAY_IMAGE="${ECR_REGISTRY}/${GATEWAY_REPO}:${TAG}"
DECODER_IMAGE="${ECR_REGISTRY}/${DECODER_REPO}:${TAG}"
REPLAY_IMAGE="${ECR_REGISTRY}/${REPLAY_REPO}:${TAG}"

ensure_ecr_repo() {
  local repo="$1"
  if ! aws ecr describe-repositories --repository-names "$repo" --region "$AWS_REGION" >/dev/null 2>&1; then
    aws ecr create-repository \
      --region "$AWS_REGION" \
      --repository-name "$repo" \
      --image-scanning-configuration scanOnPush=true >/dev/null
  fi
}

echo "ensuring ECR repositories"
ensure_ecr_repo "$API_REPO"
ensure_ecr_repo "$EDGE_REPO"
ensure_ecr_repo "$GATEWAY_REPO"
ensure_ecr_repo "$DECODER_REPO"
ensure_ecr_repo "$REPLAY_REPO"

echo "logging in to ECR"
aws ecr get-login-password --region "$AWS_REGION" | docker login --username AWS --password-stdin "$ECR_REGISTRY"

echo "building and pushing api image: $API_IMAGE"
docker build --platform linux/amd64 -f "$ROOT_DIR/services/api-go/Dockerfile" -t "$API_IMAGE" "$ROOT_DIR"
docker push "$API_IMAGE"

echo "building and pushing edge image: $EDGE_IMAGE"
docker build --platform linux/amd64 -f "$ROOT_DIR/services/edge-go/Dockerfile" -t "$EDGE_IMAGE" "$ROOT_DIR"
docker push "$EDGE_IMAGE"

echo "building and pushing gateway image: $GATEWAY_IMAGE"
docker build --platform linux/amd64 -f "$ROOT_DIR/services/gateway-rs/Dockerfile" -t "$GATEWAY_IMAGE" "$ROOT_DIR"
docker push "$GATEWAY_IMAGE"

echo "building and pushing decoder image: $DECODER_IMAGE"
docker build --platform linux/amd64 -f "$ROOT_DIR/services/decoder-rs/Dockerfile" -t "$DECODER_IMAGE" "$ROOT_DIR"
docker push "$DECODER_IMAGE"

echo "building and pushing replay image: $REPLAY_IMAGE"
docker build --platform linux/amd64 -f "$ROOT_DIR/services/replay-go/Dockerfile" -t "$REPLAY_IMAGE" "$ROOT_DIR"
docker push "$REPLAY_IMAGE"

echo "running terraform apply"
cd "$TF_DIR"
terraform init

TF_ARGS=(
  -var "aws_region=${AWS_REGION}"
  -var "project=${PROJECT}"
  -var "environment=${ENVIRONMENT}"
  -var "api_image=${API_IMAGE}"
  -var "edge_image=${EDGE_IMAGE}"
  -var "gateway_image=${GATEWAY_IMAGE}"
  -var "decoder_image=${DECODER_IMAGE}"
  -var "replay_image=${REPLAY_IMAGE}"
  -var "manage_secrets=${MANAGE_SECRETS}"
  -var "enable_msk=${ENABLE_AWS_MSK}"
  -var "msk_kafka_version=${MSK_KAFKA_VERSION}"
  -var "msk_broker_count=${MSK_BROKER_COUNT}"
  -var "msk_instance_type=${MSK_INSTANCE_TYPE}"
  -var "msk_ebs_volume_size=${MSK_EBS_VOLUME_SIZE}"
  -var "msk_client_broker=${MSK_CLIENT_BROKER}"
  -var "msk_cloudwatch_logs_enabled=${MSK_CLOUDWATCH_LOGS_ENABLED}"
  -var "cluster=${PIPELINE_CLUSTER}"
  -var "kafka_brokers=${KAFKA_BROKERS}"
  -var "raw_tx_topic=${RAW_TX_TOPIC}"
  -var "gateway_source=${GATEWAY_SOURCE}"
  -var "gateway_yellowstone_endpoint=${YELLOWSTONE_ENDPOINT}"
  -var "gateway_helius_laserstream_endpoint=${HELIUS_ENDPOINT}"
  -var "clickhouse_url=${CLICKHOUSE_URL}"
  -var "clickhouse_db=${CLICKHOUSE_DB}"
  -var "clickhouse_user=${CLICKHOUSE_USER}"
  -var "decoder_group=${DECODER_GROUP}"
)

if [[ "$TF_AUTO_APPROVE" == "true" ]]; then
  terraform apply -auto-approve "${TF_ARGS[@]}"
else
  terraform apply "${TF_ARGS[@]}"
fi

set_secret() {
  local secret_id="$1"
  local value="$2"
  if [[ -z "$value" ]]; then
    return
  fi
  aws secretsmanager put-secret-value \
    --region "$AWS_REGION" \
    --secret-id "$secret_id" \
    --secret-string "$value" >/dev/null
}

echo "updating secret values when provided"
set_secret "/${PROJECT}/${ENVIRONMENT}/api/pg_dsn" "${HELIOS_PG_DSN:-}"
set_secret "/${PROJECT}/${ENVIRONMENT}/api/api_keys" "${HELIOS_API_KEYS:-}"
set_secret "/${PROJECT}/${ENVIRONMENT}/api/openapi_token" "${HELIOS_OPENAPI_TOKEN:-}"
set_secret "/${PROJECT}/${ENVIRONMENT}/api/redis_password" "${HELIOS_RATE_LIMIT_REDIS_PASSWORD:-}"
set_secret "/${PROJECT}/${ENVIRONMENT}/edge/api_keys" "${HELIOS_EDGE_API_KEYS:-}"
set_secret "/${PROJECT}/${ENVIRONMENT}/edge/admin_token" "${HELIOS_EDGE_ADMIN_TOKEN:-}"
set_secret "/${PROJECT}/${ENVIRONMENT}/gateway/yellowstone_x_token" "${HELIOS_YELLOWSTONE_X_TOKEN:-}"
set_secret "/${PROJECT}/${ENVIRONMENT}/gateway/helius_laserstream_api_key" "${HELIUS_LASERSTREAM_API_KEY:-}"
set_secret "/${PROJECT}/${ENVIRONMENT}/decoder/clickhouse_password" "${HELIOS_CLICKHOUSE_PASSWORD:-}"

CLUSTER="${PROJECT}-${ENVIRONMENT}-cluster"
API_SERVICE="${PROJECT}-${ENVIRONMENT}-api"
EDGE_SERVICE="${PROJECT}-${ENVIRONMENT}-edge"
GATEWAY_SERVICE="${PROJECT}-${ENVIRONMENT}-gateway"
DECODER_SERVICE="${PROJECT}-${ENVIRONMENT}-decoder"

echo "forcing ECS deployments"
aws ecs update-service --region "$AWS_REGION" --cluster "$CLUSTER" --service "$API_SERVICE" --force-new-deployment >/dev/null
aws ecs update-service --region "$AWS_REGION" --cluster "$CLUSTER" --service "$EDGE_SERVICE" --force-new-deployment >/dev/null
aws ecs update-service --region "$AWS_REGION" --cluster "$CLUSTER" --service "$GATEWAY_SERVICE" --force-new-deployment >/dev/null
aws ecs update-service --region "$AWS_REGION" --cluster "$CLUSTER" --service "$DECODER_SERVICE" --force-new-deployment >/dev/null

PUBLIC_ALB_DNS="$(terraform output -raw public_alb_dns_name)"
KAFKA_BOOTSTRAP_BROKERS="$(terraform output -raw kafka_bootstrap_brokers)"

cat <<INFO
Deployment completed.
Ingress URL: http://${PUBLIC_ALB_DNS}
API image: ${API_IMAGE}
Edge image: ${EDGE_IMAGE}
Gateway image: ${GATEWAY_IMAGE}
Decoder image: ${DECODER_IMAGE}
Replay image: ${REPLAY_IMAGE}
MSK enabled: ${ENABLE_AWS_MSK}
Kafka brokers: ${KAFKA_BOOTSTRAP_BROKERS}

To enqueue replay jobs, use:
  ./infra/aws/scripts/run-replay-task.sh -start <slot> -end <slot> [-cluster mainnet-beta]
INFO
