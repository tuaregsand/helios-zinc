resource "aws_security_group" "public_alb" {
  name        = "${local.name_prefix}-public-alb-sg"
  description = "Public ALB security group"
  vpc_id      = aws_vpc.this.id
}

resource "aws_security_group" "internal_api_alb" {
  name        = "${local.name_prefix}-internal-api-alb-sg"
  description = "Internal API ALB security group"
  vpc_id      = aws_vpc.this.id
}

resource "aws_security_group" "edge" {
  name        = "${local.name_prefix}-edge-sg"
  description = "Edge ECS tasks security group"
  vpc_id      = aws_vpc.this.id
}

resource "aws_security_group" "api" {
  name        = "${local.name_prefix}-api-sg"
  description = "API ECS tasks security group"
  vpc_id      = aws_vpc.this.id
}

resource "aws_security_group" "worker" {
  name        = "${local.name_prefix}-worker-sg"
  description = "Gateway/decoder/replay ECS tasks security group"
  vpc_id      = aws_vpc.this.id
}

resource "aws_vpc_security_group_ingress_rule" "public_alb_http" {
  for_each = toset(var.allowed_ingress_cidrs)

  security_group_id = aws_security_group.public_alb.id
  cidr_ipv4         = each.value
  from_port         = 80
  ip_protocol       = "tcp"
  to_port           = 80
}

resource "aws_vpc_security_group_ingress_rule" "public_alb_https" {
  for_each = var.enable_https ? toset(var.allowed_ingress_cidrs) : toset([])

  security_group_id = aws_security_group.public_alb.id
  cidr_ipv4         = each.value
  from_port         = 443
  ip_protocol       = "tcp"
  to_port           = 443
}

resource "aws_vpc_security_group_egress_rule" "public_alb_all" {
  security_group_id = aws_security_group.public_alb.id
  cidr_ipv4         = "0.0.0.0/0"
  ip_protocol       = "-1"
}

resource "aws_vpc_security_group_ingress_rule" "edge_from_public_alb" {
  security_group_id            = aws_security_group.edge.id
  referenced_security_group_id = aws_security_group.public_alb.id
  from_port                    = var.edge_port
  ip_protocol                  = "tcp"
  to_port                      = var.edge_port
}

resource "aws_vpc_security_group_egress_rule" "edge_all" {
  security_group_id = aws_security_group.edge.id
  cidr_ipv4         = "0.0.0.0/0"
  ip_protocol       = "-1"
}

resource "aws_vpc_security_group_ingress_rule" "internal_api_alb_from_edge" {
  security_group_id            = aws_security_group.internal_api_alb.id
  referenced_security_group_id = aws_security_group.edge.id
  from_port                    = var.api_port
  ip_protocol                  = "tcp"
  to_port                      = var.api_port
}

resource "aws_vpc_security_group_egress_rule" "internal_api_alb_all" {
  security_group_id = aws_security_group.internal_api_alb.id
  cidr_ipv4         = "0.0.0.0/0"
  ip_protocol       = "-1"
}

resource "aws_vpc_security_group_ingress_rule" "api_from_internal_alb" {
  security_group_id            = aws_security_group.api.id
  referenced_security_group_id = aws_security_group.internal_api_alb.id
  from_port                    = var.api_port
  ip_protocol                  = "tcp"
  to_port                      = var.api_port
}

resource "aws_vpc_security_group_egress_rule" "api_all" {
  security_group_id = aws_security_group.api.id
  cidr_ipv4         = "0.0.0.0/0"
  ip_protocol       = "-1"
}

resource "aws_vpc_security_group_egress_rule" "worker_all" {
  security_group_id = aws_security_group.worker.id
  cidr_ipv4         = "0.0.0.0/0"
  ip_protocol       = "-1"
}
