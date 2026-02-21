variable "project" {
  type        = string
  description = "Project slug used for naming."
  default     = "helios"
}

variable "environment" {
  type        = string
  description = "Environment name (for example: dev, staging, prod)."
  default     = "prod"
}

variable "aws_region" {
  type        = string
  description = "AWS region for deployment."
  default     = "us-east-1"
}

variable "az_count" {
  type        = number
  description = "Number of AZs to use."
  default     = 2

  validation {
    condition     = var.az_count >= 2 && var.az_count <= 3
    error_message = "az_count must be 2 or 3."
  }
}

variable "vpc_cidr" {
  type        = string
  description = "CIDR block for VPC."
  default     = "10.40.0.0/16"
}

variable "allowed_ingress_cidrs" {
  type        = list(string)
  description = "CIDR ranges allowed to reach public ALB listeners."
  default     = ["0.0.0.0/0"]
}

variable "enable_https" {
  type        = bool
  description = "Enable HTTPS listener on the public ALB."
  default     = false
}

variable "acm_certificate_arn" {
  type        = string
  description = "ACM certificate ARN for HTTPS listener."
  default     = ""
}

variable "api_image" {
  type        = string
  description = "Container image for api-go."
  default     = "public.ecr.aws/docker/library/busybox:latest"

  validation {
    condition     = length(trimspace(var.api_image)) > 0
    error_message = "api_image must be set."
  }
}

variable "edge_image" {
  type        = string
  description = "Container image for edge-go."
  default     = "public.ecr.aws/docker/library/busybox:latest"

  validation {
    condition     = length(trimspace(var.edge_image)) > 0
    error_message = "edge_image must be set."
  }
}

variable "gateway_image" {
  type        = string
  description = "Container image for gateway-rs."
  default     = "public.ecr.aws/docker/library/busybox:latest"

  validation {
    condition     = length(trimspace(var.gateway_image)) > 0
    error_message = "gateway_image must be set."
  }
}

variable "decoder_image" {
  type        = string
  description = "Container image for decoder-rs."
  default     = "public.ecr.aws/docker/library/busybox:latest"

  validation {
    condition     = length(trimspace(var.decoder_image)) > 0
    error_message = "decoder_image must be set."
  }
}

variable "replay_image" {
  type        = string
  description = "Container image for replay-go run-task jobs."
  default     = "public.ecr.aws/docker/library/busybox:latest"

  validation {
    condition     = length(trimspace(var.replay_image)) > 0
    error_message = "replay_image must be set."
  }
}

variable "api_port" {
  type        = number
  description = "Container/listener port for api-go."
  default     = 8090
}

variable "edge_port" {
  type        = number
  description = "Container/listener port for edge-go."
  default     = 8080
}

variable "cluster" {
  type        = string
  description = "Cluster label propagated through pipeline metadata."
  default     = "mainnet-beta"
}

variable "enable_msk" {
  type        = bool
  description = "Provision AWS MSK and use it as kafka_brokers source."
  default     = false
}

variable "msk_kafka_version" {
  type        = string
  description = "MSK Kafka version when enable_msk=true."
  default     = "3.6.0"
}

variable "msk_broker_count" {
  type        = number
  description = "Number of MSK broker nodes."
  default     = 2

  validation {
    condition     = var.msk_broker_count >= 2
    error_message = "msk_broker_count must be >= 2."
  }
}

variable "msk_instance_type" {
  type        = string
  description = "MSK broker instance type."
  default     = "kafka.m5.large"
}

variable "msk_ebs_volume_size" {
  type        = number
  description = "MSK broker EBS volume size (GiB)."
  default     = 100
}

variable "msk_client_broker" {
  type        = string
  description = "MSK client_broker mode: PLAINTEXT, TLS, or TLS_PLAINTEXT."
  default     = "PLAINTEXT"

  validation {
    condition     = contains(["PLAINTEXT", "TLS", "TLS_PLAINTEXT"], var.msk_client_broker)
    error_message = "msk_client_broker must be PLAINTEXT, TLS, or TLS_PLAINTEXT."
  }
}

variable "msk_cloudwatch_logs_enabled" {
  type        = bool
  description = "Enable MSK broker logs to CloudWatch."
  default     = true
}

variable "kafka_brokers" {
  type        = string
  description = "Kafka/Redpanda bootstrap servers for gateway and decoder."
  default     = ""
}

