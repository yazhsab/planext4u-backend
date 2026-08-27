data "aws_caller_identity" "current" {}

locals {
  tags        = { Product = "planext4u", Environment = var.environment, ManagedBy = "terraform" }
  service_ids = { for index, service in sort(keys(var.services)) : service => index + 1 }
}

resource "aws_kms_key" "logs" {
  description             = "${var.name}-${var.environment} application logs"
  deletion_window_in_days = 30
  enable_key_rotation     = true
  tags                    = local.tags
}

resource "aws_cloudwatch_log_group" "service" {
  for_each          = var.services
  name              = "/planext4u/${var.environment}/${each.key}"
  retention_in_days = var.log_retention_days
  kms_key_id        = aws_kms_key.logs.arn
  tags              = merge(local.tags, { Service = each.key })
}

resource "aws_ecs_cluster" "this" {
  name = "${var.name}-${var.environment}"
  setting {
    name  = "containerInsights"
    value = "enhanced"
  }
  configuration {
    execute_command_configuration {
      kms_key_id = aws_kms_key.logs.arn
      logging    = "OVERRIDE"
      log_configuration {
        cloud_watch_log_group_name     = aws_cloudwatch_log_group.service["platform"].name
        cloud_watch_encryption_enabled = true
      }
    }
  }
  tags = local.tags
}

resource "aws_service_discovery_private_dns_namespace" "this" {
  name        = "${var.environment}.planext4u.internal"
  description = "Private service discovery"
  vpc         = var.vpc_id
  tags        = local.tags
}

data "aws_iam_policy_document" "ecs_assume" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["ecs-tasks.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "execution" {
  name               = "${var.name}-${var.environment}-ecs-execution"
  assume_role_policy = data.aws_iam_policy_document.ecs_assume.json
  tags               = local.tags
}

resource "aws_iam_role_policy_attachment" "execution" {
  role       = aws_iam_role.execution.name
  policy_arn = "arn:aws:iam::aws:policy/service-role/AmazonECSTaskExecutionRolePolicy"
}

resource "aws_iam_role_policy" "execution_secrets" {
  role = aws_iam_role.execution.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      { Effect = "Allow", Action = ["secretsmanager:GetSecretValue"], Resource = concat(values(var.database_secret_arns), [var.event_bus_secret_arn], var.synthetic_slice_signing_key_secret_arn == null ? [] : [var.synthetic_slice_signing_key_secret_arn]) },
      { Effect = "Allow", Action = ["kms:Decrypt"], Resource = [var.data_kms_key_arn] }
    ]
  })
}

resource "aws_iam_role" "task" {
  for_each           = var.services
  name               = "${var.name}-${var.environment}-${each.key}-task"
  assume_role_policy = data.aws_iam_policy_document.ecs_assume.json
  tags               = merge(local.tags, { Service = each.key })
}

resource "aws_iam_role_policy" "task" {
  for_each = var.services
  role     = aws_iam_role.task[each.key].id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = concat(
      [{ Effect = "Allow", Action = ["xray:PutTraceSegments", "xray:PutTelemetryRecords"], Resource = "*" }],
      each.key == "media" ? [{ Effect = "Allow", Action = ["s3:GetObject", "s3:PutObject", "s3:AbortMultipartUpload"], Resource = "${var.media_bucket_arn}/*" }] : []
    )
  })
}

resource "aws_security_group" "load_balancer" {
  name_prefix = "${var.name}-${var.environment}-alb-"
  description = "Public TLS ingress"
  vpc_id      = var.vpc_id
  ingress {
    from_port        = 443
    to_port          = 443
    protocol         = "tcp"
    cidr_blocks      = ["0.0.0.0/0"]
    ipv6_cidr_blocks = ["::/0"]
  }
  egress {
    description = "Only private application target ports"
    from_port   = 8080
    to_port     = 8099
    protocol    = "tcp"
    cidr_blocks = [var.vpc_cidr]
  }
  tags = local.tags
}

