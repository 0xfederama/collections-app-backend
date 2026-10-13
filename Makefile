# Local quality gates (no CI, D-031). Run `make check` before every PR.
GOLANGCI_LINT := go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0
MIGRATE_IMAGE := migrate/migrate:v4.20.1

.PHONY: run test lint fmt fmt-check check check-compose check-docker audit migrate migration generate reset-dev

run: ## Run the API on the host (needs `docker compose up -d db` + compose.override.yaml, see README)
	@test -f .env || { echo "missing .env: cp .env.example .env"; exit 1; }
	@mkdir -p data
	set -a && . ./.env && set +a && \
	DATABASE_URL="$${DATABASE_URL:-postgres://localhost:5432/collection?user=collection&password=$$POSTGRES_PASSWORD&sslmode=disable}" go run ./cmd/api

test: ## Go tests (need Docker for testcontainers)
	go test ./...

lint:
	$(GOLANGCI_LINT) run ./...

fmt:
	gofmt -w .

fmt-check:
	@out="$$(gofmt -l .)"; if [ -n "$$out" ]; then echo "gofmt needed on:"; echo "$$out"; exit 1; fi

check-compose: ## Fail if compose publishes any port outside 127.0.0.1 (AT-18)
	./scripts/check-compose-exposure.sh

check-docker: ## Build the runtime image (catches Dockerfile drift, e.g. a new top-level Go dir not copied)
	docker build -q -t collections-app-backend:check . >/dev/null && echo "docker build ok"

check: fmt-check lint test check-compose check-docker ## All local gates

audit: ## Vulnerability scan (needs network, not part of check)
	go tool govulncheck ./...

migrate: ## Apply migrations through the compose migrate service
	docker compose run --rm migrate

migration: ## Create a migration pair: make migration name=create_items
	@test -n "$(name)" || { echo "usage: make migration name=<snake_case>"; exit 1; }
	docker run --rm --user "$$(id -u):$$(id -g)" -v "$(CURDIR)/db/migrations:/migrations" $(MIGRATE_IMAGE) create -ext sql -dir /migrations -seq $(name)

generate: ## No-op until B-02 (sqlc) and B-03 (oapi-codegen) wire their generators and drift checks
	@echo "nothing to generate yet (sqlc arrives in B-02, oapi-codegen in B-03)"

reset-dev: ## Delete the dev containers and volumes (asks first, refuses in production)
	./scripts/reset-dev.sh
