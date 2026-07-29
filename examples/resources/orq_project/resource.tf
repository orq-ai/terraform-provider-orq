resource "orq_project" "example" {
  name        = "Production"
  description = "Primary production workloads"
  teams       = ["team_engineering", "team_platform"]
}
