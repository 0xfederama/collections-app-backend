# definitions

## Purpose

Manages workspace-scoped kind definitions and their fields. Built-in kinds and fields are seeded locked; custom kinds and their defaults are editable.

## Key files

- `internal/definitions/service.go`: definition and field mutations
- `db/queries/kinds.sql`: workspace-scoped definition persistence
- `internal/httpapi/kinds.go`: kind-management handlers and ETags
- `db/migrations/000014_kind_definitions.up.sql`: tables and built-in seed function

## Interfaces

- `New(db DB, workspaceID uuid.UUID) *Service`
- `List(ctx) ([]Kind, error)`; `Create(ctx, CreateKind) (Kind, error)`
- `Update(ctx, key, version, UpdateKind) (Kind, error)`: updates supplied custom-kind name, icon, price setting, and defaults
- `Delete(ctx, key, version) error`
- `CreateField`, `RenameField`, `ReorderFields`, `FieldDeletePreview`, `DeleteField`
- `Kind`: key, name, price setting, defaults, lock state, version, and fields
- `Field`: key, label, type, position, lock state, and version

## Gotchas

- Keys derive from names/labels and use lowercase letters, digits, and underscores.
- Locked built-ins cannot be updated or deleted; a kind in an active list cannot be deleted.
- A custom kind's optional default icon uses the same one-emoji or `i:<name>` grammar as list icons. `UpdateKind` distinguishes an omitted default from an explicit null.
- A default sort or pin must name an existing field of the kind; `price` is permitted only when the kind allows prices. Defaults may be updated after fields are created.
- Reorder accepts exactly the custom-field keys; locked fields retain their seed positions.
- Deleting a custom field clears matching metadata, resets affected defaults/pins/sorts, and includes trashed items in its count.
