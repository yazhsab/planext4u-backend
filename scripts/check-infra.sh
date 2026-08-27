#!/usr/bin/env bash
set -euo pipefail

terraform_binary="${TERRAFORM_BINARY:-terraform}"
"$terraform_binary" fmt -check -recursive infra

for infra_directory in infra/bootstrap infra/organization infra/environments/development infra/environments/staging; do
  "$terraform_binary" -chdir="$infra_directory" init -backend=false -input=false
  "$terraform_binary" -chdir="$infra_directory" validate
done

go test ./internal/infra
