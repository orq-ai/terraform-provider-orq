# The project the evaluators below live in. `project_id` is patched in place, so
# moving an evaluator to another project never replaces it.
resource "orq_project" "evals" {
  name = "Evaluations"
}

# A python_eval evaluator. Keep the source in its own .py file so it stays
# lintable, testable and diffable, and load it with file(). The referenced
# eval.py is the studio's default template: the runtime calls `evaluate(log)`
# and the docstring lists every field available on `log`.
resource "orq_evaluator" "reference_match" {
  key         = "reference-match"
  type        = "python_eval"
  project_id  = orq_project.evals.id
  description = "Passes when the output matches the reference and mentions an input keyword"
  output_type = "boolean" # python_eval: boolean | number

  code = file("${path.module}/eval.py")
}

# An llm_eval evaluator judged by a single model.
resource "orq_evaluator" "tone" {
  key         = "tone"
  type        = "llm_eval"
  project_id  = orq_project.evals.id
  description = "Classifies the tone of the answer"

  mode   = "single"
  model  = "openai/gpt-4o" # must support tool calling
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
  key        = "factuality"
  type       = "llm_eval"
  project_id = orq_project.evals.id
  mode       = "jury"
  prompt     = "Is the answer factually supported by the retrieved context?"

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

# A boolean llm_eval judge. The prompt can reference the evaluated log through
# {{log.*}} variables ({{log.input}}, {{log.output}}, {{log.messages}},
# {{log.retrievals}}, {{log.reference}}) — Terraform passes the double curly
# brackets through untouched, since its own interpolation syntax is $${ }.
resource "orq_evaluator" "shakespearean" {
  key         = "shakespearean"
  type        = "llm_eval"
  project_id  = "01JMDPA3QW5C1V0NJ1PW34T4E5" # a project id read from `orq_projects` or the UI
  description = "True when the response is written in Shakespearean English"

  mode  = "single"
  model = "openai/gpt-4o-mini"

  prompt = <<-EOT
    You judge whether a response is written in Shakespearean English
    (Early Modern English, as in Shakespeare's plays and sonnets).

    Return true only if the response substantially exhibits the style:
    - archaic pronouns and inflections (thou, thee, thy, hath, doth, -eth/-est)
    - period vocabulary and idiom (prithee, forsooth, anon, wherefore)
    - inverted or poetic syntax, rhetorical flourish, or blank-verse rhythm

    Return false if it is modern English with only a sprinkled archaism,
    a direct quotation of Shakespeare inside otherwise modern prose, or
    merely formal/old-fashioned but post-Elizabethan English.

    The user asked:
    {{log.input}}

    The response to judge:
    {{log.output}}
  EOT

  output_type = "boolean"
}
