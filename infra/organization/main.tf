resource "aws_organizations_account" "environment" {
  for_each                   = var.environment_accounts
  name                       = each.value.name
  email                      = each.value.email
  parent_id                  = var.organizational_unit_id
  role_name                  = "Planext4uOrganizationAccessRole"
  iam_user_access_to_billing = "DENY"
  close_on_deletion          = false
  tags = {
    Product     = "planext4u"
    Environment = each.key
    ManagedBy   = "terraform"
  }
  lifecycle { prevent_destroy = true }
}

output "account_ids" {
  value = { for environment, account in aws_organizations_account.environment : environment => account.id }
}
