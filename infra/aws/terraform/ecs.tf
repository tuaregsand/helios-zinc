locals {
  kafka_brokers_effective = var.enable_msk ? aws_msk_cluster.this[0].bootstrap_brokers : trimspace(var.kafka_brokers)

  api_environment = [
    { name = "HELIOS_HTTP_ADDR", value = ":${var.api_port}" },
    { name = "HELIOS_HTTP_REQUEST_TIMEOUT", value = var.api_http_request_timeout },
    { name = "HELIOS_READY_TIMEOUT", value = var.api_ready_timeout },
    { name = "HELIOS_HTTP_MAX_BODY_BYTES", value = tostring(var.api_http_max_body_bytes) },
    { name = "HELIOS_RATE_LIMIT_RPS", value = tostring(var.api_rate_limit_rps) },
    { name = "HELIOS_RATE_LIMIT_BURST", value = tostring(var.api_rate_limit_burst) },
    { name = "HELIOS_RATE_LIMIT_STORE", value = lower(var.api_rate_limit_store) },
    { name = "HELIOS_RATE_LIMIT_WINDOW", value = var.api_rate_limit_window },
    { name = "HELIOS_RATE_LIMIT_FAIL_OPEN", value = tostring(var.api_rate_limit_fail_open) },
    { name = "HELIOS_RATE_LIMIT_REDIS_ADDR", value = var.api_rate_limit_redis_addr },
    { name = "HELIOS_RATE_LIMIT_REDIS_DB", value = tostring(var.api_rate_limit_redis_db) },
    { name = "HELIOS_RATE_LIMIT_REDIS_TLS", value = tostring(var.api_rate_limit_redis_tls) },
    { name = "HELIOS_RATE_LIMIT_REDIS_PREFIX", value = var.api_rate_limit_redis_prefix },
    { name = "HELIOS_CORS_ALLOW_ORIGINS", value = local.cors_allow_origins },
    { name = "HELIOS_JWT_JWKS_URL", value = var.api_jwt_jwks_url },
    { name = "HELIOS_JWT_ISSUER", value = var.api_jwt_issuer },
    { name = "HELIOS_JWT_AUDIENCE", value = local.jwt_audience },
    { name = "HELIOS_JWT_CLOCK_SKEW", value = var.api_jwt_clock_skew },
    { name = "HELIOS_JWKS_REFRESH_INTERVAL", value = var.api_jwks_refresh_interval }
  ]

  api_secrets = concat(
    [
      { name = "HELIOS_PG_DSN", valueFrom = local.api_pg_dsn_secret_arn_effective }
    ],
    local.api_keys_secret_arn_effective != "" ? [
      { name = "HELIOS_API_KEYS", valueFrom = local.api_keys_secret_arn_effective }
    ] : [],
    local.api_openapi_token_secret_arn_effective != "" ? [
      { name = "HELIOS_OPENAPI_TOKEN", valueFrom = local.api_openapi_token_secret_arn_effective }
    ] : [],
    lower(var.api_rate_limit_store) == "redis" && local.api_redis_password_secret_arn_effective != "" ? [
      { name = "HELIOS_RATE_LIMIT_REDIS_PASSWORD", valueFrom = local.api_redis_password_secret_arn_effective }
    ] : []
  )

  edge_environment = [
    { name = "HELIOS_EDGE_ADDR", value = ":${var.edge_port}" },
    { name = "HELIOS_EDGE_BACKENDS", value = "http://${aws_lb.internal_api.dns_name}:${var.api_port}" },
    { name = "HELIOS_EDGE_BALANCE_POLICY", value = lower(var.edge_balance_policy) },
    { name = "HELIOS_EDGE_HEALTH_PATH", value = var.api_health_path },
    { name = "HELIOS_EDGE_HEALTH_INTERVAL", value = "5s" },
    { name = "HELIOS_EDGE_BACKEND_TIMEOUT", value = "5s" },
    { name = "HELIOS_EDGE_READ_TIMEOUT", value = "10s" },
    { name = "HELIOS_EDGE_READ_HEADER_TIMEOUT", value = "5s" },
    { name = "HELIOS_EDGE_WRITE_TIMEOUT", value = "30s" },
    { name = "HELIOS_EDGE_IDLE_TIMEOUT", value = "120s" },
    { name = "HELIOS_EDGE_MAX_BODY_BYTES", value = tostring(var.edge_max_body_bytes) },
    { name = "HELIOS_EDGE_CIRCUIT_FAIL_THRESHOLD", value = tostring(var.edge_circuit_fail_threshold) },
    { name = "HELIOS_EDGE_CIRCUIT_OPEN_FOR", value = var.edge_circuit_open_for },
    { name = "HELIOS_EDGE_RETRY_ATTEMPTS", value = tostring(var.edge_retry_attempts) }
  ]

  edge_secrets = concat(
    local.edge_api_keys_secret_arn_effective != "" ? [
      { name = "HELIOS_EDGE_API_KEYS", valueFrom = local.edge_api_keys_secret_arn_effective }
    ] : [],
    local.edge_admin_token_secret_arn_effective != "" ? [
      { name = "HELIOS_EDGE_ADMIN_TOKEN", valueFrom = local.edge_admin_token_secret_arn_effective }
    ] : []
  )

  gateway_environment = [
    { name = "HELIOS_KAFKA_BROKERS", value = local.kafka_brokers_effective },
    { name = "HELIOS_RAW_TX_TOPIC", value = var.raw_tx_topic },
    { name = "HELIOS_GATEWAY_SOURCE", value = lower(var.gateway_source) },
    { name = "HELIOS_CLUSTER", value = var.cluster },
    { name = "HELIOS_YELLOWSTONE_ENDPOINT", value = var.gateway_yellowstone_endpoint },
    { name = "HELIUS_LASERSTREAM_ENDPOINT", value = var.gateway_helius_laserstream_endpoint },
    { name = "HELIOS_YELLOWSTONE_COMMITMENT", value = lower(var.gateway_commitment) },
    { name = "HELIOS_YELLOWSTONE_FROM_SLOT", value = var.gateway_from_slot },
    { name = "HELIOS_YELLOWSTONE_SIGNATURE", value = var.gateway_signature },
    { name = "HELIOS_YELLOWSTONE_ACCOUNT_INCLUDE", value = local.gateway_account_include_csv },
    { name = "HELIOS_YELLOWSTONE_ACCOUNT_EXCLUDE", value = local.gateway_account_exclude_csv },
    { name = "HELIOS_YELLOWSTONE_ACCOUNT_REQUIRED", value = local.gateway_account_required_csv },
    { name = "HELIOS_YELLOWSTONE_INCLUDE_VOTE", value = tostring(var.gateway_include_vote) },
    { name = "HELIOS_YELLOWSTONE_INCLUDE_FAILED", value = tostring(var.gateway_include_failed) },
    { name = "HELIOS_YELLOWSTONE_MAX_DECODING_MSG_SIZE", value = tostring(var.gateway_max_decoding_msg_size) },
    { name = "HELIOS_YELLOWSTONE_RECONNECT_BACKOFF_MS", value = tostring(var.gateway_reconnect_backoff_ms) },
    { name = "HELIOS_YELLOWSTONE_RECONNECT_BACKOFF_MAX_MS", value = tostring(var.gateway_reconnect_backoff_max_ms) }
  ]

  gateway_secrets = concat(
    local.gateway_yellowstone_x_token_secret_arn_effective != "" ? [
      { name = "HELIOS_YELLOWSTONE_X_TOKEN", valueFrom = local.gateway_yellowstone_x_token_secret_arn_effective }
    ] : [],
    local.gateway_helius_api_key_secret_arn_effective != "" ? [
      { name = "HELIUS_LASERSTREAM_API_KEY", valueFrom = local.gateway_helius_api_key_secret_arn_effective }
    ] : []
  )

  decoder_environment = [
    { name = "HELIOS_KAFKA_BROKERS", value = local.kafka_brokers_effective },
    { name = "HELIOS_RAW_TX_TOPIC", value = var.raw_tx_topic },
    { name = "HELIOS_DECODER_GROUP", value = var.decoder_group },
    { name = "HELIOS_CLICKHOUSE_URL", value = var.clickhouse_url },
    { name = "HELIOS_CLICKHOUSE_DB", value = var.clickhouse_db },
    { name = "HELIOS_CLICKHOUSE_USER", value = var.clickhouse_user },
    { name = "HELIOS_DECODER_BATCH_SIZE", value = tostring(var.decoder_batch_size) },
    { name = "HELIOS_DECODER_BATCH_FLUSH_MS", value = tostring(var.decoder_batch_flush_ms) }
  ]

  decoder_secrets = local.decoder_clickhouse_password_secret_arn_effective != "" ? [
    { name = "HELIOS_CLICKHOUSE_PASSWORD", valueFrom = local.decoder_clickhouse_password_secret_arn_effective }
  ] : []

  replay_secrets = [
    { name = "HELIOS_PG_DSN", valueFrom = local.api_pg_dsn_secret_arn_effective }
  ]
}

