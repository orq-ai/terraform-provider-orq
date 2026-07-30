# The settings are a per-workspace singleton with no id; the workspace is implied
# by the credential. Any id works — use the `workspace` sentinel.
terraform import orq_workspace_settings.this workspace
