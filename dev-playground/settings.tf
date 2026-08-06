resource "orq_workspace_settings" "this" {
  display_name           = "orq-test-changed"
  enforce_enabled_models = true
  pii_redaction = {
    enabled = false
  }
}
