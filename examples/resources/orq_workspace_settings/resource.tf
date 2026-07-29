# Workspace settings are a SINGLETON: the management key already selects the
# workspace, so there is no id -- and declaring this resource twice would make
# two configurations fight over one object. Declare it exactly once.
#
# `create` ADOPTS the existing settings (it writes the attributes you set and
# reads the rest back); `destroy` only removes it from Terraform state and
# changes NOTHING server-side.
#
# Import with any id; the documented sentinel is `workspace`:
#   terraform import orq_workspace_settings.this workspace

resource "orq_workspace_settings" "this" {
  display_name           = "Acme Inc"
  enforce_enabled_models = true

  # Workspace-default PII redaction. OMITTING this block (as below) means the
  # resource does not manage PII redaction at all: nothing is sent on apply and
  # the server value is never read into state, so an out-of-band change produces
  # no diff. That is NOT the same as `enabled = false`, which actively turns the
  # workspace default off.
  #
  # Once the block is present it FULLY REPLACES the stored configuration on every
  # apply -- a field you delete from config is deleted server-side too, it does
  # not keep its last value.
  #
  # `enabled` is REQUIRED, so `pii_redaction = {}` is intentionally a
  # configuration error: an empty block would be ambiguous between "manage it,
  # disabled" and "do not manage it". Spell it out or leave it out.
  #
  # pii_redaction = {
  #   enabled = true
  #   config = {
  #     language = "en" # en | nl
  #
  #     # The entity catalog is per-language and validated SERVER-side (an unknown
  #     # value fails at apply, not at plan). An empty list [] means "redact every
  #     # type the detector finds"; omitting `entities` stores no list at all.
  #     # Values round-trip verbatim -- the server matches the catalog
  #     # case-insensitively but stores and returns exactly what you wrote, in
  #     # order -- so no canonical spelling is imposed on your config.
  #     entities = ["EMAIL_ADDRESS", "PERSON"]
  #
  #     on_failure = "block" # block (fail closed) | passthrough (fail open)
  #     threshold  = 0.5     # [0, 1]
  #   }
  # }
}
