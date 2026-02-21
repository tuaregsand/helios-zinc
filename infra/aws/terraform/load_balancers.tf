resource "aws_lb" "public" {
  name               = "${local.name_prefix}-public"
  internal           = false
  load_balancer_type = "application"
  security_groups    = [aws_security_group.public_alb.id]
  subnets            = [for subnet in aws_subnet.public : subnet.id]

  enable_deletion_protection = false
}

resource "aws_lb" "internal_api" {
  name               = "${local.name_prefix}-api-int"
  internal           = true
  load_balancer_type = "application"
  security_groups    = [aws_security_group.internal_api_alb.id]
  subnets            = [for subnet in aws_subnet.private : subnet.id]

  enable_deletion_protection = false
}

resource "aws_lb_target_group" "edge" {
  name        = "${substr(local.name_prefix, 0, 20)}-edge-tg"
  port        = var.edge_port
  protocol    = "HTTP"
  target_type = "ip"
  vpc_id      = aws_vpc.this.id

  health_check {
    path                = var.edge_health_path
    protocol            = "HTTP"
    matcher             = "200-399"
    healthy_threshold   = 2
    unhealthy_threshold = 3
    timeout             = 5
    interval            = 15
  }
}

resource "aws_lb_target_group" "api" {
  name        = "${substr(local.name_prefix, 0, 20)}-api-tg"
  port        = var.api_port
  protocol    = "HTTP"
  target_type = "ip"
  vpc_id      = aws_vpc.this.id

  health_check {
    path                = var.api_health_path
    protocol            = "HTTP"
    matcher             = "200-399"
    healthy_threshold   = 2
    unhealthy_threshold = 3
    timeout             = 5
    interval            = 15
  }
}

resource "aws_lb_listener" "edge_http" {
  load_balancer_arn = aws_lb.public.arn
  port              = 80
  protocol          = "HTTP"

  default_action {
    type             = "forward"
    target_group_arn = aws_lb_target_group.edge.arn
  }
}

resource "aws_lb_listener" "edge_https" {
  count = var.enable_https ? 1 : 0

  load_balancer_arn = aws_lb.public.arn
  port              = 443
  protocol          = "HTTPS"
  ssl_policy        = "ELBSecurityPolicy-TLS13-1-2-2021-06"
  certificate_arn   = var.acm_certificate_arn

  default_action {
    type             = "forward"
    target_group_arn = aws_lb_target_group.edge.arn
  }

  lifecycle {
    precondition {
      condition     = var.enable_https ? trimspace(var.acm_certificate_arn) != "" : true
      error_message = "acm_certificate_arn must be set when enable_https=true"
    }
  }
}

resource "aws_lb_listener" "api_internal" {
  load_balancer_arn = aws_lb.internal_api.arn
  port              = var.api_port
  protocol          = "HTTP"

  default_action {
    type             = "forward"
    target_group_arn = aws_lb_target_group.api.arn
  }
}
