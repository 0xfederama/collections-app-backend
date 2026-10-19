# definitions

## Purpose

Manages workspace-scoped type definitions and their fields. Built-in types and fields are seeded locked; custom types and their defaults are editable.

## Key files

- `internal/definitions/service.go`: definition and field mutations
- `db/queries/types.sql`: workspace-scoped definition persistence
- `internal/httpapi/types.go`: type-management handlers and ETags
- `db/migrations/000014_kind_definitions.up.sql`: tables and built-in seed function

## Interfaces

- `New(db DB, workspaceID uuid.UUID) *Service`
- `List(ctx) ([]Type, error)`; `Create(ctx, CreateType) (Type, error)`
- `Update(ctx, key, version, UpdateType) (Type, error)`: updates supplied custom-type name, icon, price setting, and defaults
- `Delete(ctx, key, version) error`
- `CreateField`, `RenameField`, `ReorderFields`, `FieldDeletePreview`, `DeleteField`
- `Type`: key, name, price setting, defaults, lock state, version, and fields
- `Field`: key, label, type, position, lock state, and version

## Gotchas

- Keys derive from names/labels and use lowercase letters, digits, and underscores.
- Locked built-ins cannot be updated or deleted; a type in an active list cannot be deleted.
- A custom type's optional default icon uses the same one-emoji or `i:<name>` grammar as list icons. `UpdateType` distinguishes an omitted default from an explicit null.
- A default sort or pin must name an existing field of the type; `price` is permitted only when the type allows prices. Defaults may be updated after fields are created.
- Reorder accepts exactly the custom-field keys; locked fields retain their seed positions.
- Deleting a custom field clears matching metadata, resets affected defaults/pins/sorts, and includes trashed items in its count.
