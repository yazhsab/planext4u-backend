variable "name" { type = string }
variable "environment" { type = string }
variable "region" { type = string }
variable "vpc_id" { type = string }
variable "vpc_cidr" { type = string }
variable "public_subnet_ids" { type = list(string) }
variable "private_subnet_ids" { type = list(string) }
variable "certificate_arn" { type = string }
variable "data_kms_key_arn" { type = string }
variable "media_bucket_arn" { type = string }
variable "event_bus_secret_arn" { type = string }
variable "database_secret_arns" { type = map(string) }
variable "migration_database_secret_arn" { type = string }
variable "service_environment" {
  type    = map(map(string))
  default = {}
}
variable "service_secret_arns" {
  type    = map(map(string))
  default = {}
}
variable "otel_endpoint" { type = string }
variable "log_retention_days" {
  type    = number
  default = 90
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
  validation {
    condition     = contains(keys(var.services), "platform") && contains(keys(var.services), "gateway")
    error_message = "services must include the platform base image and authenticated public gateway entrypoint."
  }
}
