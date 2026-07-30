# ENG-2440 live E2E playground — targets the LOCAL OrbStack cluster only.
#   export ORQ_URL="https://my.orq-local.test"
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

provider "orq" {
  # url and api_key are read from ORQ_URL / ORQ_API_KEY.
}

variable "lm_api_key" {
  type        = string
  sensitive   = true
  description = "API key LM Studio expects. Set in secret.auto.tfvars (gitignored)."
}

resource "orq_project" "terraform-created" {
  name        = "tf-e2e-project"
  description = "TF e2e"
}

resource "orq_model" "gemma" {
  display_name          = "LM Studio Gemma 4 E2B"
  model_id              = "google/gemma-4-e2b"
  model_type            = "chat"
  region                = "europe"
  base_url              = "http://host.docker.internal:1234/v1"
  api_key               = var.lm_api_key
  has_reasoning         = true
  supports_tool_calling = true
  supports_vision       = false
  supports_strict_tool  = false
}

# resource "orq_workspace_model" "gemma" {
#   model_id = orq_model.gemma.id
#   sharing = {
#     all_projects = true
#   }
# }

resource "orq_workspace_model" "sys" {
  model_id = "openai/gpt-4o-mini"
  sharing = {
    all_projects = true
  }
}

# resource "orq_workspace_model" "scoped" {
#   model_id = "openai/gpt-4o"
#   sharing = {
#     project_ids = [orq_project.example.id]
#   }
# }

# api-key minting works when the management key holds `api-key: write`
# (ALL-mode keys do); the minted key's lifetime is capped by the actor's.
# Management-key creation still needs an explicit RESTRICTED
# `management-key: write` grant — the domain is excluded from the ALL and
# READ_ONLY presets, so an ALL-mode key gets 403 by design.
# resource "orq_api_key" "router" {
#   name       = "tf-e2e-router-key"
#   project_id = orq_project.example.id
# }
#
# resource "orq_management_key" "ro" {
#   name            = "tf-e2e-ro"
#   permission_mode = "MANAGEMENT_PERMISSION_MODE_READ_ONLY"
# }

# resource "orq_notifier" "email" {
#   display_name = "tf-e2e-notifier"
#   type         = "EMAIL"
#   emails       = ["e2e@orq-local.test"]
# }

# resource "orq_budget" "ws" {
#   scope = {
#     kind = "WORKSPACE"
#   }
#   limits = {
#     period = "MONTHLY"
#     amount = 25
#   }
#   rate_limit_per_minute = 60

#   alerts {
#     threshold_percent = 80
#     notifier_ids      = [orq_notifier.email.id]
#     dimension         = "COST"
#   }
# }

# resource "orq_routing_rule" "fallback" {
#   display_name = "tf-e2e-routing"
#   priority     = 10
#   expression = {
#     cel = "model == \"openai/gpt-4o-mini\""
#   }
#   models_config = jsonencode({
#     mode = "fallback"
#     models = [
#       { model = "openai/gpt-4o-mini", weight = 1 },
#     ]
#   })
# }


# --- orq_evaluator -----------------------------------------------------------
# Commented out on purpose: this playground is applied against a live local
# stack and evaluators need a `path` whose project exists there, plus (for
# llm_eval) a tool-calling model the workspace can actually reach. Uncomment
# whichever half you want to exercise — leaving it commented keeps the
# playground's plan unchanged.
#
# resource "orq_evaluator" "py" {
#   key         = "tf-e2e-python"
#   type        = "python_eval"
#   path        = "Default"
#   description = "TF e2e python evaluator"
#   output_type = "boolean"
#   code        = file("${path.module}/eval.py")
# }
#
# resource "orq_evaluator" "llm" {
#   key         = "tf-e2e-llm"
#   type        = "llm_eval"
#   path        = "Default"
#   mode        = "single"
#   model       = "openai/gpt-4o-mini"
#   prompt      = "Answer true when the response is polite."
#   output_type = "boolean"
#   repetitions = 1
# }