resource "aws_ecs_cluster" "this" {
  name = "${local.name_prefix}-cluster"

  setting {
    name  = "containerInsights"
    value = "enabled"
  }
}

resource "aws_ecs_task_definition" "api" {
  family                   = "${local.name_prefix}-api"
  requires_compatibilities = ["FARGATE"]
  network_mode             = "awsvpc"
  cpu                      = tostring(var.api_cpu)
  memory                   = tostring(var.api_memory)
  execution_role_arn       = aws_iam_role.ecs_task_execution.arn
  task_role_arn            = aws_iam_role.ecs_task.arn

  runtime_platform {
    cpu_architecture        = "X86_64"
    operating_system_family = "LINUX"
  }

  container_definitions = jsonencode([
    {
      name      = "api"
      image     = var.api_image
      essential = true
      portMappings = [
        {
          containerPort = var.api_port
          hostPort      = var.api_port
          protocol      = "tcp"
        }
      ]
      environment = local.api_environment
      secrets     = local.api_secrets
      logConfiguration = {
        logDriver = "awslogs"
        options = {
          awslogs-group         = aws_cloudwatch_log_group.api.name
          awslogs-region        = var.aws_region
          awslogs-stream-prefix = "ecs"
        }
      }
    }
  ])

  lifecycle {
    precondition {
      condition     = local.api_pg_dsn_secret_arn_effective != ""
      error_message = "api_pg_dsn_secret_arn must be set, or manage_secrets=true to create secret container."
    }

    precondition {
      condition     = lower(var.api_rate_limit_store) == "memory" || trimspace(var.api_rate_limit_redis_addr) != ""
      error_message = "api_rate_limit_redis_addr is required when api_rate_limit_store=redis."
    }
  }
}

