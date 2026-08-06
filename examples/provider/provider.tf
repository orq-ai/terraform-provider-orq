terraform {
  required_providers {
    orq = {
      source = "orq-ai/orq"
    }
  }
}

# Zero-config is the recommended form: the provider reads its credentials from the
# environment, so nothing sensitive lives in your Terraform configuration.
#
#   export ORQ_API_KEY="sk-orq-..."     # a management key; the workspace is implied by the credential
#   export ORQ_API_BASE_URL="https://my.orq.ai" # optional; override only for on-prem or staging installs
#
provider "orq" {
  # Both attributes are optional and exist only as an escape hatch. Prefer the
  # ORQ_API_BASE_URL / ORQ_API_KEY environment variables — an explicit value here beats the
  # env var but hard-codes it into the config (and, for `api_key`, risks committing
  # a secret). Precedence is: explicit attribute > env var > built-in default.

  # url = "https://my.orq.ai" # defaults to ORQ_API_BASE_URL, then https://my.orq.ai

  # api_key = "sk-orq-..." # defaults to ORQ_API_KEY; do NOT commit real secrets
}
