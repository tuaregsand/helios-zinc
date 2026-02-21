resource "aws_secretsmanager_secret" "api_pg_dsn" {
  count = var.manage_secrets ? 1 : 0

  name        = "/${var.project}/${var.environment}/api/pg_dsn"
  description = "Helios API Postgres DSN"
}

resource "aws_secretsmanager_secret" "api_keys" {
  count = var.manage_secrets ? 1 : 0

  name        = "/${var.project}/${var.environment}/api/api_keys"
  description = "Helios API key list"
}

resource "aws_secretsmanager_secret" "api_openapi_token" {
  count = var.manage_secrets ? 1 : 0

  name        = "/${var.project}/${var.environment}/api/openapi_token"
  description = "Helios OpenAPI route token"
}

resource "aws_secretsmanager_secret" "api_redis_password" {
  count = var.manage_secrets ? 1 : 0

  name        = "/${var.project}/${var.environment}/api/redis_password"
  description = "Helios API Redis password"
}

resource "aws_secretsmanager_secret" "edge_api_keys" {
  count = var.manage_secrets ? 1 : 0

  name        = "/${var.project}/${var.environment}/edge/api_keys"
  description = "Helios edge API key list"
}

resource "aws_secretsmanager_secret" "edge_admin_token" {
  count = var.manage_secrets ? 1 : 0

  name        = "/${var.project}/${var.environment}/edge/admin_token"
  description = "Helios edge admin token"
}

resource "aws_secretsmanager_secret" "gateway_yellowstone_x_token" {
  count = var.manage_secrets ? 1 : 0

  name        = "/${var.project}/${var.environment}/gateway/yellowstone_x_token"
  description = "Helios gateway Yellowstone x-token"
}

resource "aws_secretsmanager_secret" "gateway_helius_api_key" {
  count = var.manage_secrets ? 1 : 0

  name        = "/${var.project}/${var.environment}/gateway/helius_laserstream_api_key"
  description = "Helios gateway Helius Laserstream API key"
}

resource "aws_secretsmanager_secret" "decoder_clickhouse_password" {
  count = var.manage_secrets ? 1 : 0

  name        = "/${var.project}/${var.environment}/decoder/clickhouse_password"
  description = "Helios decoder ClickHouse password"
}
