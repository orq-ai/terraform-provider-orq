# The `type` gates which destination field applies. Email is the common case.
resource "orq_notifier" "email" {
  display_name = "On-call email"
  type         = "EMAIL"
  emails       = ["oncall@example.com", "sre@example.com"]
}

# Slack incoming webhook — set `incoming_webhook_url` instead of `emails`:
#
# resource "orq_notifier" "slack" {
#   display_name         = "Alerts channel"
#   type                 = "SLACK_WEBHOOK"
#   incoming_webhook_url = "https://hooks.slack.com/services/T00000000/B00000000/XXXXXXXX"
# }

# Generic webhook — set `webhook_url` instead:
#
# resource "orq_notifier" "webhook" {
#   display_name = "PagerDuty events"
#   type         = "WEBHOOK"
#   webhook_url  = "https://events.example.com/hooks/orq"
# }
