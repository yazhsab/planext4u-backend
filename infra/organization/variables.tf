variable "region" {
  type    = string
  default = "ap-south-1"
}
variable "organizational_unit_id" { type = string }
variable "environment_accounts" {
  type = map(object({
    name  = string
    email = string
  }))
  validation {
    condition     = alltrue([for key in keys(var.environment_accounts) : contains(["development", "staging"], key)])
    error_message = "Only development and staging accounts belong in the Phase 2 account request."
  }
}
