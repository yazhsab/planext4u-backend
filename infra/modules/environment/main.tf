module "network" {
  source             = "../network"
  name               = var.name
  environment        = var.environment
  vpc_cidr           = var.vpc_cidr
  az_count           = 3
  single_nat_gateway = var.single_nat_gateway
}

module "data" {
  source                  = "../data"
  name                    = var.name
  environment             = var.environment
  vpc_id                  = module.network.vpc_id
  vpc_cidr                = module.network.vpc_cidr
  private_subnet_ids      = module.network.private_subnet_ids
  service_names           = toset(keys(var.services))
  database_min_capacity   = var.database_min_capacity
  database_max_capacity   = var.database_max_capacity
  database_instance_count = var.database_instance_count
  backup_retention_days   = var.backup_retention_days
  deletion_protection     = var.deletion_protection
}

module "artifacts" {
  source        = "../artifacts"
  name          = var.name
  environment   = var.environment
  service_names = toset(keys(var.services))
}

module "compute" {
  source                        = "../compute"
  name                          = var.name
  environment                   = var.environment
  region                        = var.region
  vpc_id                        = module.network.vpc_id
  vpc_cidr                      = module.network.vpc_cidr
  public_subnet_ids             = module.network.public_subnet_ids
  private_subnet_ids            = module.network.private_subnet_ids
  certificate_arn               = var.certificate_arn
  data_kms_key_arn              = module.data.data_kms_key_arn
  media_bucket_arn              = module.data.media_bucket_arn
  event_bus_secret_arn          = module.data.event_bus_secret_arn
  database_secret_arns          = module.data.service_database_secret_arns
  migration_database_secret_arn = module.data.migration_database_secret_arn
  otel_endpoint                 = var.otel_endpoint
  services                      = var.services
  service_environment = merge(var.service_environment, contains(keys(var.services), "media") ? {
    media = merge(lookup(var.service_environment, "media", {}), {
      AWS_REGION   = var.region
      MEDIA_BUCKET = module.data.media_bucket
    })
  } : {})
  service_secret_arns = var.service_secret_arns
}

module "delivery_identity" {
  source                        = "../delivery_identity"
  name                          = var.name
  environment                   = var.environment
  github_repository             = var.github_repository
  ecr_repository_arns           = module.artifacts.repository_arns
  ecs_cluster_arn               = "arn:aws:ecs:${var.region}:${data.aws_caller_identity.current.account_id}:cluster/${module.compute.cluster_name}"
  ecs_service_arns              = [for service in module.compute.service_names : "arn:aws:ecs:${var.region}:${data.aws_caller_identity.current.account_id}:service/${module.compute.cluster_name}/${service}"]
  migration_task_definition_arn = module.compute.migration_task_definition_arn
  task_role_arns                = concat([for service in keys(var.services) : "arn:aws:iam::${data.aws_caller_identity.current.account_id}:role/${var.name}-${var.environment}-${service}-task"], [module.compute.migration_task_role_arn, "arn:aws:iam::${data.aws_caller_identity.current.account_id}:role/${var.name}-${var.environment}-ecs-execution"])
  permissions_boundary_arn      = var.permissions_boundary_arn
}

data "aws_caller_identity" "current" {}

output "load_balancer_dns_name" { value = module.compute.load_balancer_dns_name }
output "repository_urls" { value = module.artifacts.repository_urls }
output "artifact_role_arn" { value = module.delivery_identity.artifact_role_arn }
output "deploy_role_arn" { value = module.delivery_identity.deploy_role_arn }
output "migration_task_definition_arn" { value = module.compute.migration_task_definition_arn }
output "private_subnet_ids" { value = module.compute.private_subnet_ids }
output "service_security_group_id" { value = module.compute.service_security_group_id }
