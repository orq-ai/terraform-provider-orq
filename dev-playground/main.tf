# ENG-2440 live E2E playground — targets the LOCAL OrbStack cluster only.
#   export ORQ_API_BASE_URL="https://my.orq-local.test"
#   export ORQ_API_KEY="sk-orq-..."      # an ALL-mode management key
# LM Studio api key comes from the (gitignored) secret.auto.tfvars.

terraform {
  required_providers {
    orq = {
      source  = "registry.opentofu.org/orq-ai/orq"
      version = "0.1.0"
    }
  }
}

provider "orq" {}

variable "lm_api_key" {
  type        = string
  sensitive   = true
  description = "API key LM Studio expects. Set in secret.auto.tfvars (gitignored)."
}

resource "orq_project" "terraform-created" {
  name        = "tf-e2e-project"
  description = "TF e2e"
}
resource "orq_project" "template" {
  name = "template-project"
}

resource "orq_workspace_model" "gpt4o" {
  model_id = "openai/gpt-4o-mini"
  sharing = {
    all_projects = true
  }
}

resource "orq_workspace_model" "luna" {
  model_id = "openai/gpt-5.6-luna"
  sharing = {
    all_projects = true
  }
}

resource "orq_api_key" "router" {
  name       = "tf-e2e-router-key"
  project_id = orq_project.terraform-created.id
}


resource "orq_notifier" "email" {
  display_name = "tf-e2e-notifier"
  type         = "EMAIL"
  emails       = ["markpeter@orq.ai"]
}

resource "orq_budget" "ws" {
  scope = {
    kind   = "API_KEY"
    target = orq_api_key.router.id
  }

  limits = {
    period = "DAILY"
    amount = 0.2
  }
  is_active = true

  alerts {
    threshold_percent = 20
    notifier_ids      = [orq_notifier.email.id]
    dimension         = "COST"
  }
}

resource "orq_evaluator" "shakespearean" {
  key         = "shakespearean"
  type        = "llm_eval"
  project_id  = orq_project.terraform-created.id
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

# A python_eval evaluator. The source lives in its own file so it stays
# lintable and diffable; `file()` inlines it at plan time. eval.py is the
# studio's default template: `evaluate(log)` with all log fields documented.
resource "orq_evaluator" "cites_sources" {
  key         = "cites-sources"
  type        = "python_eval"
  project_id  = orq_project.terraform-created.id
  description = "True when the answer cites at least one source"

  output_type = "boolean"
  code        = file("${path.module}/eval.py")
}

resource "orq_guardrail_rule" "shakespeare_guard" {
  display_name = "Shakespearean output only"
  description  = "Blocks any response the judge deems non-Shakespearean"

  enabled = true
  timeout = 30000

  guardrails {
    id           = orq_evaluator.shakespearean.id
    execute_on   = "output"
    sample_rate  = 1
    is_guardrail = true
  }
}
