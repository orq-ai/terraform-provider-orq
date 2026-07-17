# terraform-provider-orq — local development Makefile.
#
# Codegen is reproducible and MUST NOT be hand-edited:
#   - Connect clients: buf generate (config: buf.gen.yaml) from proto/
#   - REST client:     oapi-codegen (config: openapi/oapi-codegen.yaml) from openapi/openapi.json
# CI runs `make generate check-generated` so a stale/edited generated tree fails the build.

BINARY  := terraform-provider-orq
# Sync sources in the platform monorepo (adjust MONOREPO if checked out elsewhere).
MONOREPO ?= ../orquesta-web

.PHONY: build test testacc generate proto-sync openapi-sync check-generated tidy

build:
	go build -o $(BINARY) .

test:
	go test ./...

# Acceptance tests hit a real orq stack; require ORQ_URL + ORQ_TOKEN.
testacc:
	TF_ACC=1 go test ./... -v -timeout 120m

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

proto-sync:
	@set -e; \
	for f in projects budgets management_keys notifiers model_sharing api_keys identities sharing; do \
		cp $(MONOREPO)/apps/platform-api/proto/orq/platform/v1/$$f.proto proto/orq/platform/v1/; \
	done; \
	cp $(MONOREPO)/libs/catalog/orq/authz/v1/authz.proto proto/orq/authz/v1/; \
	cp $(MONOREPO)/libs/catalog/orq/apikeys/v1/catalog.proto proto/orq/apikeys/v1/; \
	cp $(MONOREPO)/libs/catalog/orq/managementkeys/v1/catalog.proto proto/orq/managementkeys/v1/; \
	cp $(MONOREPO)/apps/platform-api/proto/openapiv3/OpenAPIv3.proto proto/openapiv3/; \
	cp $(MONOREPO)/apps/platform-api/proto/openapiv3/annotations.proto proto/openapiv3/; \
	echo "synced protos from $(MONOREPO)"

openapi-sync:
	cp $(MONOREPO)/.openapi/v2/public/openapi.json openapi/openapi.json
	@echo "synced openapi.json from $(MONOREPO)"
