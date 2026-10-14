# apperr

## Purpose

Typed domain errors that services return and that `internal/httpapi` maps to problem+json. The package has no HTTP imports, so services stay transport-agnostic (ARCHITECTURE §2, API.md §4).

## Key files

- `internal/apperr/apperr.go`: error types, constructors, `Code` constants

## Interfaces

- `NotFound(resource string) error`: missing or trashed resource (`*NotFoundError`)
- `VersionConflict(current int) error`: stale If-Match; carries `Current` (`*VersionConflictError`)
- `Validation(errs ...FieldError) error`: failed field rules (`*ValidationError`)
- `Cycle() error`: 409 `cycle_detected`
- `ParentInTrash(b BlockingParent) error`: 409 `parent_in_trash` with the blocking ancestor
- `DestinationUnavailable() error`: 409 `destination_unavailable`
- `OrderStale() error`: 409 `order_stale`
- `PayloadTooLarge(limit int64) error`: over-limit body or upload
- `UnsupportedMediaType(detail string) error`: upload not JPEG, PNG or WebP
- `NotImplemented() error` and `ErrNotImplemented`: sentinel for 501 stubs

## Gotchas

- Inspect with `errors.As` for the typed errors and `errors.Is` for `ErrNotImplemented`.
- `Validation` should get at least one `FieldError`.
- Only the `CodeParentInTrash` conflict carries `Blocking`; the other codes leave it nil.
- Messages here are for logs. Clients never see them: `httpapi` maps each type to a fixed title and detail.
- Add new error kinds here and map them once in `httpapi/problem.go`.
