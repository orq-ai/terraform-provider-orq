# Scratch dir for authoring orq resources WITH IDE completions.
# Setup (see ../DEVELOPMENT.md):
#   make ide-install      # from the repo root
#   cd dev-playground && tofu init
# Then edit below — the IDE completes orq_* resources + attributes.
# Never commit real credentials; provide them via env:
#   export ORQ_URL=...  ORQ_API_KEY=sk-orq-...

terraform {
  required_providers {
    orq = {
      source  = "orq-ai/orq"
      version = "0.1.0"
    }
  }
}

provider "orq" {
  # url and api_key are optional; prefer ORQ_URL / ORQ_API_KEY env vars.
}

resource "orq_project" "example" {
  name        = "playground"
  description = "scratch project — try completions here"
}
