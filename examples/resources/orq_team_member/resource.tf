resource "orq_team" "platform" {
  display_name = "Platform"
}

# `user_id` is the workspace user id (a UUID), not the account id or the email.
resource "orq_team_member" "alice" {
  team_id = orq_team.platform.id
  user_id = "5c1d7e0a-3b4f-4a6c-8d2e-9f0a1b2c3d4e"
}
