# api-contract

## Purpose

`api/openapi.yaml` is the canonical OpenAPI 3.0.3 contract. It defines generated Go models and strict-server stubs, including workspace kind-definition management. Design context: `/Users/federico/Developer/collections-app/design-docs/`.

## Key files

- `api/openapi.yaml`: contract, kind and field schemas, examples
- `oapi-codegen.yaml`: generator config (models, chi-server, strict-server, nullable-type)
- `internal/httpapi/oapi/oapi.gen.go`: generated code, committed, never edit
- `internal/httpapi/oapi/spec_test.go`: spec validity and route coverage tests (kin-openapi)
- `redocly.yaml`: Redocly lint rules (examples must be valid)

## Interfaces

- `make generate`: runs sqlc, then oapi-codegen (`go tool oapi-codegen`)
- `make lint-api`: Redocly CLI 2.63.0 via npx (needs Node >= 24)
- `make check-generate`: fails if regenerating changes tracked generated files
- `StrictServerInterface`: one method per operationId, generated in `oapi.gen.go`
- `GET/POST /api/v1/kinds`: list definitions; create a custom kind
- `PATCH /api/v1/kinds/{key}`: update supplied custom-kind name, `default_icon`, `allows_price`, default sort, or default pins (If-Match)
- `DELETE /api/v1/kinds/{key}`: delete an unused custom kind (If-Match)
- `POST /api/v1/kinds/{key}/fields`, `PUT .../fields/order`: add or reorder custom fields (If-Match)
- `PATCH/GET/DELETE .../fields/{fieldKey}`: rename, preview deletion, or delete a custom field
- `GET /api/v1/lists/{id}/items`: sort by a kind field and filter with `filter_field`, `filter_value`, `filter_min`, `filter_max`
- `TestSpecIsValid`: loads and validates `api/openapi.yaml`
- `TestSpecCoversAllAPIRoutes`: every API.md §5 route exists with a unique operationId

## Gotchas

- The strict server does not reject unknown JSON fields or limit body size. The spec promises `400 unknown_field`, so B-04 must enforce it.
- Default generated error handlers are text/plain. B-04 installs problem+json handlers.
- `If-Match` is `required: false` so a missing header reaches the handler, which returns 428.
- An unset `nullable.Nullable` in a response marshals as a zero value, not `null`.
- `nullable: true` must sit on the schema itself, not next to `allOf`.
- Kind and field versions have independent ETag streams; all mutations except creation require If-Match.
- Kind create and update responses preserve `default_icon`; PATCH fields are optional and nullable defaults may be cleared with JSON `null`.
- Item metadata is an object validated against its list kind's definition; the item `kind` is read-only.
