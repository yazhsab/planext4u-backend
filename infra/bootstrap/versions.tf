terraform {
  required_version = ">= 1.13.0, < 2.0.0"
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.62"
    }
  }
}

provider "aws" {
  region = var.region
  default_tags {
    tags = {
      Product   = "planext4u"
      ManagedBy = "terraform"
      Component = "terraform-state"
    }
  }
}
