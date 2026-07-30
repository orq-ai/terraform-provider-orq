# AWS Bedrock models are imported by their catalog document id (a UUID). A model
# that is not a Bedrock one -- a system model, an openai-like custom model, or a
# legacy AWS access-key model -- is refused. assume_role_arn and
# assume_role_external_id are stripped from every server response, so import
# cannot recover them; add them to config if the imported model uses one.
terraform import orq_bedrock_model.example 019facd8-a87e-7664-885b-41cdc118e18b
