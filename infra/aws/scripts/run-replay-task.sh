#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
TF_DIR="$ROOT_DIR/infra/aws/terraform"

AWS_REGION="${AWS_REGION:-us-east-1}"
PROJECT="${PROJECT:-helios}"
ENVIRONMENT="${ENVIRONMENT:-prod}"
CLUSTER_NAME="${CLUSTER_NAME:-}"
REPLAY_CLUSTER="mainnet-beta"
START_SLOT=""
END_SLOT=""

usage() {
  cat <<USAGE
Usage: $0 -start <slot> -end <slot> [-cluster <label>] [--dry-run]

Environment overrides:
  AWS_REGION   (default: us-east-1)
  PROJECT      (default: helios)
  ENVIRONMENT  (default: prod)
  CLUSTER_NAME (optional explicit ECS cluster name)
USAGE
}

DRY_RUN="false"
while [[ $# -gt 0 ]]; do
  case "$1" in
    -start)
      START_SLOT="$2"
      shift 2
      ;;
    -end)
      END_SLOT="$2"
      shift 2
      ;;
    -cluster)
      REPLAY_CLUSTER="$2"
      shift 2
      ;;
    --dry-run)
      DRY_RUN="true"
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "unknown argument: $1" >&2
      usage
      exit 1
      ;;
  esac
done

if [[ -z "$START_SLOT" || -z "$END_SLOT" ]]; then
  echo "-start and -end are required" >&2
  usage
  exit 1
fi

if ! [[ "$START_SLOT" =~ ^[0-9]+$ && "$END_SLOT" =~ ^[0-9]+$ ]]; then
  echo "start/end must be positive integers" >&2
  exit 1
fi

if (( END_SLOT < START_SLOT )); then
  echo "end must be >= start" >&2
  exit 1
fi

cd "$TF_DIR"

if [[ -z "$CLUSTER_NAME" ]]; then
  CLUSTER_NAME="$(terraform output -raw ecs_cluster_name)"
fi
REPLAY_TASK_DEFINITION_ARN="$(terraform output -raw replay_task_definition_arn)"
PRIVATE_SUBNET_IDS_CSV="$(terraform output -raw private_subnet_ids_csv)"
WORKER_SECURITY_GROUP_ID="$(terraform output -raw worker_security_group_id)"

IFS=',' read -r -a SUBNETS <<< "$PRIVATE_SUBNET_IDS_CSV"
if [[ ${#SUBNETS[@]} -eq 0 ]]; then
  echo "no private subnets found in terraform output" >&2
  exit 1
fi

SUBNET_ARGS=""
for subnet_id in "${SUBNETS[@]}"; do
  if [[ -n "$SUBNET_ARGS" ]]; then
    SUBNET_ARGS+=","
  fi
  SUBNET_ARGS+="\"${subnet_id}\""
done

NETWORK_CFG="awsvpcConfiguration={subnets=[${SUBNET_ARGS}],securityGroups=[\"${WORKER_SECURITY_GROUP_ID}\"],assignPublicIp=DISABLED}"
OVERRIDES="{\"containerOverrides\":[{\"name\":\"replay\",\"command\":[\"-cluster\",\"${REPLAY_CLUSTER}\",\"-start\",\"${START_SLOT}\",\"-end\",\"${END_SLOT}\"]}]}"

CMD=(
  aws ecs run-task
  --region "$AWS_REGION"
  --cluster "$CLUSTER_NAME"
  --launch-type FARGATE
  --task-definition "$REPLAY_TASK_DEFINITION_ARN"
  --network-configuration "$NETWORK_CFG"
  --overrides "$OVERRIDES"
)

if [[ "$DRY_RUN" == "true" ]]; then
  printf '%q ' "${CMD[@]}"
  printf '\n'
  exit 0
fi

"${CMD[@]}"
