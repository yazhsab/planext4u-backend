variable "name" {
  type    = string
  default = "planext4u"
}
variable "environment" { type = string }
variable "region" { type = string }
variable "vpc_cidr" { type = string }
variable "single_nat_gateway" { type = bool }
variable "certificate_arn" { type = string }
variable "otel_endpoint" { type = string }
variable "github_repository" { type = string }
variable "permissions_boundary_arn" {
  type    = string
  default = null
}
variable "services" {
  type = map(object({
    image         = string
    port          = number
    cpu           = number
    memory        = number
    desired_count = number
    health_path   = string
  }))
}
variable "service_environment" {
  type    = map(map(string))
  default = {}
}
variable "service_secret_arns" {
  type    = map(map(string))
  default = {}
}
variable "database_min_capacity" { type = number }
variable "database_max_capacity" { type = number }
variable "database_instance_count" { type = number }
variable "backup_retention_days" { type = number }
variable "deletion_protection" { type = bool }
