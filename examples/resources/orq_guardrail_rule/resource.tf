resource "orq_guardrail_rule" "pii" {
  display_name = "PII protection"
  description  = "Redact PII on both input and output"
  enabled      = true
  timeout      = 5000 # milliseconds

  # Each `guardrails` block references a guardrail evaluator by id. `options` is a
  # free-form JSON object string (compared semantically) carrying that
  # evaluator's configuration.
  guardrails {
    id           = "guardrail_pii"
    execute_on   = "both" # input | output | both
    sample_rate  = 1      # 0..1
    is_guardrail = true    # enforce (block) rather than just observe

    options = jsonencode({
      language  = "en"
      threshold = 0.5
      entities  = ["EMAIL_ADDRESS", "PHONE_NUMBER", "CREDIT_CARD"]
    })
  }
}
