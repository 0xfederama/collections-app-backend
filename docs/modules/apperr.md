# apperr

## Purpose

Typed domain errors that services return and that `internal/httpapi` maps to problem+json. The package has no HTTP imports, so services stay transport-agnostic (ARCHITECTURE §2, API.md §4).

## Key files

- `internal/apperr/apperr.go`: error types, constructors, `Code` constants

## Interfaces

- `NotFound(resource string) error`: missing or trashed resource (`*NotFoundError`)
- `VersionConflict(current int) error`: stale If-Match; carries `Current` (`*VersionConflictError`)
- `Validation(errs ...FieldError) error`: failed field rules (`*ValidationError`)
- `UnknownField(path string) error`: undeclared key at a dotted path, e.g. `metadata.runtime` (`*UnknownFieldError`, 400 `unknown_field`)
- `Cycle() error`: 409 `cycle_detected`
- `ParentInTrash(b BlockingParent) error`: 409 `parent_in_trash` with the blocking ancestor
- `DestinationUnavailable() error`: 409 `destination_unavailable`
- `TypeMismatch() error`: 409 `type_mismatch` (item moved to a list of another type)
- `OrderStale() error`: 409 `order_stale`
- `Forbidden() error`: 403 `forbidden` (`*ForbiddenError`): the caller sees the resource but only its owner may do this
- `FolderNotTopLevel() error`: 409 `folder_not_top_level`; `FolderShared() error`: 409 `folder_shared`
- `DuplicateTitle() error`: 409 `duplicate_title` (items create/update/move, lookup enriched create)
- `PayloadTooLarge(limit int64) error`: over-limit body or upload
- `UnsupportedMediaType(detail string) error`: upload not JPEG, PNG or WebP
- `FaviconNotFound() error`: site without a usable favicon (404 `favicon_not_found`, `*FaviconNotFoundError`)

## Gotchas

- Inspect with `errors.As` for the typed errors.
- `Validation` should get at least one `FieldError`.
- Only the `CodeParentInTrash` conflict carries `Blocking`; the other codes leave it nil.
- Messages here are for logs. Clients never see them: `httpapi` maps each type to a fixed title and detail.
- Add new error kinds here and map them once in `platform/problem/problem.go`.
