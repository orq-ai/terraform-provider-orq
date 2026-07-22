# Enabling a model in the workspace catalog and sharing it with specific projects.
resource "orq_project" "example" {
  name = "Production"
}

resource "orq_workspace_model" "gpt4o" {
  # model_id is the model DOCUMENT ID (the `id` field returned by GET /v2/models),
  # a document UUID on this backend -- NOT a display slug. A slug like
  # "openai/gpt-4o" can map to several provider documents, so enabling by slug
  # 404s; always copy the document `id` from the models list. Changing it forces
  # replacement.
  model_id = "0b3f2c9e-7a1d-4e5b-9c2a-1f2e3d4c5b6a" # example document UUID

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
#   model_id = "1a2b3c4d-5e6f-7a8b-9c0d-1e2f3a4b5c6d" # document UUID from GET /v2/models
#   sharing = {
#     all_projects            = true
#     auto_grant_new_projects = true
#     allow_version_pin       = true
#     allow_fork              = true
#   }
# }
