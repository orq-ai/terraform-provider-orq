# Enabling a model in the workspace catalog and sharing it with specific projects.
resource "orq_project" "example" {
  name = "Production"
}

resource "orq_workspace_model" "gpt4o" {
  # For a system model this is the slug (e.g. openai/gpt-4o). Changing it forces
  # replacement.
  model_id = "openai/gpt-4o"

  # Exactly one of `all_projects` or `project_ids` must be set.
  sharing = {
    project_ids       = [orq_project.example.id]
    allow_version_pin = true
    allow_fork        = false
    # auto_grant_new_projects must NOT be combined with an explicit project_ids
    # list (it is only valid with all_projects = true).
  }
}

# Share with every project in the workspace, and auto-grant future ones:
#
# resource "orq_workspace_model" "shared_everywhere" {
#   model_id = "anthropic/claude-3-5-sonnet"
#   sharing = {
#     all_projects            = true
#     auto_grant_new_projects = true
#     allow_version_pin       = true
#     allow_fork              = true
#   }
# }
