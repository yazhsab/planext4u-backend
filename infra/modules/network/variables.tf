variable "name" { type = string }
variable "environment" { type = string }
variable "vpc_cidr" { type = string }
variable "az_count" {
  type    = number
  default = 3
}
variable "single_nat_gateway" {
  type    = bool
  default = false
}
variable "log_retention_days" {
  type    = number
  default = 90
}
