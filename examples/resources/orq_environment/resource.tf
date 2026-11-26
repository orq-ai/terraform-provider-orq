resource "orq_project" "example" {
  name = "Production"
}

# A project-owned environment.
resource "orq_environment" "staging" {
  slug         = "staging"
  display_name = "Staging"
  description  = "Pre-production checks"
  project_id   = orq_project.example.id
}

# A workspace-owned environment sets `sharing` instead of `project_id`:
#
# resource "orq_environment" "production" {
#   slug         = "production"
#   display_name = "Production"
#   sharing = {
#     all_projects = true
#   }
# }
#
# Or shared with specific projects only:
#
# resource "orq_environment" "canary" {
#   slug         = "canary"
#   display_name = "Canary"
#   sharing = {
#     project_ids = [orq_project.example.id]
#   }
# }
