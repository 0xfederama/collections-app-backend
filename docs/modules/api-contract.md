# api-contract

## Purpose

`api/openapi.yaml` is the canonical OpenAPI 3.0.3 contract for the HTTP API. Go models and server stubs are generated from it into `internal/httpapi/oapi`. F013 adds movie/TV TMDB suggestions and item lookup operations. Design context lives in `/Users/federico/Developer/collections-app/design-docs/`.

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
- `GET /api/v1/tmdb/suggestions`: ranked movie or TV matches using workspace `metadata_language`; disabled servers return `available: false`
- `POST /api/v1/items/{id}/lookup`: `fill`, `rematch`, or `refresh`; If-Match required
- `POST /api/v1/lists/{id}/items`: movie/TV creation may request lookup or select a `tmdb_id`
- Movie metadata: `director` is filterable, pinnable, and sortable; `metadata_language` accepts `xx` or `xx-YY`; `lookup_available` is read-only
- `TestSpecIsValid`: loads and validates `api/openapi.yaml`
- `TestSpecCoversAllAPIRoutes`: every API.md §5 route exists with a unique operationId

## Gotchas

- The strict server does not reject unknown JSON fields or limit body size. The spec promises `400 unknown_field`, so B-04 must enforce it.
- Default generated error handlers are text/plain. B-04 installs problem+json handlers.
- `If-Match` is `required: false` so a missing header reaches the handler, which returns 428.
- An unset `nullable.Nullable` in a response marshals as a zero value, not `null`.
- `nullable: true` must sit on the schema itself, not next to `allOf`.
- Item metadata is a `oneOf` discriminated by `kind`. Items have no `kind` in request bodies; it is the list's kind (F-007, spec 0.3.0).
- Spec 0.3.0 removed `default_currency`, item `summary`/`status`, and renamed `official_website_url` to `website`.
- Item creation can request TMDB lookup for movie/TV lists; missing matches preserve the typed item, but explicit lookup failures return 503.
