resource "orq_project" "example" {
  name = "Production"
}

# An all-permissions key scoped to a single project. Omitting `permission_mode`
# defaults to PERMISSION_MODE_ALL.
resource "orq_api_key" "app" {
  name       = "backend-service"
  project_id = orq_project.example.id
}

# A restricted key: the `access` map is REQUIRED when permission_mode is
# PERMISSION_MODE_RESTRICTED (and must be OMITTED otherwise). Keys are api-key
# catalog domain ids; values are ACCESS_LEVEL_NONE | ACCESS_LEVEL_READ |
# ACCESS_LEVEL_WRITE.
resource "orq_api_key" "restricted" {
  name            = "read-only-agent"
  permission_mode = "PERMISSION_MODE_RESTRICTED"

  access = {
    agent            = "ACCESS_LEVEL_READ"
    chat_completions = "ACCESS_LEVEL_WRITE"
  }

  expires_at = "2027-01-01T00:00:00Z"
}

# The raw secret is returned only once, on create, and stored in state as a
# sensitive value — use an encrypted remote backend.
output "app_api_key_token" {
  value     = orq_api_key.app.token
  sensitive = true
}