resource "aws_security_group" "service" {
  name_prefix = "${var.name}-${var.environment}-service-"
  description = "Application ports from the load balancer and private service mesh"
  vpc_id      = var.vpc_id
  ingress {
    from_port       = 8080
    to_port         = 8099
    protocol        = "tcp"
    security_groups = [aws_security_group.load_balancer.id]
  }
  ingress {
    from_port   = 8080
    to_port     = 8099
    protocol    = "tcp"
    cidr_blocks = [var.vpc_cidr]
  }
  egress {
    description = "Private dependencies and VPC endpoints only; public providers require a reviewed egress proxy"
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = [var.vpc_cidr]
  }
  tags = local.tags
}

resource "aws_lb" "this" {
  name = "${var.name}-${var.environment}"
  #trivy:ignore:AWS-0053 -- Intentional public TLS application edge; ingress is 443-only and the WAF is attached below.
  internal                   = false
  load_balancer_type         = "application"
  security_groups            = [aws_security_group.load_balancer.id]
  subnets                    = var.public_subnet_ids
  enable_deletion_protection = var.environment != "development"
  drop_invalid_header_fields = true
  tags                       = local.tags
}

resource "aws_lb_target_group" "service" {
  for_each             = var.services
  name_prefix          = substr(each.key, 0, 6)
  port                 = each.value.port
  protocol             = "HTTP"
  target_type          = "ip"
  vpc_id               = var.vpc_id
  deregistration_delay = 30
  health_check {
    enabled             = true
    path                = each.value.health_path
    healthy_threshold   = 2
    unhealthy_threshold = 3
    interval            = 15
    timeout             = 5
    matcher             = "200-299"
  }
  tags = merge(local.tags, { Service = each.key })
}

resource "aws_lb_listener" "https" {
  load_balancer_arn = aws_lb.this.arn
  port              = 443
  protocol          = "HTTPS"
  ssl_policy        = "ELBSecurityPolicy-TLS13-1-2-2021-06"
  certificate_arn   = var.certificate_arn
  default_action {
    type             = "forward"
    target_group_arn = aws_lb_target_group.service["platform"].arn
  }
}

resource "aws_lb_listener_rule" "service" {
  for_each     = { for service, config in var.services : service => config if service != "platform" }
  listener_arn = aws_lb_listener.https.arn
  priority     = 100 + local.service_ids[each.key]
  action {
    type             = "forward"
    target_group_arn = aws_lb_target_group.service[each.key].arn
  }
  condition {
    path_pattern {
      values = ["/${each.key}/*"]
    }
  }
}

resource "aws_ecs_task_definition" "service" {
  for_each                 = var.services
  family                   = "${var.name}-${var.environment}-${each.key}"
  requires_compatibilities = ["FARGATE"]
  network_mode             = "awsvpc"
  cpu                      = each.value.cpu
  memory                   = each.value.memory
  execution_role_arn       = aws_iam_role.execution.arn
  task_role_arn            = aws_iam_role.task[each.key].arn
  runtime_platform {
    operating_system_family = "LINUX"
    cpu_architecture        = "X86_64"
  }
  container_definitions = jsonencode([{
    name                   = each.key
    image                  = each.value.image
    essential              = true
    readonlyRootFilesystem = true
    portMappings           = [{ containerPort = each.value.port, hostPort = each.value.port, protocol = "tcp", name = "http" }]
    environment = concat([
      { name = "APP_ENV", value = var.environment },
      { name = "SERVICE_NAME", value = "planext4u-${each.key}" },
      { name = "HTTP_ADDRESS", value = ":${each.value.port}" },
      { name = "OTEL_EXPORTER_OTLP_ENDPOINT", value = var.otel_endpoint },
      { name = "OTEL_EXPORTER_OTLP_PROTOCOL", value = "http/protobuf" }
      ], each.key == "platform" && var.environment == "staging" ? [
      { name = "SYNTHETIC_SLICE_ENABLED", value = "true" }
    ] : [])
    secrets = concat([
      { name = "DATABASE_URL", valueFrom = var.database_secret_arns[each.key] },
      { name = "EVENT_BUS_URL", valueFrom = var.event_bus_secret_arn }
      ], each.key == "platform" && var.environment == "staging" ? [
      { name = "SYNTHETIC_SLICE_SIGNING_KEY", valueFrom = var.synthetic_slice_signing_key_secret_arn }
    ] : [])
    logConfiguration = { logDriver = "awslogs", options = { awslogs-group = aws_cloudwatch_log_group.service[each.key].name, awslogs-region = var.region, awslogs-stream-prefix = "service", mode = "non-blocking", max-buffer-size = "25m" } }
  }])
  tags = merge(local.tags, { Service = each.key })
}

