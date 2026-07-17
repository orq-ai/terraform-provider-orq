# A budget references a project (its scope target) and one or more notifiers
# (its alert destinations), so a small project and notifier are included here to
# keep the example self-contained.
resource "orq_project" "example" {
  name = "Production"
}

resource "orq_notifier" "budget_alerts" {
  display_name = "FinOps budget alerts"
  type         = "EMAIL"
  emails       = ["finops@example.com"]
}

resource "orq_budget" "project_monthly" {
  # Provide EXACTLY ONE of `scope` (structured, immutable) or `match_cel`.
  # The scope is immutable — changing it forces replacement.
  scope = {
    kind   = "PROJECT" # WORKSPACE | PROJECT | IDENTITY | API_KEY | PROVIDER | MODEL
    target = orq_project.example.id
  }

  # A dynamic, CEL-matched budget instead of a structured scope:
  # match_cel = "request.model == 'openai/gpt-4o'"

  # At least one of amount / token_limit (here) or rate_limit_per_minute (below)
  # must be set.
  limits = {
    period      = "MONTHLY" # DAILY | WEEKLY | MONTHLY | YEARLY | ONE_TIME
    amount      = 500        # USD spend ceiling
    token_limit = 10000000   # token ceiling
  }

  rate_limit_per_minute = 600
  is_active             = true
  expires_at            = "2027-01-01T00:00:00Z"

  # Alerts are nested blocks; each fires once per period when consumption crosses
  # the threshold.
  alerts {
    threshold_percent = 80
    notifier_ids      = [orq_notifier.budget_alerts.id]
    dimension         = "COST" # COST (default) | TOKENS
  }

  alerts {
    threshold_percent = 95
    notifier_ids      = [orq_notifier.budget_alerts.id]
    dimension         = "TOKENS"
  }
}
