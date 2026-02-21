#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
EDGE_BIN="/tmp/helios-edge-bench"
RESULT_DIR="$ROOT_DIR/load/k6/results"
DATE_TAG="$(date +%F)"

mkdir -p "$RESULT_DIR"

cd "$ROOT_DIR/services/edge-go"
GOCACHE=/tmp/gocache-edge-bench GOPATH=/tmp/gopath-edge-bench go build -o "$EDGE_BIN" ./cmd/edge

printf 'ok\n' >/tmp/helios_payload.txt

python3 -m http.server 18090 --directory /tmp >/tmp/helios-ab-upstream.log 2>&1 &
UP_PID=$!

HELIOS_EDGE_ADDR=127.0.0.1:18080 \
HELIOS_EDGE_BACKENDS=http://127.0.0.1:18090 \
HELIOS_EDGE_HEALTH_PATH=/helios_payload.txt \
HELIOS_EDGE_BALANCE_POLICY=round_robin \
"$EDGE_BIN" >/tmp/helios-ab-edge.log 2>&1 &
EDGE_PID=$!

cleanup() {
  kill "$EDGE_PID" "$UP_PID" >/dev/null 2>&1 || true
  wait "$EDGE_PID" >/dev/null 2>&1 || true
  wait "$UP_PID" >/dev/null 2>&1 || true
}
trap cleanup EXIT

READY=0
for _ in $(seq 1 60); do
  if curl -sf http://127.0.0.1:18080/healthz >/dev/null; then
    READY=1
    break
  fi
  sleep 0.25
done

if [ "$READY" -ne 1 ]; then
  cat /tmp/helios-ab-edge.log
  cat /tmp/helios-ab-upstream.log
  exit 1
fi

ab -k -n 10000 -c 100 http://127.0.0.1:18080/healthz >"$RESULT_DIR/${DATE_TAG}-ab-healthz.txt"
ab -k -n 10000 -c 100 http://127.0.0.1:18080/helios_payload.txt >"$RESULT_DIR/${DATE_TAG}-ab-proxy.txt"

cat "$RESULT_DIR/${DATE_TAG}-ab-healthz.txt"
cat "$RESULT_DIR/${DATE_TAG}-ab-proxy.txt"
