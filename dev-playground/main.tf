# Working folder for the orq provider (IDE completions via the filesystem mirror).
# Setup (see ../DEVELOPMENT.md):
#   make ide-install                     # from the repo root — builds the v0.1.0 mirror binary
#   cd dev-playground && tofu init       # (-upgrade after a rebuild)
#
# Target: the LOCAL OrbStack cluster (Traefik gateway, orbstack-dev chart) —
# no proxy needed; the gateway serves https directly:
#   export ORQ_URL="https://my.orq-local.test"
#   export ORQ_API_KEY="sk-orq-..."      # an ALL-mode management key
# The LM Studio api key is supplied via the (gitignored) secret.auto.tfvars.

terraform {
  required_providers {
    orq = {
      # Host-explicit so terraform-ls (which canonicalizes bare names to
      # registry.terraform.io) keys the module to the SAME address tofu
      # installs the schema under — otherwise IDE completions silently break.
      source  = "registry.opentofu.org/orq-ai/orq"
      version = "0.1.0"
    }
  }
}

provider "orq" {
  # url and api_key are read from ORQ_URL / ORQ_API_KEY.
}

resource "orq_project" "example" {
  name = "tf-test-project-2"
  description = "Test project for Terraform"
}

variable "lm_api_key" {
  type        = string
  sensitive   = true
  description = "API key LM Studio expects. Set in secret.auto.tfvars (gitignored)."
}

# A self-hosted LM Studio model registered as a custom openai-like model.
# base_url is resolved from inside the cluster (pods reach the host via
# host.docker.internal); LM Studio JIT-loads the model on the create probe.
resource "orq_model" "lmstudio" {
  display_name = "LM Studio LFM2.5 1.2B"
  model_id     = "liquid/lfm2.5-1.2b"
  model_type   = "chat"
  region       = "europe"
  base_url     = "http://host.docker.internal:1234/v1"
  api_key      = var.lm_api_key

  # Capabilities (UI "Capabilities" toggles). The workspace chat "Select target"
  # dropdown only lists tool-calling models, so supports_tool_calling gates it.
  # NOTE: the server omits `false` on the wire, so the provider can set/hold these
  # true but cannot detect a UI flip back to false (retain-on-null, by design).
  supports_tool_calling = true # Function calling
  supports_vision       = false # Vision
  supports_strict_tool  = false # Structured Output
  has_reasoning         = false # Reasoning (encoded server-side as a parameter)
}

# Model Garden enablement. orq_model has NO enable/disable flag — enablement is
# the lifecycle of this orq_workspace_model: its PRESENCE = enabled, destroying it
# = disabled (the model stays in the catalog; only DESTROYING orq_model deletes it).
# Flip the variable to false (e.g. `tofu apply -var lmstudio_enabled=false`) to
# disable without deleting.
variable "lmstudio_enabled" {
  type        = bool
  default     = true
  description = "Model Garden enablement toggle. false disables the model (kept in the catalog)."
}

resource "orq_workspace_model" "lmstudio" {
  count    = var.lmstudio_enabled ? 1 : 0
  model_id = orq_model.lmstudio.id # the resolved document UUID
  sharing = {
    all_projects = true
  }
}

# Enable a built-in (system) model. No orq_model needed — it already exists in the
# catalog; the workspace_model row is what enables it. Referenced by its human
# ref (provider/model) thanks to the provider's ref resolver.
resource "orq_workspace_model" "gpt4o_mini" {
  model_id = "openai/gpt-4o-mini"
  sharing = {
    all_projects = true
  }
}

# gpt-4o enabled but shared ONLY with the newly created test project (scoped
# sharing via project_ids instead of all_projects).
resource "orq_workspace_model" "gpt4o" {
  model_id = "openai/gpt-4o"
  sharing = {
    project_ids = [orq_project.example.id]
  }
}

# Workspace settings singleton (ENG-2440). COMMENTED OUT on purpose: enabling it
# ADOPTS the live workspace settings and renames the workspace on the first
# apply, so it must be an explicit choice rather than something a stray
# `tofu plan` picks up. Uncomment for the live run.
#
# `destroy` on this resource is state-only: nothing is changed server-side.
# Import (any id; `workspace` is the documented sentinel):
#   tofu import orq_workspace_settings.this workspace
#
# resource "orq_workspace_settings" "this" {
#   display_name           = "orq-test"
#   enforce_enabled_models = false
#
#   # Omitted pii_redaction = NOT managed (never sent, never read into state).
#   # It is not the same as `enabled = false`.
# }