variable "raw_tx_topic" {
  type        = string
  description = "Topic for raw transaction events."
  default     = "helios.raw_tx"
}

variable "gateway_source" {
  type        = string
  description = "Gateway source mode: yellowstone or stdin."
  default     = "yellowstone"

  validation {
    condition     = contains(["yellowstone", "stdin"], lower(var.gateway_source))
    error_message = "gateway_source must be yellowstone or stdin."
  }
}

variable "gateway_yellowstone_endpoint" {
  type        = string
  description = "Yellowstone endpoint for gateway stream ingestion."
  default     = ""
}

variable "gateway_helius_laserstream_endpoint" {
  type        = string
  description = "Helius Laserstream endpoint fallback for gateway stream ingestion."
  default     = ""
}

variable "gateway_commitment" {
  type        = string
  description = "Yellowstone commitment level."
  default     = "processed"

  validation {
    condition     = contains(["processed", "confirmed", "finalized"], lower(var.gateway_commitment))
    error_message = "gateway_commitment must be processed, confirmed, or finalized."
  }
}

variable "gateway_from_slot" {
  type        = string
  description = "Optional replay start slot for Yellowstone subscription."
  default     = ""
}

variable "gateway_signature" {
  type        = string
  description = "Optional single-signature filter for Yellowstone subscription."
  default     = ""
}

variable "gateway_account_include" {
  type        = list(string)
  description = "Optional account include filters for Yellowstone subscription."
  default     = []
}

variable "gateway_account_exclude" {
  type        = list(string)
  description = "Optional account exclude filters for Yellowstone subscription."
  default     = []
}

variable "gateway_account_required" {
  type        = list(string)
  description = "Optional account required filters for Yellowstone subscription."
  default     = []
}

variable "gateway_include_vote" {
  type        = bool
  description = "Include vote transactions in gateway stream."
  default     = false
}

variable "gateway_include_failed" {
  type        = bool
  description = "Include failed transactions in gateway stream."
  default     = true
}

variable "gateway_max_decoding_msg_size" {
  type        = number
  description = "Gateway max gRPC decode message size in bytes."
  default     = 52428800
}

variable "gateway_reconnect_backoff_ms" {
  type        = number
  description = "Gateway initial reconnect backoff in milliseconds."
  default     = 1000
}

variable "gateway_reconnect_backoff_max_ms" {
  type        = number
  description = "Gateway max reconnect backoff in milliseconds."
  default     = 15000
}

variable "clickhouse_url" {
  type        = string
  description = "ClickHouse HTTP URL for decoder sink."
  default     = "http://localhost:8123"

  validation {
    condition     = length(trimspace(var.clickhouse_url)) > 0
    error_message = "clickhouse_url must be set."
  }
}

variable "clickhouse_db" {
  type        = string
  description = "ClickHouse database for decoder sink."
  default     = "helios"
}

variable "clickhouse_user" {
  type        = string
  description = "ClickHouse user for decoder sink."
  default     = "helios"
}

variable "decoder_group" {
  type        = string
  description = "Kafka consumer group id for decoder service."
  default     = "helios-decoder"
}

variable "decoder_batch_size" {
  type        = number
  description = "Decoder max batch size before sink flush."
  default     = 2000
}

variable "decoder_batch_flush_ms" {
  type        = number
  description = "Decoder max flush interval in milliseconds."
  default     = 250
}

variable "api_cpu" {
  type        = number
  description = "CPU units for api-go task definition."
  default     = 512
}

variable "api_memory" {
  type        = number
  description = "Memory (MiB) for api-go task definition."
  default     = 1024
}

variable "edge_cpu" {
  type        = number
  description = "CPU units for edge-go task definition."
  default     = 512
}

variable "edge_memory" {
  type        = number
  description = "Memory (MiB) for edge-go task definition."
  default     = 1024
}

variable "gateway_cpu" {
  type        = number
  description = "CPU units for gateway-rs task definition."
  default     = 1024
}

variable "gateway_memory" {
  type        = number
  description = "Memory (MiB) for gateway-rs task definition."
  default     = 2048
}

variable "decoder_cpu" {
  type        = number
  description = "CPU units for decoder-rs task definition."
  default     = 1024
}

