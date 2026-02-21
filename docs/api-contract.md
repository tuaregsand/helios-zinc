# API Contract

Base path: `/`

## Authentication

Protected routes (`/metrics`, `/openapi.yaml`, `/v1/*`) require one of:

- `X-API-Key: <key>` with value in `HELIOS_API_KEYS`
- `Authorization: Bearer <jwt>` validated via `HELIOS_JWT_JWKS_URL` when JWT auth is enabled

If `HELIOS_OPENAPI_TOKEN` is set, `/openapi.yaml` also requires:

- `X-OpenAPI-Token: <token>`

## Rate limiting

- Configured with `HELIOS_RATE_LIMIT_*`
- Stores:
  - `memory` for single-instance deployments
  - `redis` for shared/global limits across replicas

## Endpoints

### `GET /healthz`

- Purpose: liveness probe
- Auth: none
- Response: `200 {"status":"ok"}`

### `GET /readyz`

- Purpose: readiness probe (database dependency)
- Auth: none
- Responses:
  - `200 {"status":"ready"}`
  - `503` error envelope when DB is unavailable

### `GET /openapi.yaml`

- Purpose: OpenAPI specification
- Auth: protected (and optional docs token)

### `GET /v1/watermarks`

- Purpose: finality watermark list
- Query params:
  - `cluster` (optional)
  - `status` in `processed|confirmed|finalized` (optional)
  - `limit` in `[1, 5000]` (optional, default `500`)

### `GET /v1/replay/jobs`

- Purpose: recent replay jobs
- Query params:
  - `cluster` (optional)
  - `status` in `queued|running|failed|done` (optional)
  - `limit` in `[1, 500]` (optional, default `50`)

## Error envelope

All API errors use:

```json
{
  "error": {
    "code": "rate_limit_exceeded",
    "message": "rate limit exceeded",
    "request_id": "optional-request-id"
  }
}
```
