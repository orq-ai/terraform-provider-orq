# AWS Bedrock inference profiles registered as custom models. No credentials are
# stored on the model: they are resolved at request time from either the pod's
# own IAM identity (auth_mode = "pod-identity") or a dashboard-created AWS
# integration (auth_mode = "integration").
#
# auth_mode, integration_id and model_type are not accepted by the update
# endpoint, so changing any of them forces replacement.

# Pod identity, with a cross-account role assumed via STS. assume_role_arn /
# assume_role_external_id are honored ONLY in this mode, and the server strips
# both from every response -- they are never refreshed, and REMOVING one from
# config forces replacement (the update endpoint cannot clear it).
resource "orq_bedrock_model" "claude_cross_account" {
  display_name    = "Claude Sonnet (prod account)"
  model_id        = "arn:aws:bedrock:eu-central-1:123456789012:application-inference-profile/abc123"
  region          = "eu-central-1"
  model_developer = "anthropic"
  model_family    = "claude"

  auth_mode               = "pod-identity"
  assume_role_arn         = "arn:aws:iam::123456789012:role/orq-bedrock-access"
  assume_role_external_id = var.bedrock_external_id

  input_cost  = 0.003 # USD per 1K input tokens
  output_cost = 0.015 # USD per 1K output tokens

  # Tunables. The update endpoint rebuilds the server's whole parameter list
  # from these three, so clearing the LAST one that is set forces replacement.
  max_tokens    = 8192
  temperature   = 0.7
  has_reasoning = true

  supports_tool_calling = true
  supports_vision       = true
}

variable "bedrock_external_id" {
  type      = string
  sensitive = true
}

# Integration mode: credentials come from an AWS integration created in the orq
# dashboard, named here by its document id. integration_id is required in this
# mode and rejected in pod-identity mode.
resource "orq_bedrock_model" "titan_embeddings" {
  display_name    = "Titan Embeddings"
  model_id        = "arn:aws:bedrock:us-east-1:123456789012:inference-profile/amazon.titan-embed-text-v2"
  region          = "us-east-1"
  model_developer = "amazon"
  model_type      = "embedding"

  auth_mode      = "integration"
  integration_id = "6712f0c1a2b3c4d5e6f70123"

  input_cost = 0.00002
}
