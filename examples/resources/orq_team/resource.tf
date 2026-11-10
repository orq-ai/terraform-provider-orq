resource "orq_team" "platform" {
  display_name = "Platform"
  description  = "Owns the shared gateway configuration"
}

# Project access is granted from the project side.
resource "orq_project" "production" {
  name  = "Production"
  teams = [orq_team.platform.id]
}
