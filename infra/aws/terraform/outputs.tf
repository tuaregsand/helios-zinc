output "public_alb_dns_name" {
  description = "Public ALB DNS for edge ingress."
  value       = aws_lb.public.dns_name
}

output "internal_api_alb_dns_name" {
  description = "Internal API ALB DNS used as edge upstream."
  value       = aws_lb.internal_api.dns_name
}

output "ecs_cluster_name" {
  description = "ECS cluster name."
  value       = aws_ecs_cluster.this.name
}

output "api_service_name" {
  description = "API ECS service name."
  value       = aws_ecs_service.api.name
}

output "edge_service_name" {
  description = "Edge ECS service name."
  value       = aws_ecs_service.edge.name
}

output "gateway_service_name" {
  description = "Gateway ECS service name."
  value       = aws_ecs_service.gateway.name
}

output "decoder_service_name" {
  description = "Decoder ECS service name."
  value       = aws_ecs_service.decoder.name
}

output "replay_task_definition_arn" {
  description = "Replay task definition ARN for ad-hoc run-task jobs."
  value       = aws_ecs_task_definition.replay.arn
}

output "private_subnet_ids_csv" {
  description = "Private subnet IDs as CSV, used by replay run-task helper."
  value       = join(",", [for subnet in aws_subnet.private : subnet.id])
}

output "worker_security_group_id" {
  description = "Security group used by gateway/decoder/replay tasks."
  value       = aws_security_group.worker.id
}

output "kafka_bootstrap_brokers" {
  description = "Kafka bootstrap brokers used by gateway/decoder."
  value       = var.enable_msk ? aws_msk_cluster.this[0].bootstrap_brokers : var.kafka_brokers
}

output "msk_cluster_arn" {
  description = "MSK cluster ARN when enable_msk=true."
  value       = var.enable_msk ? aws_msk_cluster.this[0].arn : ""
}

output "managed_secret_arns" {
  description = "Secrets created by this stack when manage_secrets=true."
  value = var.manage_secrets ? {
    api_pg_dsn                  = aws_secretsmanager_secret.api_pg_dsn[0].arn
    api_keys                    = aws_secretsmanager_secret.api_keys[0].arn
    api_openapi                 = aws_secretsmanager_secret.api_openapi_token[0].arn
    api_redis_pass              = aws_secretsmanager_secret.api_redis_password[0].arn
    edge_api_keys               = aws_secretsmanager_secret.edge_api_keys[0].arn
    edge_admin_token            = aws_secretsmanager_secret.edge_admin_token[0].arn
    gateway_yellowstone_x_token = aws_secretsmanager_secret.gateway_yellowstone_x_token[0].arn
    gateway_helius_api_key      = aws_secretsmanager_secret.gateway_helius_api_key[0].arn
    decoder_clickhouse_password = aws_secretsmanager_secret.decoder_clickhouse_password[0].arn
  } : {}
}
