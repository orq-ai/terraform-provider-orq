variable "otlp_token" {
  type      = string
  sensitive = true
}

resource "orq_telemetry_destination" "collector" {
  display_name  = "Collector"
  endpoint_url  = "https://otel.example.com/v1/traces"
  content_mode  = "METADATA_ONLY"
  project_scope = "ALL"

  request_headers = {
    authorization = "Bearer ${var.otlp_token}"
  }
}

# Export full content for selected projects only:
#
# resource "orq_telemetry_destination" "selected" {
#   endpoint_url  = "https://otel.example.com/v1/traces"
#   content_mode  = "FULL_CONTENT"
#   project_scope = "SELECTED"
#   project_ids   = [orq_project.example.id]
# }
