terraform {
  required_version = ">= 1.13.0, < 2.0.0"
  backend "s3" {
    key          = "planext4u/production/platform.tfstate"
    region       = "ap-south-1"
    encrypt      = true
    use_lockfile = true
  }
  required_providers {
    aws = { source = "hashicorp/aws", version = "~> 6.62" }
    tls = { source = "hashicorp/tls", version = "~> 4.1" }
  }
}

provider "aws" {
  region = var.region
  assume_role { role_arn = var.terraform_deployment_role_arn }
  default_tags { tags = { Product = "planext4u", Environment = "production", ManagedBy = "terraform" } }
}
