variable "name" { type = string }
variable "environment" { type = string }
variable "vpc_id" { type = string }
variable "vpc_cidr" { type = string }
variable "private_subnet_ids" { type = list(string) }
variable "database_name" {
  type    = string
  default = "planext4u"
}
variable "database_engine_version" {
  type    = string
  default = "17.5"
}
variable "database_min_capacity" {
  type    = number
  default = 0.5
}
variable "database_max_capacity" {
  type    = number
  default = 8
}
variable "database_instance_count" {
  type    = number
  default = 2
}
variable "backup_retention_days" {
  type    = number
  default = 14
}
variable "deletion_protection" {
  type    = bool
  default = true
}
variable "service_names" { type = set(string) }
variable "media_retention_days" {
  type    = number
  default = 365
}
