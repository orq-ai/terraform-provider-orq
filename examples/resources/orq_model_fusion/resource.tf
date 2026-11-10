resource "orq_model_fusion" "research" {
  key    = "research-fusion" # immutable; forms the model reference
  preset = "quality"         # general | quality | budget | code | safety

  # 1 to 8 distinct chat models that answer independently.
  panel_models = [
    "openai/gpt-4o",
    "anthropic/claude-sonnet-4-5",
  ]
  judge_model = "openai/gpt-4o"

  # analysis needs an orchestrator_model to synthesize the final answer.
  synthesis_mode     = "analysis"
  orchestrator_model = "anthropic/claude-sonnet-4-5"

  max_tool_calls    = 4 # 0 to 20; defaults to 8
  feed_raw_to_synth = true
}

# The judge writes the final answer, so there is no orchestrator_model. The
# budget preset never uses web tools.
resource "orq_model_fusion" "budget" {
  key            = "budget-fusion"
  preset         = "budget"
  panel_models   = ["openai/gpt-4o-mini"]
  judge_model    = "openai/gpt-4o-mini"
  synthesis_mode = "judge_writes_final"
}
