# Lists every project visible to the provider credential (newest first).
data "orq_projects" "all" {}

# Project ids, keyed by name, for wiring other resources.
output "project_ids_by_name" {
  value = { for p in data.orq_projects.all.projects : p.name => p.id }
}

# Only the non-archived projects.
output "active_project_keys" {
  value = [for p in data.orq_projects.all.projects : p.key if !p.is_archived]
}
