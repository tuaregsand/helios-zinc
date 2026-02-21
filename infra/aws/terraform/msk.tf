locals {
  msk_client_ports = var.msk_client_broker == "PLAINTEXT" ? toset(["9092"]) : (
    var.msk_client_broker == "TLS" ? toset(["9094"]) : toset(["9092", "9094"])
  )
}

resource "aws_security_group" "msk" {
  count = var.enable_msk ? 1 : 0

  name        = "${local.name_prefix}-msk-sg"
  description = "MSK cluster security group"
  vpc_id      = aws_vpc.this.id
}

resource "aws_vpc_security_group_ingress_rule" "msk_from_worker" {
  for_each = var.enable_msk ? local.msk_client_ports : toset([])

  security_group_id            = aws_security_group.msk[0].id
  referenced_security_group_id = aws_security_group.worker.id
  from_port                    = tonumber(each.value)
  ip_protocol                  = "tcp"
  to_port                      = tonumber(each.value)
}

resource "aws_vpc_security_group_egress_rule" "msk_all" {
  count = var.enable_msk ? 1 : 0

  security_group_id = aws_security_group.msk[0].id
  cidr_ipv4         = "0.0.0.0/0"
  ip_protocol       = "-1"
}

resource "aws_msk_cluster" "this" {
  count = var.enable_msk ? 1 : 0

  cluster_name           = "${local.name_prefix}-msk"
  kafka_version          = var.msk_kafka_version
  number_of_broker_nodes = var.msk_broker_count

  broker_node_group_info {
    instance_type   = var.msk_instance_type
    client_subnets  = [for subnet in aws_subnet.private : subnet.id]
    security_groups = [aws_security_group.msk[0].id]

    storage_info {
      ebs_storage_info {
        volume_size = var.msk_ebs_volume_size
      }
    }
  }

  client_authentication {
    unauthenticated = true
  }

  encryption_info {
    encryption_in_transit {
      client_broker = var.msk_client_broker
      in_cluster    = true
    }
  }

  dynamic "logging_info" {
    for_each = var.msk_cloudwatch_logs_enabled ? [1] : []

    content {
      broker_logs {
        cloudwatch_logs {
          enabled   = true
          log_group = aws_cloudwatch_log_group.msk[0].name
        }
      }
    }
  }

  lifecycle {
    precondition {
      condition     = var.msk_broker_count >= length(local.azs) && var.msk_broker_count % length(local.azs) == 0
      error_message = "msk_broker_count must be a multiple of az_count and >= az_count."
    }
  }
}
