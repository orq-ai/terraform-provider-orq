# An all-permissions management key. Omitting `permission_mode` defaults to
# MANAGEMENT_PERMISSION_MODE_ALL.
resource "orq_management_key" "ci" {
  name = "terraform-ci"
}

# A restricted management key: the `access` map is REQUIRED when permission_mode
# is MANAGEMENT_PERMISSION_MODE_RESTRICTED (and must be OMITTED otherwise). Keys
# are management catalog domain ids; values are ACCESS_LEVEL_NONE |
# ACCESS_LEVEL_READ | ACCESS_LEVEL_WRITE. Domain ids containing a hyphen must be
# quoted.
resource "orq_management_key" "budget_manager" {
  name            = "budget-manager"
  permission_mode = "MANAGEMENT_PERMISSION_MODE_RESTRICTED"

  access = {
    project          = "ACCESS_LEVEL_READ"
    budget           = "ACCESS_LEVEL_WRITE"
    notifier         = "ACCESS_LEVEL_WRITE"
    model            = "ACCESS_LEVEL_READ"
    "workspace-model" = "ACCESS_LEVEL_READ"
    "guardrail-rule"  = "ACCESS_LEVEL_NONE"
    "routing-rule"    = "ACCESS_LEVEL_NONE"
    policy           = "ACCESS_LEVEL_NONE"
    "api-key"         = "ACCESS_LEVEL_NONE"
    "management-key"  = "ACCESS_LEVEL_NONE"
  }

  expires_at = "2027-01-01T00:00:00Z"
}

# The raw secret is returned only once, on create, and stored in state as a
# sensitive value — use an encrypted remote backend.
output "ci_management_key_token" {
  value     = orq_management_key.ci.token
  sensitive = true
}