resource "aws_ecs_service" "service" {
  for_each                           = var.services
  name                               = each.key
  cluster                            = aws_ecs_cluster.this.id
  task_definition                    = aws_ecs_task_definition.service[each.key].arn
  desired_count                      = each.value.desired_count
  launch_type                        = "FARGATE"
  health_check_grace_period_seconds  = 60
  enable_execute_command             = false
  deployment_minimum_healthy_percent = 100
  deployment_maximum_percent         = 200
  deployment_circuit_breaker {
    enable   = true
    rollback = true
  }
  network_configuration {
    subnets          = var.private_subnet_ids
    security_groups  = [aws_security_group.service.id]
    assign_public_ip = false
  }
  load_balancer {
    target_group_arn = aws_lb_target_group.service[each.key].arn
    container_name   = each.key
    container_port   = each.value.port
  }
  service_connect_configuration {
    enabled   = true
    namespace = aws_service_discovery_private_dns_namespace.this.arn
    service {
      port_name      = "http"
      discovery_name = each.key
      client_alias {
        port     = each.value.port
        dns_name = each.key
      }
    }
  }
  lifecycle { ignore_changes = [task_definition, desired_count] }
  depends_on = [aws_lb_listener.https]
  tags       = merge(local.tags, { Service = each.key })
}

resource "aws_appautoscaling_target" "service" {
  for_each           = var.services
  max_capacity       = max(each.value.desired_count * 5, 5)
  min_capacity       = each.value.desired_count
  resource_id        = "service/${aws_ecs_cluster.this.name}/${aws_ecs_service.service[each.key].name}"
  scalable_dimension = "ecs:service:DesiredCount"
  service_namespace  = "ecs"
}

resource "aws_appautoscaling_policy" "cpu" {
  for_each           = var.services
  name               = "${each.key}-cpu"
  policy_type        = "TargetTrackingScaling"
  resource_id        = aws_appautoscaling_target.service[each.key].resource_id
  scalable_dimension = aws_appautoscaling_target.service[each.key].scalable_dimension
  service_namespace  = aws_appautoscaling_target.service[each.key].service_namespace
  target_tracking_scaling_policy_configuration {
    target_value       = 60
    scale_in_cooldown  = 300
    scale_out_cooldown = 60
    predefined_metric_specification {
      predefined_metric_type = "ECSServiceAverageCPUUtilization"
    }
  }
}

resource "aws_wafv2_web_acl" "this" {
  name  = "${var.name}-${var.environment}"
  scope = "REGIONAL"
  default_action {
    allow {}
  }
  rule {
    name     = "AWSManagedCommon"
    priority = 10
    override_action {
      none {}
    }
    statement {
      managed_rule_group_statement {
        name        = "AWSManagedRulesCommonRuleSet"
        vendor_name = "AWS"
      }
    }
    visibility_config {
      cloudwatch_metrics_enabled = true
      metric_name                = "managed-common"
      sampled_requests_enabled   = false
    }
  }
  rule {
    name     = "GlobalRateLimit"
    priority = 20
    action {
      block {}
    }
    statement {
      rate_based_statement {
        aggregate_key_type    = "IP"
        limit                 = 2000
        evaluation_window_sec = 300
      }
    }
    visibility_config {
      cloudwatch_metrics_enabled = true
      metric_name                = "global-rate-limit"
      sampled_requests_enabled   = false
    }
  }
  visibility_config {
    cloudwatch_metrics_enabled = true
    metric_name                = "${var.name}-${var.environment}"
    sampled_requests_enabled   = false
  }
  tags = local.tags
}

resource "aws_wafv2_web_acl_association" "this" {
  resource_arn = aws_lb.this.arn
  web_acl_arn  = aws_wafv2_web_acl.this.arn
}

output "cluster_name" { value = aws_ecs_cluster.this.name }
output "load_balancer_dns_name" { value = aws_lb.this.dns_name }
output "service_names" { value = keys(aws_ecs_service.service) }
