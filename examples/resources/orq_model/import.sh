# Custom openai-like models are imported by their catalog document id. Importing
# a system (non-custom) model is refused. `api_key` is never returned by the API,
# so setting it after an import forces replacement.
terraform import orq_model.example 019facd8-a87e-7664-885b-41cdc118e18b
