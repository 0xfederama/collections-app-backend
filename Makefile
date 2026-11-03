# Local quality gates (no CI, D-031). Run `make check` before every PR.
GOLANGCI_LINT := go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0
MIGRATE_IMAGE := migrate/migrate:v4.20.1
REDOCLY_VERSION := 2.63.0
SHELLCHECK_IMAGE := koalaman/shellcheck:v0.11.0

.PHONY: run test lint lint-api spec check-spec fmt fmt-check check check-generate check-compose check-docker lint-scripts test-backup smoke audit deadcode migrate migration generate reset-dev

run: ## Run the API on the host (needs `docker compose up -d db` + compose.override.yaml, see README)
	@test -f .env || { echo "missing .env: cp .env.example .env"; exit 1; }
	@mkdir -p data
	set -a && . ./.env && set +a && \
	DATABASE_URL="$${DATABASE_URL:-postgres://localhost:5432/collection?user=collection&password=$$POSTGRES_PASSWORD&sslmode=disable}" go run ./cmd/api

test: ## Go tests (need Docker for testcontainers)
	go test ./...

lint:
	$(GOLANGCI_LINT) run ./...

lint-api: ## Lint api/openapi.yaml with Redocly (needs Node >= 24, see README); config in redocly.yaml
	npx --yes @redocly/cli@$(REDOCLY_VERSION) lint api/openapi.yaml

spec: ## Write api/openapi.yaml, generated from the Huma operations (cmd/specgen)
	go run ./cmd/specgen > api/openapi.yaml.tmp && mv api/openapi.yaml.tmp api/openapi.yaml

check-spec: ## Fail if api/openapi.yaml is not the generated spec (prints the diff)
	@tmp="$$(mktemp)"; \
	go run ./cmd/specgen > "$$tmp" || { rm -f "$$tmp"; exit 1; }; \
	if ! diff -u api/openapi.yaml "$$tmp"; then rm -f "$$tmp"; echo "api/openapi.yaml is stale, run 'make spec' and commit it"; exit 1; fi; \
	rm -f "$$tmp"; echo "spec ok"

fmt:
	gofmt -w .

fmt-check:
	@out="$$(gofmt -l .)"; if [ -n "$$out" ]; then echo "gofmt needed on:"; echo "$$out"; exit 1; fi

check-compose: ## Fail if compose publishes any port outside 127.0.0.1 (AT-18)
	./scripts/check-compose-exposure.sh

check-docker: ## Build the runtime image (catches Dockerfile drift, e.g. a new top-level Go dir not copied)
	docker build -q -t collections-app-backend:check . >/dev/null && echo "docker build ok"

lint-scripts: ## shellcheck every script through its pinned Docker image
	docker run --rm -v "$(CURDIR)/scripts:/mnt/scripts:ro" -w /mnt $(SHELLCHECK_IMAGE) -x -P SCRIPTDIR scripts/*.sh scripts/lib/*.sh
	@echo "shellcheck ok"

test-backup: ## AT-17 backup/restore round trip on an isolated compose project (Docker, jq, curl, nc; not part of check)
	./scripts/test-backup.sh

smoke: ## API smoke test over HTTP (curl, jq) against a running stack; API_URL defaults to http://localhost:8090 (./deploy/dev up -d --fresh; never ./deploy/test); not part of check
	./scripts/smoke.sh

check: fmt-check lint check-spec lint-api lint-scripts check-generate test check-compose check-docker ## All local gates

audit: ## Vulnerability scan (needs network, not part of check)
	go tool govulncheck ./...

deadcode: ## Report unreachable functions, tests included (not part of check; unused types/consts are golangci's unused linter)
	go tool deadcode -test ./...

migrate: ## Apply migrations through the compose migrate service (the API image: /app/api migrate)
	docker compose run --rm migrate

migration: ## Create a migration pair: make migration name=create_items
	@test -n "$(name)" || { echo "usage: make migration name=<snake_case>"; exit 1; }
	docker run --rm --user "$$(id -u):$$(id -g)" -v "$(CURDIR)/db/migrations:/migrations" $(MIGRATE_IMAGE) create -ext sql -dir /migrations -seq $(name)

generate: ## Regenerate code: sqlc (db/queries)
	go tool sqlc generate

check-generate: ## Fail if regenerating changes (or adds) tracked generated files (D-019)
	@$(MAKE) --no-print-directory generate
	@out="$$(git status --porcelain -- internal/platform/database/sqlc db/queries sqlc.yaml)"; \
	if [ -n "$$out" ]; then echo "generated code is stale or uncommitted, run 'make generate' and commit:"; echo "$$out"; exit 1; fi

reset-dev: ## Delete the dev containers and volumes (asks first, refuses in production)
	./scripts/reset-dev.sh
