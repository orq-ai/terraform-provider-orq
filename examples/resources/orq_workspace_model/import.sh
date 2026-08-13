# Workspace models are imported by the model reference or by the catalog
# document id — the same forms `model_id` accepts. An import by document id
# stores the canonical reference in `model_id`, so a config written with the
# reference plans clean (`model_id` forces replacement).
terraform import orq_workspace_model.example openai/gpt-4o
