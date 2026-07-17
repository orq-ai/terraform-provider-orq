resource "orq_routing_rule" "fallback" {
  display_name = "GPT-4 weighted fallback"
  description  = "Split traffic across two models, weighted"
  enabled      = true
  priority     = 10 # >= 0; lower numbers evaluate first

  # Optional CEL match expression; the rule applies only when it evaluates true.
  expression = {
    cel = "model == \"gpt-4\""
  }

  # `models_config` is a JSON object string. Each model entry uses the key
  # `model` (a model slug) and an optional `weight` in 0..1 (a model with an
  # omitted or zero weight is stored server-side as weight = 0.5).
  models_config = jsonencode({
    mode = "fallback"
    models = [
      { model = "openai/gpt-4o", weight = 0.7 },
      { model = "openai/gpt-4o-mini", weight = 0.3 },
    ]
  })
}
