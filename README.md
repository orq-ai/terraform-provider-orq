# terraform-provider-orq

Terraform provider for [orq.ai](https://orq.ai) workspace resources.

## Registry naming requirement

Both registry.terraform.io and the OpenTofu registry ingest only from a **public
GitHub repo named `NAMESPACE/terraform-provider-NAME`**. This repo is
`orq-ai/terraform-provider-orq`, so the provider type is `orq` and the
`providerserver` address is `registry.terraform.io/orq-ai/orq` (see `main.go`).

## Provider configuration

```hcl
provider "orq" {
  url     = "https://my.orq.ai" # optional; env ORQ_API_BASE_URL; defaults to https://my.orq.ai
  api_key = "sk-orq-..."        # optional; env ORQ_API_KEY
}
```

- The credential is an opaque `sk-orq-...` **management key**; the workspace is
  implied by the credential (no `workspace` argument).
- Precedence: explicit `api_key` attribute **beats** `ORQ_API_KEY`; same for `url` /
  `ORQ_API_BASE_URL`.
- Sent as `Authorization: Bearer <token>` on **both** transports.
- **Lazy credential validation:** provider init only checks that `url`/`api_key`
  are structurally present and well-formed. It does **not** probe
  `/v2/management-keys/capabilities` (a public, unauthenticated route that cannot
  validate a credential). The first real API call surfaces auth errors naming
  `ORQ_API_KEY`.

## Architecture: two generated clients, one interface

The resource layer is transport-agnostic. It depends only on the per-domain
interfaces in [`internal/client`](internal/client); the concrete `Client`
dispatches each domain to its native transport:

| Transport | Generator | Source | Output | Domains |
|-----------|-----------|--------|--------|---------|
| Connect (connect-go) | `buf` (`buf.gen.yaml`) | `proto/` (committed copy) | `internal/gen` | projects, budgets, notifiers, management-keys, model-sharing, api-keys, identities |
| REST | `oapi-codegen` (`openapi/oapi-codegen.yaml`) | `openapi/openapi.json` (committed copy) | `internal/restgen` | guardrail-rules, routing-rules, models, workspace-models |

Connect calls go to `${ORQ_API_BASE_URL}/v3/rpc/platform` (the gateway strips that prefix
to the bare `/orq.platform.v1.<Service>/...` path). REST calls go to
`${ORQ_API_BASE_URL}/v2/...`.

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

Re-sync with `make proto-sync openapi-sync`, then `make generate`. The sources
are read from git objects at `SOURCE_BRANCH` (default `main`; override with e.g.
`make proto-sync openapi-sync SOURCE_BRANCH=staging` — any git rev works), so
the monorepo checkout itself is never touched. Each sync stamps
[`SOURCE_COMMIT`](./SOURCE_COMMIT) with the exact orquesta-web git revision the
copies were taken from, so drift between this repo's committed snapshot and the
monorepo is auditable.

Codegen is byte-for-byte reproducible: the buf remote plugin versions are pinned
in `buf.gen.yaml` (`protocolbuffers/go`, `connectrpc/gosimple`), the buf CLI is
pinned in `.github/workflows/ci.yml`, and `oapi-codegen` is pinned via `go tool`.
`buf.gen.yaml` sets `clean: true` so a proto removed from the sync leaves no
orphaned `*.pb.go`. The REST client is scoped by an exact operation-ID allowlist
in `openapi/oapi-codegen.yaml` (not a tag include) so the OpenAI-compatible
`/v3/router/models` garden endpoint is not pulled into the provider surface.

## Make targets

| Target | Purpose |
|--------|---------|
| `build` | compile the provider binary |
| `test` | unit tests |
| `testacc` | acceptance tests (`TF_ACC=1`; needs `ORQ_API_BASE_URL` + `ORQ_API_KEY`) |
| `generate` | regenerate both clients (buf + oapi-codegen) |
| `check-generated` | CI guard: fail if generated code is stale |
| `docs` | regenerate `docs/` for the registries (tfplugindocs) |
| `proto-sync` / `openapi-sync` | re-copy codegen inputs from the monorepo (`SOURCE_BRANCH=main` by default) |

## Prerequisites

- Go 1.26 (matches the platform monorepo)
- [`buf`](https://buf.build) on `PATH` for `make generate`
- `oapi-codegen` is pinned as a Go tool dependency (`go tool`); no separate install.

## Publishing

Releases are tag-driven: pushing a `vX.Y.Z` tag runs
[`.github/workflows/release.yml`](.github/workflows/release.yml), which builds
and GPG-signs the assets with [`.goreleaser.yml`](./.goreleaser.yml) and creates
a **draft** GitHub release. A maintainer must click "Publish release" before
registry.terraform.io and the OpenTofu registry can ingest the tag.

Requires two repo secrets: `GPG_PRIVATE_KEY` (the ASCII-armored signing key,
whose public half is registered with the registries) and `PASSPHRASE`.

Registry documentation is generated from the provider schema and `examples/`
with `make docs`; CI fails if `docs/` is stale. Never hand-edit `docs/`.
