resource "orq_routing_rule" "fallback" {
  display_name = "GPT-4 weighted fallback"
  description  = "Split traffic across two models, weighted"
  enabled      = true
  priority     = 10 # >= 0; lower numbers evaluate first

  # Optional CEL match expression; the rule applies only when it evaluates true.
  expression = {
    cel = "model == \"gpt-4\""
  }

  # Optional. Omit the whole block for a rule that only matches; removing it
  # from an existing rule clears the stored configuration.
  models_config = {
    mode = "weighted" # fallback | latency_based | weighted | round_robin
    models = [
      { model = "openai/gpt-4o", weight = 0.7 },
      { model = "openai/gpt-4o-mini", weight = 0.3 },
    ]
  }
}

# The smallest usable configuration: a mode and one model. `display_name`,
# `weight` (0.5) and `integration_id` are filled in by the server.
resource "orq_routing_rule" "minimal" {
  display_name = "Default route"

  models_config = {
    mode   = "fallback"
    models = [{ model = "openai/gpt-4o" }]
  }
}
