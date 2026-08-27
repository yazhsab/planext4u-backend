variable "name" { type = string }
variable "environment" { type = string }
variable "github_repository" { type = string }
variable "ecr_repository_arns" { type = list(string) }
variable "ecs_cluster_arn" { type = string }
variable "ecs_service_arns" { type = list(string) }
variable "task_role_arns" { type = list(string) }
variable "permissions_boundary_arn" {
  type    = string
  default = null
}
