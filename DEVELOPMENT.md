# Development

## IDE completions for a local (unpublished) build

Editor autocomplete for `orq_*` resources and attributes comes from the **provider
schema**, which the Terraform language server (`terraform-ls`, bundled by the VS Code
and JetBrains Terraform plugins) reads only from a directory that has been
`terraform init`/`tofu init`-ed against an *installed* provider.

`dev_overrides` (the fast edit→apply loop, below) deliberately **skips `init`**, so it
provides **no schema** — you get no completions in a dir configured that way. To get
completions before the provider is published to a registry, install it into OpenTofu's
implied local mirror and init a normal directory:

```sh
make ide-install                 # builds into ~/.terraform.d/plugins/registry.opentofu.org/orq-ai/orq/<version>/<os>_<arch>/
cd dev-playground
rm -rf .terraform .terraform.lock.hcl   # only needed after a version bump / re-install
tofu init                        # installs from the local mirror; writes the schema terraform-ls reads
```

Open `dev-playground/main.tf` (or any dir with the versioned `required_providers`
block below) in your IDE — completions for all `orq_*` resources and their attributes
now work.

Notes / gotchas:
- **Spell the source host-explicitly: `source = "registry.opentofu.org/orq-ai/orq"`.**
  terraform-ls (a HashiCorp tool) canonicalizes a bare `orq-ai/orq` to
  `registry.terraform.io/...`, while `tofu init` keys the installed schema under
  `registry.opentofu.org/...` — the LSP then can't join the module to its schema and
  every resource attribute shows `unknown attribute` with no completions, even though
  `terraform providers schema -json` works. The explicit host makes both agree.
- **terraform-ls needs a binary literally named `terraform`.** With only OpenTofu
  installed, symlink it: `ln -s $(command -v tofu) /opt/homebrew/bin/terraform`
  (tofu is CLI-compatible for everything the LSP invokes).
- **`dev_overrides` and completions are mutually exclusive in the same dir.** Use the
  mirror + `init` (above) for editing with completions; use `dev_overrides` for the
  fast test loop (below). Don't point `TF_CLI_CONFIG_FILE` at a dev_overrides config in
  the dir where you want completions.
- After you rebuild the provider, re-run `make ide-install`. If the dir was already
  init'd, either `tofu init -upgrade` or bump `VERSION` (e.g. `make ide-install VERSION=0.1.1`
  and update the `version` constraint) so `tofu init` picks up the new binary.
- The registry-style files under `examples/` intentionally omit the `terraform`/
  `required_providers` block (the docs generator injects it), so they do **not** get
  completions on their own. Author snippets in `dev-playground/` instead.
- **The real, zero-setup fix is publishing** `orq-ai/orq` to the OpenTofu (and
  Terraform) registry. Once published, terraform-ls downloads the schema automatically
  and completes everywhere with no local mirror and no per-dir `init`.

Minimal block that makes a directory completion-capable:

```hcl
terraform {
  required_providers {
    orq = {
      source  = "orq-ai/orq"
      version = "0.1.0"
    }
  }
}
```

## Fast test loop (dev_overrides)

For iterating on behaviour with `tofu plan/apply` against a running orq stack without
`init`, build the binary and point a CLI config's `dev_overrides` at it:

```sh
make build
cat > /tmp/orq-dev.tfrc <<EOF
provider_installation {
  dev_overrides { "orq-ai/orq" = "$(pwd)" }
  direct {}
}
EOF
export TF_CLI_CONFIG_FILE=/tmp/orq-dev.tfrc
export ORQ_URL=...          # your orq API base (e.g. https://my.orq.ai)
export ORQ_API_KEY=sk-orq-...   # a management key
# now `tofu plan/apply` in any dir — no `tofu init` needed (and no schema for the IDE)
```

## Codegen

`internal/gen` (Connect) and `internal/restgen` (REST) are generated and committed;
never hand-edit them. Regenerate with `make generate`; CI enforces freshness via
`make check-generated`.
