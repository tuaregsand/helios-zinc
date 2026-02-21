data "aws_availability_zones" "available" {
  state = "available"
}

locals {
  name_prefix = "${var.project}-${var.environment}"
  azs         = slice(data.aws_availability_zones.available.names, 0, var.az_count)

  public_subnet_cidrs = {
    for idx, az in local.azs :
    az => cidrsubnet(var.vpc_cidr, 8, idx)
  }

  private_subnet_cidrs = {
    for idx, az in local.azs :
    az => cidrsubnet(var.vpc_cidr, 8, idx + 16)
  }

  cors_allow_origins = join(",", var.api_cors_allow_origins)
  jwt_audience       = join(",", var.api_jwt_audience)

  gateway_account_include_csv  = join(",", var.gateway_account_include)
  gateway_account_exclude_csv  = join(",", var.gateway_account_exclude)
  gateway_account_required_csv = join(",", var.gateway_account_required)

  api_pg_dsn_secret_arn_effective = trimspace(var.api_pg_dsn_secret_arn) != "" ? trimspace(var.api_pg_dsn_secret_arn) : (
    var.manage_secrets ? aws_secretsmanager_secret.api_pg_dsn[0].arn : ""
  )

  api_keys_secret_arn_effective = trimspace(var.api_keys_secret_arn) != "" ? trimspace(var.api_keys_secret_arn) : (
    var.manage_secrets ? aws_secretsmanager_secret.api_keys[0].arn : ""
  )

  api_openapi_token_secret_arn_effective = trimspace(var.api_openapi_token_secret_arn) != "" ? trimspace(var.api_openapi_token_secret_arn) : (
    var.manage_secrets ? aws_secretsmanager_secret.api_openapi_token[0].arn : ""
  )

  api_redis_password_secret_arn_effective = trimspace(var.api_redis_password_secret_arn) != "" ? trimspace(var.api_redis_password_secret_arn) : (
    var.manage_secrets ? aws_secretsmanager_secret.api_redis_password[0].arn : ""
  )

  edge_api_keys_secret_arn_effective = trimspace(var.edge_api_keys_secret_arn) != "" ? trimspace(var.edge_api_keys_secret_arn) : (
    var.manage_secrets ? aws_secretsmanager_secret.edge_api_keys[0].arn : ""
  )

  edge_admin_token_secret_arn_effective = trimspace(var.edge_admin_token_secret_arn) != "" ? trimspace(var.edge_admin_token_secret_arn) : (
    var.manage_secrets ? aws_secretsmanager_secret.edge_admin_token[0].arn : ""
  )

  gateway_yellowstone_x_token_secret_arn_effective = trimspace(var.gateway_yellowstone_x_token_secret_arn) != "" ? trimspace(var.gateway_yellowstone_x_token_secret_arn) : (
    var.manage_secrets ? aws_secretsmanager_secret.gateway_yellowstone_x_token[0].arn : ""
  )

  gateway_helius_api_key_secret_arn_effective = trimspace(var.gateway_helius_api_key_secret_arn) != "" ? trimspace(var.gateway_helius_api_key_secret_arn) : (
    var.manage_secrets ? aws_secretsmanager_secret.gateway_helius_api_key[0].arn : ""
  )

  decoder_clickhouse_password_secret_arn_effective = trimspace(var.decoder_clickhouse_password_secret_arn) != "" ? trimspace(var.decoder_clickhouse_password_secret_arn) : (
    var.manage_secrets ? aws_secretsmanager_secret.decoder_clickhouse_password[0].arn : ""
  )

  secret_arns_effective = compact([
    local.api_pg_dsn_secret_arn_effective,
    local.api_keys_secret_arn_effective,
    local.api_openapi_token_secret_arn_effective,
    local.api_redis_password_secret_arn_effective,
    local.edge_api_keys_secret_arn_effective,
    local.edge_admin_token_secret_arn_effective,
    local.gateway_yellowstone_x_token_secret_arn_effective,
    local.gateway_helius_api_key_secret_arn_effective,
    local.decoder_clickhouse_password_secret_arn_effective
  ])
}
