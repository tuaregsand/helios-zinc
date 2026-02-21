# Load Testing

## Suite

- Primary script: `load/k6/control-plane.js`
- Raw result artifacts: `load/k6/results/`
- Reproducible local fallback harness: `load/ab/run-baseline.sh`

## k6 run commands

```bash
BASE_URL=https://api.example.com \
API_KEY=replace-me \
OPENAPI_TOKEN=replace-me \
RATE=200 \
DURATION=2m \
PRE_ALLOCATED_VUS=50 \
MAX_VUS=400 \
SUMMARY_PATH=load/k6/results/latest-summary.json \
k6 run load/k6/control-plane.js
```

## Published baseline (local synthetic pass)

Date: February 20, 2026

Harness used:

- `edge-go` on `127.0.0.1:18080`
- upstream `python3 -m http.server` on `127.0.0.1:18090`
- `ab` as fallback runner (k6 binary not available in this environment)
- repeat command: `./load/ab/run-baseline.sh`

Scenarios:

- `/healthz` directly on edge
  Command: `ab -k -n 10000 -c 100 http://127.0.0.1:18080/healthz`
- proxied file request through edge
  Command: `ab -k -n 10000 -c 100 http://127.0.0.1:18080/helios_payload.txt`

Results:

| Scenario | Requests | Concurrency | Failures | RPS | Mean latency | p95 latency | p99 latency |
|---|---:|---:|---:|---:|---:|---:|---:|
| edge `/healthz` | 10,000 | 100 | 0 (0.00%) | 112,103.85/s | 0.892 ms | 2 ms | 2 ms |
| edge proxied `/helios_payload.txt` | 10,000 | 100 | 48 (0.48%) | 1,998.14/s | 50.047 ms | 32 ms | 213 ms |

Raw outputs:

- `load/k6/results/2026-02-20-ab-healthz.txt`
- `load/k6/results/2026-02-20-ab-proxy.txt`

Interpretation:

- Edge local health path has very high throughput and low latency in this synthetic setup.
- The proxied path shows upstream saturation effects under high concurrency (tail latency and non-2xx).
- For production baselines, run the k6 suite against ECS/ALB with real API replicas and datastore dependencies.
