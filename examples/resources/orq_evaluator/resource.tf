# A python_eval evaluator. Keep the source in its own .py file so it stays
# lintable, testable and diffable, and load it with file().
resource "orq_evaluator" "cites_sources" {
  key         = "cites-sources"
  type        = "python_eval"
  path        = "Default/evaluators" # project, then optional folders
  description = "True when the answer cites at least one source"
  output_type = "boolean" # python_eval: boolean | number

  code = file("${path.module}/eval.py")
}

# An llm_eval evaluator judged by a single model.
resource "orq_evaluator" "tone" {
  key         = "tone"
  type        = "llm_eval"
  path        = "Default/evaluators"
  description = "Classifies the tone of the answer"

  mode  = "single"
  model = "openai/gpt-4o" # must support tool calling
  prompt = <<-EOT
    Read the assistant's answer and classify its tone.
    Reply with exactly one of the allowed labels.
  EOT

  output_type = "categorical"
  repetitions = 1 # 1..3

  categorical_labels = [
    { value = "friendly", description = "Warm and welcoming" },
    { value = "neutral", description = "Matter-of-fact" },
    { value = "curt", description = "Terse or dismissive" },
  ]
}

# An llm_eval evaluator judged by a jury. `mode` cannot be changed in place —
# switching between single and jury replaces the evaluator.
resource "orq_evaluator" "factuality_jury" {
  key    = "factuality"
  type   = "llm_eval"
  path   = "Default/evaluators"
  mode   = "jury"
  prompt = "Is the answer factually supported by the retrieved context?"

  output_type = "boolean"

  jury = {
    # At least two judges are required.
    judges = [
      {
        model = "openai/gpt-4o"
        retry = {
          count    = 3
          on_codes = [429, 503]
        }
        # Fallback models are tried in order when this judge keeps failing.
        fallbacks = ["openai/gpt-4o-mini"]
      },
      {
        model = "anthropic/claude-sonnet-4-5"
      },
    ]

    # Used only when a judge fails outright; counts towards min_successful_judges.
    replacement_judges = [
      { model = "google/gemini-2.5-pro" },
    ]

    # At least 2, never more than judges + replacement_judges.
    min_successful_judges = 2
  }
}
