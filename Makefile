# terraform-provider-orq — local development Makefile.
#
# Codegen is reproducible and MUST NOT be hand-edited:
#   - Connect clients: buf generate (config: buf.gen.yaml) from proto/
#   - REST client:     oapi-codegen (config: openapi/oapi-codegen.yaml) from openapi/openapi.json
# CI runs `make generate check-generated` so a stale/edited generated tree fails the build.

BINARY  := terraform-provider-orq
# Sync sources in the platform monorepo (adjust MONOREPO if checked out elsewhere).
MONOREPO ?= ../orquesta-web

# Local-install coordinates for IDE completions (see DEVELOPMENT.md). VERSION is a
# placeholder release used only by the local filesystem mirror; bump it when you
# want `tofu init` to pick up a rebuilt binary in an already-initialised dir.
VERSION    ?= 0.1.0
OS_ARCH    := $(shell go env GOOS)_$(shell go env GOARCH)
PLUGIN_DIR := $(HOME)/.terraform.d/plugins/registry.opentofu.org/orq-ai/orq/$(VERSION)/$(OS_ARCH)

# Pinned tfplugindocs used to render docs/ from the provider schema + examples/.
TFPLUGINDOCS := github.com/hashicorp/terraform-plugin-docs/cmd/tfplugindocs@v0.25.0
TF_VERSION   := 1.15.8

.PHONY: build test testacc generate generate-connect generate-rest proto-sync openapi-sync fetch-source record-source-commit check-generated docs tidy ide-install

build:
	go build -o $(BINARY) .

test:
	go test ./...

# Acceptance tests hit a real orq stack; require ORQ_API_BASE_URL + ORQ_API_KEY.
testacc:
	TF_ACC=1 go test ./... -v -timeout 120m

# Build the provider into OpenTofu's implied local mirror so the IDE's Terraform
# language server can read its schema (dev_overrides yields NO schema). After this,
# run `tofu init` in dev-playground/ (or any dir with a versioned required_providers
# block) to get completions for orq_* resources. See DEVELOPMENT.md.
ide-install:
	@mkdir -p "$(PLUGIN_DIR)"
	go build -o "$(PLUGIN_DIR)/$(BINARY)_v$(VERSION)" .
	@echo "Installed orq-ai/orq v$(VERSION) -> $(PLUGIN_DIR)"
	@echo "Next: cd dev-playground && rm -rf .terraform .terraform.lock.hcl && tofu init"

# Regenerate both clients. Never hand-edit internal/gen or internal/restgen.
generate: generate-connect generate-rest

generate-connect:
	buf generate

generate-rest:
	go tool oapi-codegen -config openapi/oapi-codegen.yaml openapi/openapi.json

# CI guard: fail if the committed generated tree differs from a fresh regen.
check-generated: generate
	@git add -N internal/gen internal/restgen
	@git diff --exit-code -- internal/gen internal/restgen \
		|| { echo "generated code is out of date; run 'make generate' and commit"; exit 1; }

# Registry documentation. Generated from the provider schema and examples/;
# never hand-edit docs/. --tf-version makes tfplugindocs download its own
# terraform rather than use whatever is on PATH (an OpenTofu binary named
# `terraform` resolves the provider against the wrong registry and fails).
docs:
	go run $(TFPLUGINDOCS) generate --tf-version $(TF_VERSION)

tidy:
	go mod tidy

# --- codegen source sync (from the platform monorepo) ------------------------
# proto/ and openapi/openapi.json are COMMITTED COPIES. Re-sync then regenerate.
#
# Files are read from git objects at SOURCE_BRANCH (default main, override with
# e.g. `make proto-sync openapi-sync SOURCE_BRANCH=staging`; any git rev works),
# so the monorepo checkout's working tree and checked-out branch are never
# touched. origin/SOURCE_BRANCH is fetched first and preferred; the local ref is
# the offline fallback.
#
# Every sync stamps SOURCE_COMMIT with the exact orquesta-web revision the
# copies were taken from, so drift between the committed proto/OpenAPI snapshot
# and the monorepo is auditable. Sync BOTH (`make proto-sync openapi-sync`) in
# one invocation so the stamp is meaningful.
SOURCE_BRANCH ?= main

fetch-source:
	@git -C $(MONOREPO) fetch --quiet origin $(SOURCE_BRANCH) \
		|| echo "warn: could not fetch origin/$(SOURCE_BRANCH); falling back to local refs"

# record-source-commit resolves SOURCE_BRANCH to a commit and writes it into
# SOURCE_COMMIT. The sync targets read the rev back from that stamp, so the
# stamp and the copied files always come from the same commit.
record-source-commit: fetch-source
	@{ \
		rev=$$(git -C $(MONOREPO) rev-parse --verify --quiet "origin/$(SOURCE_BRANCH)" \
			|| git -C $(MONOREPO) rev-parse --verify "$(SOURCE_BRANCH)"); \
		printf 'orquesta-web %s\nbranch %s\nsynced %s\n' "$$rev" "$(SOURCE_BRANCH)" "$$(date -u +%Y-%m-%dT%H:%M:%SZ)" > SOURCE_COMMIT; \
		echo "recorded source commit $$rev ($(SOURCE_BRANCH))"; \
	}

proto-sync: record-source-commit
	@set -e; \
	rev=$$(awk '/^orquesta-web /{print $$2}' SOURCE_COMMIT); \
	for f in projects budgets management_keys notifiers model_sharing api_keys identities sharing workspace_settings; do \
		git -C $(MONOREPO) show $$rev:apps/platform-api/proto/orq/platform/v1/$$f.proto > proto/orq/platform/v1/$$f.proto; \
	done; \
	git -C $(MONOREPO) show $$rev:libs/catalog/orq/authz/v1/authz.proto > proto/orq/authz/v1/authz.proto; \
	git -C $(MONOREPO) show $$rev:libs/catalog/orq/apikeys/v1/catalog.proto > proto/orq/apikeys/v1/catalog.proto; \
	git -C $(MONOREPO) show $$rev:libs/catalog/orq/managementkeys/v1/catalog.proto > proto/orq/managementkeys/v1/catalog.proto; \
	git -C $(MONOREPO) show $$rev:apps/platform-api/proto/openapiv3/OpenAPIv3.proto > proto/openapiv3/OpenAPIv3.proto; \
	git -C $(MONOREPO) show $$rev:apps/platform-api/proto/openapiv3/annotations.proto > proto/openapiv3/annotations.proto; \
	echo "synced protos from $(MONOREPO) at $$rev"

openapi-sync: record-source-commit
	@set -e; \
	rev=$$(awk '/^orquesta-web /{print $$2}' SOURCE_COMMIT); \
	git -C $(MONOREPO) show $$rev:.openapi/v2/public/openapi.json > openapi/openapi.json; \
	echo "synced openapi.json from $(MONOREPO) at $$rev"
