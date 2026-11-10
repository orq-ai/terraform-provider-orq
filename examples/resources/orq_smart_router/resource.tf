resource "orq_smart_router" "support" {
  key     = "support-router" # immutable; forms the model reference
  profile = "balanced"       # quality | balanced | cost

  # 2 to 50 distinct models that are enabled in the workspace.
  models = [
    "openai/gpt-4o",
    "openai/gpt-4o-mini",
    "anthropic/claude-sonnet-4-5",
  ]

  # Optional. Removing the block turns session affinity off.
  session_affinity = {
    enabled     = true
    ttl_seconds = 900 # 60 to 3600
  }
}

# Use the router as a model in AI Gateway requests.
output "support_router_model" {
  value = orq_smart_router.support.model_ref
}

# The smallest usable configuration, created disabled.
resource "orq_smart_router" "minimal" {
  key     = "cheap-router"
  profile = "cost"
  models  = ["openai/gpt-4o-mini", "openai/gpt-4.1-nano"]
  enabled = false
}
