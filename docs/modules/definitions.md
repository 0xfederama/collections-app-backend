# definitions

## Purpose

Manages workspace-scoped type definitions and their fields. Built-in types (movie, tv_show, book, vehicle, wishlist; there is no `generic`, a list with no type has `type` null) and fields are seeded locked; custom types and their defaults are editable.

## Key files

- `internal/definitions/service.go`: definition and field mutations
- `db/queries/types.sql`: workspace-scoped definition persistence
- `internal/httpapi/types.go`: type-management handlers and ETags
- `db/migrations/000014_kind_definitions.up.sql`: tables and built-in seed function
- `db/migrations/000021_book_type.up.sql`: built-in `book` type, seed and default pins
- `db/migrations/000025_type_refactor.{up,down}.sql`: built-in set reduced to five plural-named types (API 0.16.0)

## Interfaces

- `New(db DB, workspaceID uuid.UUID) *Service`
- `List(ctx) ([]Type, error)`; `Create(ctx, CreateType) (Type, error)`
- `Update(ctx, key, version, UpdateType) (Type, error)`: updates supplied custom-type name, icon, price setting, and defaults
- `Delete(ctx, key, version) error`
- `CreateField`, `RenameField`, `ReorderFields`, `FieldDeletePreview`, `DeleteField`
- `Type`: key, name, price setting, defaults, lock state, version, and fields
- `Field`: key, label, type, position, lock state, and version

## Gotchas

- Keys derive from names/labels and use lowercase letters, digits, and underscores. `store_links` is a reserved field key.
- Locked built-ins cannot be updated or deleted; a type in an active list cannot be deleted.
- A custom type's optional default icon uses the same one-emoji or `i:<name>` grammar as list icons. `UpdateType` distinguishes an omitted default from an explicit null.
- A default sort or pin must name an existing field of the type; `price` is permitted only when the type allows prices. Defaults may be updated after fields are created.
- Default pins hold at most three unique keys (migration 000019 CHECK). A `stores` field is pinnable but never a sort key.
- `stores` is a locked-only field type (DB CHECK); custom fields cannot use it.
- The built-in `book` has no first publication year since migration 000023 (6 fields, default sort manual/NULL, default pins `author, pages, stores`); a custom `book_custom` is untouched.
- Migration 000021 renames a custom type keyed `book` to `book_custom` (`book_custom_2`, ... when taken) before the built-in takes the key; its lists follow.
- Built-in names are plural (Movies, TV shows, Books, Vehicles, Wishlist). Built-in `vehicle` (`i:car`) has make, model, model year, displacement and power, no price; built-in `wishlist` (`i:tag`) is the only built-in that allows price.
- Migration 000025 removes the built-in motorcycle, car and camera types, renames a custom type keyed `vehicle` or `wishlist` to `<key>_custom` (lists follow), and discards prices on vehicle and book items.
- Migration 000024 relabels the built-in movie `runtime_minutes` to "Runtime" (only from the seeded "Runtime (minutes)"; key unchanged, a customised label is kept).
- Reorder accepts exactly the custom-field keys; locked fields retain their seed positions.
- Deleting a custom field clears matching metadata, resets affected defaults/pins/sorts, and includes trashed items in its count.
