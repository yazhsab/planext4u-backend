data "aws_caller_identity" "current" {}

locals {
  tags = { Product = "planext4u", Environment = var.environment, ManagedBy = "terraform" }
}

resource "aws_kms_key" "data" {
  description             = "${var.name}-${var.environment} application data"
  deletion_window_in_days = 30
  enable_key_rotation     = true
  tags                    = local.tags
}

resource "aws_db_subnet_group" "this" {
  name       = "${var.name}-${var.environment}"
  subnet_ids = var.private_subnet_ids
  tags       = local.tags
}

resource "aws_security_group" "database" {
  name_prefix = "${var.name}-${var.environment}-database-"
  description = "PostgreSQL from private application subnets"
  vpc_id      = var.vpc_id
  ingress {
    from_port   = 5432
    to_port     = 5432
    protocol    = "tcp"
    cidr_blocks = [var.vpc_cidr]
  }
  tags = local.tags
}

resource "aws_rds_cluster" "this" {
  cluster_identifier                  = "${var.name}-${var.environment}"
  engine                              = "aurora-postgresql"
  engine_version                      = var.database_engine_version
  database_name                       = var.database_name
  master_username                     = "planext4u_migration"
  manage_master_user_password         = true
  master_user_secret_kms_key_id       = aws_kms_key.data.arn
  db_subnet_group_name                = aws_db_subnet_group.this.name
  vpc_security_group_ids              = [aws_security_group.database.id]
  storage_encrypted                   = true
  kms_key_id                          = aws_kms_key.data.arn
  backup_retention_period             = var.backup_retention_days
  preferred_backup_window             = "18:30-19:30"
  preferred_maintenance_window        = "sun:20:00-sun:21:00"
  deletion_protection                 = var.deletion_protection
  skip_final_snapshot                 = false
  final_snapshot_identifier           = "${var.name}-${var.environment}-final"
  enabled_cloudwatch_logs_exports     = ["postgresql"]
  iam_database_authentication_enabled = true
  serverlessv2_scaling_configuration {
    min_capacity = var.database_min_capacity
    max_capacity = var.database_max_capacity
  }
  tags = local.tags
}

resource "aws_rds_cluster_instance" "this" {
  count                                 = var.database_instance_count
  identifier                            = "${var.name}-${var.environment}-${count.index + 1}"
  cluster_identifier                    = aws_rds_cluster.this.id
  instance_class                        = "db.serverless"
  engine                                = aws_rds_cluster.this.engine
  engine_version                        = aws_rds_cluster.this.engine_version
  db_subnet_group_name                  = aws_db_subnet_group.this.name
  publicly_accessible                   = false
  monitoring_interval                   = 60
  auto_minor_version_upgrade            = true
  performance_insights_enabled          = true
  performance_insights_kms_key_id       = aws_kms_key.data.arn
  performance_insights_retention_period = 7
  tags                                  = local.tags
}

resource "aws_security_group" "cache" {
  name_prefix = "${var.name}-${var.environment}-cache-"
  description = "Valkey from private application subnets"
  vpc_id      = var.vpc_id
  ingress {
    from_port   = 6379
    to_port     = 6379
    protocol    = "tcp"
    cidr_blocks = [var.vpc_cidr]
  }
  tags = local.tags
}

resource "aws_elasticache_serverless_cache" "this" {
  engine                   = "valkey"
  name                     = "${var.name}-${var.environment}"
  description              = "Planext4u ${var.environment} cache and rate limiting"
  kms_key_id               = aws_kms_key.data.arn
  subnet_ids               = var.private_subnet_ids
  security_group_ids       = [aws_security_group.cache.id]
  major_engine_version     = "8"
  daily_snapshot_time      = "19:30"
  snapshot_retention_limit = var.backup_retention_days
  tags                     = local.tags
}

resource "aws_s3_bucket" "media" {
  bucket_prefix = "${var.name}-${var.environment}-media-"
  force_destroy = false
  tags          = local.tags
}

resource "aws_s3_bucket_versioning" "media" {
  bucket = aws_s3_bucket.media.id
  versioning_configuration { status = "Enabled" }
}

resource "aws_s3_bucket_server_side_encryption_configuration" "media" {
  bucket = aws_s3_bucket.media.id
  rule {
    apply_server_side_encryption_by_default {
      kms_master_key_id = aws_kms_key.data.arn
      sse_algorithm     = "aws:kms"
    }
    bucket_key_enabled = true
  }
}

resource "aws_s3_bucket_public_access_block" "media" {
  bucket                  = aws_s3_bucket.media.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_lifecycle_configuration" "media" {
  bucket = aws_s3_bucket.media.id
  rule {
    id     = "retention"
    status = "Enabled"
    filter {}
    noncurrent_version_expiration { noncurrent_days = 30 }
    expiration { days = var.media_retention_days }
    abort_incomplete_multipart_upload { days_after_initiation = 7 }
  }
}

resource "aws_secretsmanager_secret" "service_database" {
  for_each                = var.service_names
  name                    = "/planext4u/${var.environment}/${each.key}/database-url"
  description             = "Rotated ${each.key} runtime database URL; value provisioned outside Terraform state"
  kms_key_id              = aws_kms_key.data.arn
  recovery_window_in_days = 30
  tags                    = merge(local.tags, { Service = each.key })
}

resource "aws_secretsmanager_secret" "migration_database" {
  name                    = "/planext4u/${var.environment}/migration/database-url"
  description             = "Privileged TLS PostgreSQL URL used only by the one-off migration task; value provisioned outside Terraform state"
  kms_key_id              = aws_kms_key.data.arn
  recovery_window_in_days = 30
  tags                    = merge(local.tags, { Service = "migration" })
}

resource "aws_secretsmanager_secret" "event_bus" {
  name                    = "/planext4u/${var.environment}/platform/event-bus-url"
  description             = "Managed NATS JetStream endpoint and credentials; value provisioned by the approved provider integration"
  kms_key_id              = aws_kms_key.data.arn
  recovery_window_in_days = 30
  tags                    = local.tags
}

output "database_endpoint" { value = aws_rds_cluster.this.endpoint }
output "database_master_secret_arn" {
  value     = aws_rds_cluster.this.master_user_secret[0].secret_arn
  sensitive = true
}
output "cache_endpoint" { value = aws_elasticache_serverless_cache.this.endpoint[0].address }
output "media_bucket" { value = aws_s3_bucket.media.id }
output "media_bucket_arn" { value = aws_s3_bucket.media.arn }
output "data_kms_key_arn" { value = aws_kms_key.data.arn }
output "service_database_secret_arns" { value = { for service, secret in aws_secretsmanager_secret.service_database : service => secret.arn } }
output "migration_database_secret_arn" { value = aws_secretsmanager_secret.migration_database.arn }
output "event_bus_secret_arn" { value = aws_secretsmanager_secret.event_bus.arn }
