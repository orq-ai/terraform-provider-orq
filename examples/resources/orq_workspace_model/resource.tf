# Enabling a model in the workspace catalog and sharing it with specific projects.
#
# `model_id` is the human-readable model reference `provider/model_id` -- the
# `ref_id` field of an entry in `GET /v2/models`. A model document id (UUID, the
# `id` field) is also accepted for compatibility (see the commented variant).

resource "orq_project" "example" {
  name = "Production"
}

resource "orq_workspace_model" "gpt4o" {
  # Human-readable ref (provider/model_id) -- the recommended form.
  model_id = "openai/gpt-4o"

  # A model DOCUMENT id (UUID) from the `id` field of GET /v2/models is also
  # accepted, and takes precedence when it exactly matches a document. Prefer the
  # ref above; fall back to the UUID only when a ref is ambiguous (the same
  # model_id exists under more than one provider):
  #   model_id = "0b3f2c9e-7a1d-4e5b-9c2a-1f2e3d4c5b6a"

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

# Enable a workspace-CUSTOM model (one managed by orq_model). A custom model's ref
# is `workspaceKey@provider/model_id`. The provider does not expose your workspace
# key, so set it as a literal (replace `acme` with your workspace key); the
# model_id part is interpolated from the orq_model resource, which also orders the
# apply so the custom model exists before it is enabled.
#
# resource "orq_workspace_model" "custom" {
#   model_id = "acme@openailike/${orq_model.self_hosted.model_id}"
#   sharing = {
#     all_projects            = true
#     auto_grant_new_projects = true
#     allow_version_pin       = true
#     allow_fork              = true
#   }
# }
