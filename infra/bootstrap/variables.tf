variable "region" {
  type        = string
  description = "Region containing the central encrypted Terraform state bucket."
  default     = "ap-south-1"
}

variable "state_bucket_name" {
  type        = string
  description = "Globally unique state bucket name."
  validation {
    condition     = can(regex("^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$", var.state_bucket_name))
    error_message = "state_bucket_name must be a valid lowercase S3 bucket name."
  }
}

variable "ci_role_arns" {
  type        = list(string)
  description = "Federated CI roles allowed to read and update state objects."
}