resource "aws_ecs_task_definition" "edge" {
  family                   = "${local.name_prefix}-edge"
  requires_compatibilities = ["FARGATE"]
  network_mode             = "awsvpc"
  cpu                      = tostring(var.edge_cpu)
  memory                   = tostring(var.edge_memory)
  execution_role_arn       = aws_iam_role.ecs_task_execution.arn
  task_role_arn            = aws_iam_role.ecs_task.arn

  runtime_platform {
    cpu_architecture        = "X86_64"
    operating_system_family = "LINUX"
  }

  container_definitions = jsonencode([
    {
      name      = "edge"
      image     = var.edge_image
      essential = true
      portMappings = [
        {
          containerPort = var.edge_port
          hostPort      = var.edge_port
          protocol      = "tcp"
        }
      ]
      environment = local.edge_environment
      secrets     = local.edge_secrets
      logConfiguration = {
        logDriver = "awslogs"
        options = {
          awslogs-group         = aws_cloudwatch_log_group.edge.name
          awslogs-region        = var.aws_region
          awslogs-stream-prefix = "ecs"
        }
      }
    }
  ])
}

resource "aws_ecs_task_definition" "gateway" {
  family                   = "${local.name_prefix}-gateway"
  requires_compatibilities = ["FARGATE"]
  network_mode             = "awsvpc"
  cpu                      = tostring(var.gateway_cpu)
  memory                   = tostring(var.gateway_memory)
  execution_role_arn       = aws_iam_role.ecs_task_execution.arn
  task_role_arn            = aws_iam_role.ecs_task.arn

  runtime_platform {
    cpu_architecture        = "X86_64"
    operating_system_family = "LINUX"
  }

  container_definitions = jsonencode([
    {
      name        = "gateway"
      image       = var.gateway_image
      essential   = true
      environment = local.gateway_environment
      secrets     = local.gateway_secrets
      logConfiguration = {
        logDriver = "awslogs"
        options = {
          awslogs-group         = aws_cloudwatch_log_group.gateway.name
          awslogs-region        = var.aws_region
          awslogs-stream-prefix = "ecs"
        }
      }
    }
  ])

  lifecycle {
    precondition {
      condition = (
        lower(var.gateway_source) != "yellowstone" ||
        trimspace(var.gateway_yellowstone_endpoint) != "" ||
        trimspace(var.gateway_helius_laserstream_endpoint) != ""
      )
      error_message = "gateway_source=yellowstone requires gateway_yellowstone_endpoint or gateway_helius_laserstream_endpoint."
    }

    precondition {
      condition     = trimspace(local.kafka_brokers_effective) != ""
      error_message = "kafka_brokers must be set, or enable_msk=true."
    }
  }
}

resource "aws_ecs_task_definition" "decoder" {
  family                   = "${local.name_prefix}-decoder"
  requires_compatibilities = ["FARGATE"]
  network_mode             = "awsvpc"
  cpu                      = tostring(var.decoder_cpu)
  memory                   = tostring(var.decoder_memory)
  execution_role_arn       = aws_iam_role.ecs_task_execution.arn
  task_role_arn            = aws_iam_role.ecs_task.arn

  runtime_platform {
    cpu_architecture        = "X86_64"
    operating_system_family = "LINUX"
  }

  container_definitions = jsonencode([
    {
      name        = "decoder"
      image       = var.decoder_image
      essential   = true
      environment = local.decoder_environment
      secrets     = local.decoder_secrets
      logConfiguration = {
        logDriver = "awslogs"
        options = {
          awslogs-group         = aws_cloudwatch_log_group.decoder.name
          awslogs-region        = var.aws_region
          awslogs-stream-prefix = "ecs"
        }
      }
    }
  ])

  lifecycle {
    precondition {
      condition     = trimspace(local.kafka_brokers_effective) != ""
      error_message = "kafka_brokers must be set, or enable_msk=true."
    }
  }
}

