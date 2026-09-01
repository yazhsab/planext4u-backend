variable "region" {
  type    = string
  default = "ap-south-1"
}
variable "terraform_deployment_role_arn" { type = string }
variable "permissions_boundary_arn" {
  type    = string
  default = null
}
variable "certificate_arn" { type = string }
variable "otel_endpoint" { type = string }
variable "services" {
  type = map(object({ image = string, port = number, cpu = number, memory = number, desired_count = number, health_path = string }))
}
variable "service_environment" {
  type    = map(map(string))
  default = {}
}
variable "service_secret_arns" {
  type    = map(map(string))
  default = {}
}
