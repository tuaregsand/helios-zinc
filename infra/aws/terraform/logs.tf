resource "aws_cloudwatch_log_group" "edge" {
  name              = "/ecs/${local.name_prefix}/edge"
  retention_in_days = var.log_retention_days
}

resource "aws_cloudwatch_log_group" "api" {
  name              = "/ecs/${local.name_prefix}/api"
  retention_in_days = var.log_retention_days
}

resource "aws_cloudwatch_log_group" "gateway" {
  name              = "/ecs/${local.name_prefix}/gateway"
  retention_in_days = var.log_retention_days
}

resource "aws_cloudwatch_log_group" "decoder" {
  name              = "/ecs/${local.name_prefix}/decoder"
  retention_in_days = var.log_retention_days
}

resource "aws_cloudwatch_log_group" "replay" {
  name              = "/ecs/${local.name_prefix}/replay"
  retention_in_days = var.log_retention_days
}

resource "aws_cloudwatch_log_group" "msk" {
  count = var.enable_msk && var.msk_cloudwatch_logs_enabled ? 1 : 0

  name              = "/msk/${local.name_prefix}"
  retention_in_days = var.log_retention_days
}
