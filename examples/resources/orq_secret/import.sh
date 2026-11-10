# Secrets are imported as <vault_id>/<secret_id>. Metadata only: the value is
# never returned, so the next apply rotates it to the configured `value`.
terraform import orq_secret.example vault_01K8EXAMPLEVAULT000000000/sec_01K8EXAMPLESECRET000000000
