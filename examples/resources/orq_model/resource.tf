# A custom OpenAI-compatible ("openai-like") model. Only custom models are
# managed by this resource; system models (slug ids like "openai/gpt-4o") are not.

# The API key is a secret: pass it through a sensitive Terraform variable
# (e.g. TF_VAR_model_api_key=...) rather than hard-coding it. The server never
# returns the key, so it is held from config and changing it forces replacement.
variable "model_api_key" {
  type      = string
  sensitive = true
}

resource "orq_model" "self_hosted" {
  display_name = "Self-hosted LFM2"
  model_id     = "liquid/lfm2.5-1.2b"
  model_type   = "chat"
  region       = "europe"
  base_url     = "https://llm.internal.example.com/v1"

  # Literal key or an "env://VAR" reference (stored verbatim).
  api_key = var.model_api_key

  # Optional cost / capability metadata (all omittable):
  # description           = "Internal chat model"
  # input_cost            = 0.1
  # output_cost           = 0.2
  # max_tokens            = 8192
  # temperature           = 0.7
  # supports_tool_calling = true
  # supports_vision       = false
}
