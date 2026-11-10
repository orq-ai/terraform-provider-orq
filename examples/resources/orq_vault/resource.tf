# A workspace vault shared with selected factories. Omit `shared_factory_ids`
# to share it with every factory.
resource "orq_vault" "staging" {
  display_name       = "Staging credentials"
  description        = "Third-party keys for the staging stack."
  shared_factory_ids = ["fac_01K8EXAMPLEFACTORY0000000"]
}

# A vault owned by a single factory — set `factory_id` instead:
#
# resource "orq_vault" "factory" {
#   display_name = "Factory credentials"
#   factory_id   = "fac_01K8EXAMPLEFACTORY0000000"
# }
