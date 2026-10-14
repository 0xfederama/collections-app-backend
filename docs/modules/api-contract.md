# api-contract

## Purpose

`api/openapi.yaml` is the canonical OpenAPI 3.0.3 contract (37 operations, tagged health, settings, tree, folders, lists, items, image, content, search, trash). Go models and server stubs are generated from it into `internal/httpapi/oapi`. The contract is not mounted in the router yet (B-04). Design context lives in `/Users/federico/Developer/collections-app/design-docs/`.

## Key files

- `api/openapi.yaml`: the contract; examples feed the web MSW mocks
- `oapi-codegen.yaml`: generator config (models, chi-server, strict-server, nullable-type)
- `internal/httpapi/oapi/oapi.gen.go`: generated code, committed, never edit
- `internal/httpapi/oapi/spec_test.go`: spec validity and route coverage tests (kin-openapi)
- `redocly.yaml`: Redocly lint rules (examples must be valid)

## Interfaces

- `make generate`: runs sqlc, then oapi-codegen (`go tool oapi-codegen`)
- `make lint-api`: Redocly CLI 2.63.0 via npx (needs Node >= 24)
- `make check-generate`: fails if regenerating changes tracked generated files
- `StrictServerInterface`: one method per operationId, generated in `oapi.gen.go`
- `TestSpecIsValid`: loads and validates `api/openapi.yaml`
- `TestSpecCoversAllAPIRoutes`: every API.md §5 route exists with a unique operationId

## Gotchas

- The strict server does not reject unknown JSON fields or limit body size. The spec promises `400 unknown_field`, so B-04 must enforce it.
- Default generated error handlers are text/plain. B-04 installs problem+json handlers.
- `If-Match` is `required: false` so a missing header reaches the handler, which returns 428.
- An unset `nullable.Nullable` in a response marshals as a zero value, not `null`.
- `nullable: true` must sit on the schema itself, not next to `allOf`.
- Item metadata is a `oneOf` discriminated by `kind`.
