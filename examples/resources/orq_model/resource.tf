# A custom OpenAI-compatible ("openai-like") model. Only custom models are
# managed by this resource; a system or non-custom model is refused on read and
# import (it can never be PATCHed or DELETEd here).
#
# The API key is a secret and is STORED IN TERRAFORM STATE (the server never
# returns it). Use an encrypted remote backend, and pass the key through a
# sensitive variable (e.g. TF_VAR_model_api_key=...) rather than hard-coding it.
# Changing api_key forces replacement (the update endpoint cannot rotate it).
#
# Import recovers only the model id; api_key is required and unreadable, so after
# `terraform import` you must add api_key to config -- the next apply then
# REPLACES (destroy + recreate) the model rather than adopting it in place.
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

  # Sensitive: passed through verbatim; sourced here from a Terraform variable.
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
