resource "orq_policy" "prod" {
  display_name = "Production guardrails"
  description  = "Spend limits, retries, model routing, and evaluators for production"
  enabled      = true
  timeout      = 300000 # milliseconds (>= 1000)

  # Request / token / budget limits as a JSON object string. `period` is one of
  # hour | day | week | month.
  limits = jsonencode({
    requests = {
      amount = 10000
      period = "day"
    }
  })

  # Model routing config. Each entry uses the key `model` (a slug) and an
  # optional `weight` in 0..1.
  models_config = jsonencode({
    mode = "fallback"
    models = [
      { model = "openai/gpt-4o", weight = 0.5 },
      { model = "anthropic/claude-3-5-sonnet", weight = 0.5 },
    ]
  })

  # Retry config: a `count` and the HTTP status codes to retry on.
  retry_config = jsonencode({
    count    = 2
    on_codes = [429, 503]
  })

  # Referenced evaluators (mirror guardrail refs).
  evaluators {
    id           = "evaluator_toxicity"
    execute_on   = "output" # input | output | both
    sample_rate  = 1        # 0..1
    is_guardrail = true

    options = jsonencode({
      threshold = 0.8
    })
  }
}
