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

.PHONY: build test testacc generate generate-connect generate-rest proto-sync openapi-sync record-source-commit check-generated tidy ide-install

build:
	go build -o $(BINARY) .

test:
	go test ./...

# Acceptance tests hit a real orq stack; require ORQ_URL + ORQ_API_KEY.
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
	@git diff --exit-code -- internal/gen internal/restgen \
		|| { echo "generated code is out of date; run 'make generate' and commit"; exit 1; }

tidy:
	go mod tidy

# --- codegen source sync (from the platform monorepo) ------------------------
# proto/ and openapi/openapi.json are COMMITTED COPIES. Re-sync then regenerate.
#
# Every sync stamps SOURCE_COMMIT with the exact orquesta-web git revision the
# copies were taken from, so drift between the committed proto/OpenAPI snapshot
# and the monorepo is auditable. Sync BOTH (`make proto-sync openapi-sync`) from
# the same checkout so the stamp is meaningful.

# record-source-commit writes the monorepo HEAD (with a dirty marker) into
# SOURCE_COMMIT. Called by both sync targets.
record-source-commit:
	@{ \
		rev=$$(git -C $(MONOREPO) rev-parse HEAD); \
		branch=$$(git -C $(MONOREPO) rev-parse --abbrev-ref HEAD); \
		dirty=$$(git -C $(MONOREPO) status --porcelain | head -1); \
		if [ -n "$$dirty" ]; then rev="$$rev (working tree dirty at sync time)"; fi; \
		printf 'orquesta-web %s\nbranch %s\nsynced %s\n' "$$rev" "$$branch" "$$(date -u +%Y-%m-%dT%H:%M:%SZ)" > SOURCE_COMMIT; \
		echo "recorded source commit $$rev"; \
	}

proto-sync: record-source-commit
	@set -e; \
	for f in projects budgets management_keys notifiers model_sharing api_keys identities sharing workspace_settings; do \
		cp $(MONOREPO)/apps/platform-api/proto/orq/platform/v1/$$f.proto proto/orq/platform/v1/; \
	done; \
	cp $(MONOREPO)/libs/catalog/orq/authz/v1/authz.proto proto/orq/authz/v1/; \
	cp $(MONOREPO)/libs/catalog/orq/apikeys/v1/catalog.proto proto/orq/apikeys/v1/; \
	cp $(MONOREPO)/libs/catalog/orq/managementkeys/v1/catalog.proto proto/orq/managementkeys/v1/; \
	cp $(MONOREPO)/apps/platform-api/proto/openapiv3/OpenAPIv3.proto proto/openapiv3/; \
	cp $(MONOREPO)/apps/platform-api/proto/openapiv3/annotations.proto proto/openapiv3/; \
	echo "synced protos from $(MONOREPO)"

openapi-sync: record-source-commit
	cp $(MONOREPO)/.openapi/v2/public/openapi.json openapi/openapi.json
	@echo "synced openapi.json from $(MONOREPO)"
