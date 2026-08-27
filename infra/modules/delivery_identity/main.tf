data "tls_certificate" "github" { url = "https://token.actions.githubusercontent.com" }

resource "aws_iam_openid_connect_provider" "github" {
  url             = "https://token.actions.githubusercontent.com"
  client_id_list  = ["sts.amazonaws.com"]
  thumbprint_list = [data.tls_certificate.github.certificates[0].sha1_fingerprint]
  tags            = { Product = "planext4u", Environment = var.environment, ManagedBy = "terraform" }
}

data "aws_iam_policy_document" "artifact_assume" {
  statement {
    actions = ["sts:AssumeRoleWithWebIdentity"]
    principals {
      type        = "Federated"
      identifiers = [aws_iam_openid_connect_provider.github.arn]
    }
    condition {
      test     = "StringEquals"
      variable = "token.actions.githubusercontent.com:aud"
      values   = ["sts.amazonaws.com"]
    }
    condition {
      test     = "StringEquals"
      variable = "token.actions.githubusercontent.com:sub"
      values   = ["repo:${var.github_repository}:ref:refs/heads/main", "repo:${var.github_repository}:environment:${var.environment}"]
    }
  }
}

resource "aws_iam_role" "artifact" {
  name                 = "${var.name}-${var.environment}-github-artifact"
  assume_role_policy   = data.aws_iam_policy_document.artifact_assume.json
  permissions_boundary = var.permissions_boundary_arn
  max_session_duration = 3600
  tags                 = { Product = "planext4u", Environment = var.environment, ManagedBy = "terraform" }
}

resource "aws_iam_role_policy" "artifact" {
  role = aws_iam_role.artifact.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      { Effect = "Allow", Action = ["ecr:GetAuthorizationToken"], Resource = "*" },
      { Effect = "Allow", Action = ["ecr:BatchCheckLayerAvailability", "ecr:CompleteLayerUpload", "ecr:GetDownloadUrlForLayer", "ecr:InitiateLayerUpload", "ecr:PutImage", "ecr:UploadLayerPart"], Resource = var.ecr_repository_arns }
    ]
  })
}

data "aws_iam_policy_document" "deploy_assume" {
  statement {
    actions = ["sts:AssumeRoleWithWebIdentity"]
    principals {
      type        = "Federated"
      identifiers = [aws_iam_openid_connect_provider.github.arn]
    }
    condition {
      test     = "StringEquals"
      variable = "token.actions.githubusercontent.com:aud"
      values   = ["sts.amazonaws.com"]
    }
    condition {
      test     = "StringEquals"
      variable = "token.actions.githubusercontent.com:sub"
      values   = ["repo:${var.github_repository}:environment:${var.environment}"]
    }
  }
}

resource "aws_iam_role" "deploy" {
  name                 = "${var.name}-${var.environment}-github-deploy"
  assume_role_policy   = data.aws_iam_policy_document.deploy_assume.json
  permissions_boundary = var.permissions_boundary_arn
  max_session_duration = 3600
  tags                 = { Product = "planext4u", Environment = var.environment, ManagedBy = "terraform" }
}

resource "aws_iam_role_policy" "deploy" {
  role = aws_iam_role.deploy.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      { Effect = "Allow", Action = ["ecs:DescribeTaskDefinition", "ecs:RegisterTaskDefinition"], Resource = "*" },
      { Effect = "Allow", Action = ["ecs:DescribeServices", "ecs:UpdateService"], Resource = var.ecs_service_arns, Condition = { ArnEquals = { "ecs:cluster" = var.ecs_cluster_arn } } },
      { Effect = "Allow", Action = ["iam:PassRole"], Resource = var.task_role_arns, Condition = { StringEquals = { "iam:PassedToService" = "ecs-tasks.amazonaws.com" } } }
    ]
  })
}

output "artifact_role_arn" { value = aws_iam_role.artifact.arn }
output "deploy_role_arn" { value = aws_iam_role.deploy.arn }
