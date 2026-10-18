# definitions

## Purpose

Manages workspace-scoped kind definitions and their fields. Built-in kinds and fields are seeded locked; custom kinds and fields are editable.

## Key files

- `internal/definitions/service.go`: definition and field mutations
- `db/queries/kinds.sql`: workspace-scoped definition persistence
- `internal/httpapi/kinds.go`: kind-management handlers and ETags
- `db/migrations/000014_kind_definitions.up.sql`: tables and built-in seed function

## Interfaces

- `New(db DB, workspaceID uuid.UUID) *Service`
- `List(ctx) ([]Kind, error)`; `Create(ctx, CreateKind) (Kind, error)`
- `Rename(ctx, key, version, name) (Kind, error)`; `Delete(ctx, key, version) error`
- `CreateField`, `RenameField`, `ReorderFields`, `FieldDeletePreview`, `DeleteField`
- `Kind`: key, name, price setting, defaults, lock state, version, and fields
- `Field`: key, label, type, position, lock state, and version

## Gotchas

- Keys derive from names/labels and use lowercase letters, digits, and underscores.
- Locked built-ins cannot be renamed or deleted; a kind in an active list cannot be deleted.
- Reorder accepts exactly the custom-field keys; locked fields retain their seed positions.
- Deleting a custom field clears matching metadata, resets affected defaults/pins/sorts, and includes trashed items in its count.
