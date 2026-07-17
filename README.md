# terraform-provider-orq

Terraform provider for [orq.ai](https://orq.ai) workspace resources (ENG-2440, phase 1).

> **Status: local development only.** Not published to any registry. The upstream
> GitHub repo `orq-ai/terraform-provider-orq` is created separately by the repo
> owner; do not `git push` or add a remote until that exists and publish is
> approved. This scaffold currently ships auth + generated clients + one
> read-only data source (`orq_projects`) as an end-to-end smoke test; the full
> resource set lands in a later task.

## Registry naming requirement

Both registry.terraform.io and the OpenTofu registry ingest only from a **public
GitHub repo named `NAMESPACE/terraform-provider-NAME`**. This repo is
`orq-ai/terraform-provider-orq`, so the provider type is `orq` and the
`providerserver` address is `registry.terraform.io/orq-ai/orq` (see `main.go`).

## Provider configuration

```hcl
provider "orq" {
  url   = "https://api.orq.ai" # optional; env ORQ_URL; defaults to https://api.orq.ai
  token = "sk-orq-..."         # optional; env ORQ_TOKEN (like GOOGLE_CREDENTIALS)
}
```

- The credential is an opaque `sk-orq-...` **management key**; the workspace is
  implied by the credential (no `workspace` argument).
- Precedence: explicit `token` attribute **beats** `ORQ_TOKEN`; same for `url` /
  `ORQ_URL`.
- Sent as `Authorization: Bearer <token>` on **both** transports.
- **Lazy credential validation:** provider init only checks that `url`/`token`
  are structurally present and well-formed. It does **not** probe
  `/v2/management-keys/capabilities` (a public, unauthenticated route that cannot
  validate a credential). The first real API call surfaces auth errors naming
  `ORQ_TOKEN`.

## Architecture: two generated clients, one interface

The resource layer is transport-agnostic. It depends only on the per-domain
interfaces in [`internal/client`](internal/client); the concrete `Client`
dispatches each domain to its native transport:

| Transport | Generator | Source | Output | Domains |
|-----------|-----------|--------|--------|---------|
| Connect (connect-go) | `buf` (`buf.gen.yaml`) | `proto/` (committed copy) | `internal/gen` | projects, budgets, notifiers, management-keys, model-sharing, api-keys, identities |
| REST | `oapi-codegen` (`openapi/oapi-codegen.yaml`) | `openapi/openapi.json` (committed copy) | `internal/restgen` | guardrail-rules, routing-rules, policies, models, workspace-models |

Connect calls go to `${ORQ_URL}/v3/rpc/platform` (the gateway strips that prefix
to the bare `/orq.platform.v1.<Service>/...` path). REST calls go to
`${ORQ_URL}/v2/...`.

The REST client is expected to be retired over time; the `internal/client`
interface is the seam that lets a domain move from REST to Connect without
touching resource code.

### Codegen is reproducible — never hand-edit

`internal/gen` and `internal/restgen` are committed but generated. Regenerate
with `make generate`; CI runs `make check-generated` to fail on drift.

The proto and OpenAPI inputs are **committed copies** synced from the platform
monorepo (`../orquesta-web` by default; override `MONOREPO=`):

- `proto/orq/platform/v1/*.proto` ← `apps/platform-api/proto/orq/platform/v1/`
- `proto/openapiv3/*.proto` ← `apps/platform-api/proto/openapiv3/`
- `proto/orq/{authz,apikeys,managementkeys}/**` ← `libs/catalog/orq/...`
- `openapi/openapi.json` ← `.openapi/v2/public/openapi.json`

Re-sync with `make proto-sync` / `make openapi-sync`, then `make generate`.

## Make targets

| Target | Purpose |
|--------|---------|
| `build` | compile the provider binary |
| `test` | unit tests |
| `testacc` | acceptance tests (`TF_ACC=1`; needs `ORQ_URL` + `ORQ_TOKEN`) |
| `generate` | regenerate both clients (buf + oapi-codegen) |
| `check-generated` | CI guard: fail if generated code is stale |
| `proto-sync` / `openapi-sync` | re-copy codegen inputs from the monorepo |

## Prerequisites

- Go 1.26 (matches the platform monorepo)
- [`buf`](https://buf.build) on `PATH` for `make generate`
- `oapi-codegen` is pinned as a Go tool dependency (`go tool`); no separate install.

## Publishing

`.goreleaser.yml` is present but **unused** until the GitHub upstream and a GPG
signing key exist. Releases are tag-driven (`git tag vX.Y.Z`); both registries
auto-detect them.
