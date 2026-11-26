variable "stripe_api_key" {
  type      = string
  sensitive = true
}

resource "orq_secret" "stripe" {
  vault_id    = orq_vault.staging.id
  name        = "STRIPE_API_KEY"
  kind        = "SECRET"
  value       = var.stripe_api_key
  description = "Stripe test-mode key."
}
