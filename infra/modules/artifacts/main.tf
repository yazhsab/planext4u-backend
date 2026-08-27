locals { tags = { Product = "planext4u", Environment = var.environment, ManagedBy = "terraform" } }

resource "aws_kms_key" "ecr" {
  description             = "${var.name}-${var.environment} container artifacts"
  deletion_window_in_days = 30
  enable_key_rotation     = true
  tags                    = local.tags
}

resource "aws_ecr_repository" "service" {
  for_each             = var.service_names
  name                 = "${var.name}/${each.key}"
  image_tag_mutability = "IMMUTABLE"
  force_delete         = false
  encryption_configuration {
    encryption_type = "KMS"
    kms_key         = aws_kms_key.ecr.arn
  }
  image_scanning_configuration { scan_on_push = true }
  tags = merge(local.tags, { Service = each.key })
}

resource "aws_ecr_lifecycle_policy" "service" {
  for_each   = aws_ecr_repository.service
  repository = each.value.name
  policy     = jsonencode({ rules = [{ rulePriority = 1, description = "Retain the latest 100 release artifacts", selection = { tagStatus = "any", countType = "imageCountMoreThan", countNumber = 100 }, action = { type = "expire" } }] })
}

output "repository_urls" { value = { for service, repository in aws_ecr_repository.service : service => repository.repository_url } }
output "repository_arns" { value = [for repository in aws_ecr_repository.service : repository.arn] }
