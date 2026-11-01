# definitions

## Purpose

Serves `GET /api/v1/types`: the five built-in types (movie, tv_show, book, vehicle, wishlist) with their fields, default icon, `allows_price` and default sort field. The types are code, not data: the registry lives in `internal/items/types/registry.go` and is shared by lists, items, search and lookup. There are no custom fields, no type or field writes and no database tables for them (F-023, D-105).

## Key files

- `internal/definitions/handler.go`: `Register`, the `listTypes` operation (no service, no repository)
- `internal/definitions/wire.go`: `TypeDefinitionJSON`, `TypeFieldJSON`, `typeJSON`
- `internal/items/types/registry.go`: `Def`, `Field`, `All()`, `Lookup(key)`

## Interfaces

- `Register(api huma.API, log *slog.Logger)`: mounts `GET /api/v1/types`
- `types.All() []Def`, `types.Lookup(key) (Def, bool)`: the registry; the composition root adapts it to `lists.TypeSource` and `items.TypeSource`

## Gotchas

- Field `position` is the index in the registry; the answer has no `is_locked` and no `version`, and no ETag.
- Item metadata accepts only the fields of the item's type (an unknown key is 400 `unknown_field`); lists sort and filter only on those fields or the generic sort modes.
- A new built-in field is a Go change in the registry; no migration or seeding is involved.
