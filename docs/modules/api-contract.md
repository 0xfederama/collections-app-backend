# api-contract

## Purpose

`api/openapi.yaml` (OpenAPI 3.1.0, `info.version` 1.0.0) is generated from the Huma operations, not written by hand: the handler input and output types and their registrations are the contract. `make spec` renders it, `make check-spec` fails when the committed file is stale. Design context: `/Users/federico/Developer/collections-app/design-docs/`.

## Key files

- `api/openapi.yaml`: generated contract, never edit by hand
- `cmd/specgen/main.go`: prints `httpapi.Spec()`
- `internal/httpapi/spec.go`: info description (conventions, ETags, errors, limits) and tags
- `internal/platform/problem/codes.go`: error code list, rendered into the `Problem` schema
- `docs/api-notes.md`: constraints and error statuses the generated spec leaves out
- `redocly.yaml`: Redocly lint config

## Interfaces

- `make spec`: rewrites `api/openapi.yaml` from `cmd/specgen`
- `make check-spec`: fails with a diff when the committed spec is stale (in `make check`)
- `make lint-api`: Redocly CLI 2.63.0 via `npx` (Node >= 24), in `make check`
- `TestSpecIsValid`, `TestSpecMatchesRegistrations`, `TestSpecCoversAllAPIRoutes`: `internal/httpapi/spec_test.go`

## Gotchas

- To change the contract, change the handler types or registration, run `make spec`, and commit the handler and the spec together.
- Keep `operationId`s stable across changes.
- Constraints such as `maxLength`, `pattern` or `maxItems` are enforced in services, not in the spec (see `docs/api-notes.md`).
- The document declares no `servers`; the Redocly rule `no-empty-servers` is off for that reason.
- The generated spec has no examples.