resource "aws_ecs_task_definition" "replay" {
  family                   = "${local.name_prefix}-replay"
  requires_compatibilities = ["FARGATE"]
  network_mode             = "awsvpc"
  cpu                      = tostring(var.replay_cpu)
  memory                   = tostring(var.replay_memory)
  execution_role_arn       = aws_iam_role.ecs_task_execution.arn
  task_role_arn            = aws_iam_role.ecs_task.arn

  runtime_platform {
    cpu_architecture        = "X86_64"
    operating_system_family = "LINUX"
  }

  container_definitions = jsonencode([
    {
      name      = "replay"
      image     = var.replay_image
      essential = true
      secrets   = local.replay_secrets
      logConfiguration = {
        logDriver = "awslogs"
        options = {
          awslogs-group         = aws_cloudwatch_log_group.replay.name
          awslogs-region        = var.aws_region
          awslogs-stream-prefix = "ecs"
        }
      }
    }
  ])

  lifecycle {
    precondition {
      condition     = local.api_pg_dsn_secret_arn_effective != ""
      error_message = "api_pg_dsn_secret_arn must be set for replay task definition."
    }
  }
}

resource "aws_ecs_service" "api" {
  name                               = "${local.name_prefix}-api"
  cluster                            = aws_ecs_cluster.this.id
  task_definition                    = aws_ecs_task_definition.api.arn
  desired_count                      = var.api_desired_count
  launch_type                        = "FARGATE"
  health_check_grace_period_seconds  = var.health_check_grace_period_seconds
  deployment_minimum_healthy_percent = 50
  deployment_maximum_percent         = 200
  enable_execute_command             = true

  network_configuration {
    subnets          = [for subnet in aws_subnet.private : subnet.id]
    security_groups  = [aws_security_group.api.id]
    assign_public_ip = false
  }

  load_balancer {
    target_group_arn = aws_lb_target_group.api.arn
    container_name   = "api"
    container_port   = var.api_port
  }

  depends_on = [aws_lb_listener.api_internal]
}

resource "aws_ecs_service" "edge" {
  name                               = "${local.name_prefix}-edge"
  cluster                            = aws_ecs_cluster.this.id
  task_definition                    = aws_ecs_task_definition.edge.arn
  desired_count                      = var.edge_desired_count
  launch_type                        = "FARGATE"
  health_check_grace_period_seconds  = var.health_check_grace_period_seconds
  deployment_minimum_healthy_percent = 50
  deployment_maximum_percent         = 200
  enable_execute_command             = true

  network_configuration {
    subnets          = [for subnet in aws_subnet.private : subnet.id]
    security_groups  = [aws_security_group.edge.id]
    assign_public_ip = false
  }

  load_balancer {
    target_group_arn = aws_lb_target_group.edge.arn
    container_name   = "edge"
    container_port   = var.edge_port
  }

  depends_on = [aws_lb_listener.edge_http]
}

resource "aws_ecs_service" "gateway" {
  name                               = "${local.name_prefix}-gateway"
  cluster                            = aws_ecs_cluster.this.id
  task_definition                    = aws_ecs_task_definition.gateway.arn
  desired_count                      = var.gateway_desired_count
  launch_type                        = "FARGATE"
  health_check_grace_period_seconds  = var.health_check_grace_period_seconds
  deployment_minimum_healthy_percent = 50
  deployment_maximum_percent         = 200
  enable_execute_command             = true

  network_configuration {
    subnets          = [for subnet in aws_subnet.private : subnet.id]
    security_groups  = [aws_security_group.worker.id]
    assign_public_ip = false
  }
}

resource "aws_ecs_service" "decoder" {
  name                               = "${local.name_prefix}-decoder"
  cluster                            = aws_ecs_cluster.this.id
  task_definition                    = aws_ecs_task_definition.decoder.arn
  desired_count                      = var.decoder_desired_count
  launch_type                        = "FARGATE"
  health_check_grace_period_seconds  = var.health_check_grace_period_seconds
  deployment_minimum_healthy_percent = 50
  deployment_maximum_percent         = 200
  enable_execute_command             = true

  network_configuration {
    subnets          = [for subnet in aws_subnet.private : subnet.id]
    security_groups  = [aws_security_group.worker.id]
    assign_public_ip = false
  }
}