variable "decoder_memory" {
  type        = number
  description = "Memory (MiB) for decoder-rs task definition."
  default     = 2048
}

variable "replay_cpu" {
  type        = number
  description = "CPU units for replay-go run-task definition."
  default     = 512
}

variable "replay_memory" {
  type        = number
  description = "Memory (MiB) for replay-go run-task definition."
  default     = 1024
}

variable "api_desired_count" {
  type        = number
  description = "Desired replica count for api-go ECS service."
  default     = 2
}

variable "api_min_count" {
  type        = number
  description = "Minimum autoscaling count for api-go ECS service."
  default     = 2
}

variable "api_max_count" {
  type        = number
  description = "Maximum autoscaling count for api-go ECS service."
  default     = 12
}

variable "edge_desired_count" {
  type        = number
  description = "Desired replica count for edge-go ECS service."
  default     = 2
}

variable "edge_min_count" {
  type        = number
  description = "Minimum autoscaling count for edge-go ECS service."
  default     = 2
}

variable "edge_max_count" {
  type        = number
  description = "Maximum autoscaling count for edge-go ECS service."
  default     = 12
}

variable "gateway_desired_count" {
  type        = number
  description = "Desired replica count for gateway-rs ECS service."
  default     = 1
}

variable "gateway_min_count" {
  type        = number
  description = "Minimum autoscaling count for gateway-rs ECS service."
  default     = 1
}

variable "gateway_max_count" {
  type        = number
  description = "Maximum autoscaling count for gateway-rs ECS service."
  default     = 6
}

variable "decoder_desired_count" {
  type        = number
  description = "Desired replica count for decoder-rs ECS service."
  default     = 1
}

variable "decoder_min_count" {
  type        = number
  description = "Minimum autoscaling count for decoder-rs ECS service."
  default     = 1
}

variable "decoder_max_count" {
  type        = number
  description = "Maximum autoscaling count for decoder-rs ECS service."
  default     = 6
}

variable "api_target_cpu" {
  type        = number
  description = "Target average CPU utilization for api-go autoscaling."
  default     = 60
}

variable "api_target_memory" {
  type        = number
  description = "Target average memory utilization for api-go autoscaling."
  default     = 70
}

variable "edge_target_cpu" {
  type        = number
  description = "Target average CPU utilization for edge-go autoscaling."
  default     = 60
}

variable "edge_target_memory" {
  type        = number
  description = "Target average memory utilization for edge-go autoscaling."
  default     = 70
}

variable "gateway_target_cpu" {
  type        = number
  description = "Target average CPU utilization for gateway-rs autoscaling."
  default     = 70
}

variable "gateway_target_memory" {
  type        = number
  description = "Target average memory utilization for gateway-rs autoscaling."
  default     = 75
}

variable "decoder_target_cpu" {
  type        = number
  description = "Target average CPU utilization for decoder-rs autoscaling."
  default     = 70
}

variable "decoder_target_memory" {
  type        = number
  description = "Target average memory utilization for decoder-rs autoscaling."
  default     = 75
}

variable "health_check_grace_period_seconds" {
  type        = number
  description = "ECS health check grace period."
  default     = 60
}

variable "log_retention_days" {
  type        = number
  description = "CloudWatch log retention period in days."
  default     = 30
}

variable "api_health_path" {
  type        = string
  description = "Health endpoint path for api-go."
  default     = "/healthz"
}

variable "edge_health_path" {
  type        = string
  description = "Health endpoint path for edge-go."
  default     = "/healthz"
}

variable "api_cors_allow_origins" {
  type        = list(string)
  description = "Comma-joined CORS allow list passed to api-go."
  default     = []
}

variable "api_rate_limit_rps" {
  type        = number
  description = "API rate limit requests per second."
  default     = 200
}

variable "api_rate_limit_burst" {
  type        = number
  description = "API rate limit burst size."
  default     = 400
}

variable "api_rate_limit_store" {
  type        = string
  description = "Rate limit store mode for api-go: memory or redis."
  default     = "memory"

  validation {
    condition     = contains(["memory", "redis"], lower(var.api_rate_limit_store))
    error_message = "api_rate_limit_store must be memory or redis."
  }
}

variable "api_rate_limit_window" {
  type        = string
  description = "Rate limit window duration for api-go."
  default     = "1s"
}

