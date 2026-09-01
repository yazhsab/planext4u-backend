module "environment" {
  source                   = "../../modules/environment"
  environment              = "staging"
  region                   = var.region
  vpc_cidr                 = "10.30.0.0/16"
  single_nat_gateway       = false
  certificate_arn          = var.certificate_arn
  otel_endpoint            = var.otel_endpoint
  github_repository        = "yazhsab/planext4u-backend"
  permissions_boundary_arn = var.permissions_boundary_arn
  services                 = var.services
  service_environment      = var.service_environment
  service_secret_arns      = var.service_secret_arns
  database_min_capacity    = 1
  database_max_capacity    = 16
  database_instance_count  = 2
  backup_retention_days    = 14
  deletion_protection      = true
}

output "load_balancer_dns_name" { value = module.environment.load_balancer_dns_name }
output "repository_urls" { value = module.environment.repository_urls }
output "artifact_role_arn" { value = module.environment.artifact_role_arn }
output "deploy_role_arn" { value = module.environment.deploy_role_arn }
output "migration_task_definition_arn" { value = module.environment.migration_task_definition_arn }
output "private_subnet_ids" { value = module.environment.private_subnet_ids }
output "service_security_group_id" { value = module.environment.service_security_group_id }