variable "api_rate_limit_fail_open" {
  type        = bool
  description = "Whether api-go should fail-open when rate limiter backend is unavailable."
  default     = false
}

variable "api_rate_limit_redis_addr" {
  type        = string
  description = "Redis address for distributed rate limit backend."
  default     = ""
}

variable "api_rate_limit_redis_db" {
  type        = number
  description = "Redis DB index for distributed rate limiting."
  default     = 0
}

variable "api_rate_limit_redis_tls" {
  type        = bool
  description = "Enable TLS for Redis rate limiter connection."
  default     = true
}

variable "api_rate_limit_redis_prefix" {
  type        = string
  description = "Redis key prefix for rate limiting."
  default     = "helios:rl"
}

variable "api_http_request_timeout" {
  type        = string
  description = "API request timeout."
  default     = "15s"
}

variable "api_ready_timeout" {
  type        = string
  description = "API readiness DB ping timeout."
  default     = "2s"
}

variable "api_http_max_body_bytes" {
  type        = number
  description = "API max request body size in bytes."
  default     = 1048576
}

variable "api_jwt_jwks_url" {
  type        = string
  description = "Optional JWKS URL for JWT verification."
  default     = ""
}

variable "api_jwt_issuer" {
  type        = string
  description = "Optional expected JWT issuer."
  default     = ""
}

variable "api_jwt_audience" {
  type        = list(string)
  description = "Optional expected JWT audience values."
  default     = []
}

variable "api_jwt_clock_skew" {
  type        = string
  description = "Clock skew tolerance for JWT validation."
  default     = "30s"
}

variable "api_jwks_refresh_interval" {
  type        = string
  description = "JWKS refresh interval for JWT verification keys."
  default     = "5m"
}

variable "edge_balance_policy" {
  type        = string
  description = "Edge backend balancing policy: round_robin or least_conn."
  default     = "round_robin"

  validation {
    condition     = contains(["round_robin", "least_conn"], lower(var.edge_balance_policy))
    error_message = "edge_balance_policy must be round_robin or least_conn."
  }
}

variable "edge_circuit_fail_threshold" {
  type        = number
  description = "Consecutive failures before opening edge circuit breaker."
  default     = 5
}

variable "edge_circuit_open_for" {
  type        = string
  description = "Duration to keep edge circuit open."
  default     = "30s"
}

variable "edge_retry_attempts" {
  type        = number
  description = "Retry attempts for retry-safe edge requests."
  default     = 2
}

variable "edge_max_body_bytes" {
  type        = number
  description = "Max body bytes accepted by edge service."
  default     = 1048576
}

variable "manage_secrets" {
  type        = bool
  description = "Create Secrets Manager secret containers in this stack. Values are set outside Terraform."
  default     = true
}

variable "api_pg_dsn_secret_arn" {
  type        = string
  description = "Existing secret ARN that holds HELIOS_PG_DSN (if not managed by this stack)."
  default     = ""
}

variable "api_keys_secret_arn" {
  type        = string
  description = "Existing secret ARN for HELIOS_API_KEYS."
  default     = ""
}

variable "api_openapi_token_secret_arn" {
  type        = string
  description = "Existing secret ARN for HELIOS_OPENAPI_TOKEN."
  default     = ""
}

variable "api_redis_password_secret_arn" {
  type        = string
  description = "Existing secret ARN for HELIOS_RATE_LIMIT_REDIS_PASSWORD."
  default     = ""
}

variable "edge_api_keys_secret_arn" {
  type        = string
  description = "Existing secret ARN for HELIOS_EDGE_API_KEYS."
  default     = ""
}

variable "edge_admin_token_secret_arn" {
  type        = string
  description = "Existing secret ARN for HELIOS_EDGE_ADMIN_TOKEN."
  default     = ""
}

variable "gateway_yellowstone_x_token_secret_arn" {
  type        = string
  description = "Existing secret ARN for HELIOS_YELLOWSTONE_X_TOKEN."
  default     = ""
}

variable "gateway_helius_api_key_secret_arn" {
  type        = string
  description = "Existing secret ARN for HELIUS_LASERSTREAM_API_KEY."
  default     = ""
}

variable "decoder_clickhouse_password_secret_arn" {
  type        = string
  description = "Existing secret ARN for HELIOS_CLICKHOUSE_PASSWORD."
  default     = ""
}
