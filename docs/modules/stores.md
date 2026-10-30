# stores

## Purpose

Owns the fixed list of online store provider keys a `stores` field on books can name, and validates them. Metadata and workspace settings keep keys only. The backend builds no store links since API 0.17.0 (the web does). No I/O happens here. Design context lives in `/Users/federico/Developer/collections-app/design-docs/`.

## Key files

- `internal/stores/stores.go`: provider list (`Provider`), key validation
- `internal/stores/handler.go`, `wire.go`: `listStoreProviders` (Huma) and its JSON types
- `internal/items/types/types.go`: `stores` field value check via `ValidateKeys`
- `internal/items/service.go`: default store on item create (`seedPreferredStores`)
- `db/migrations/000001_init.up.sql`: the `stores` field type and `workspace_settings.preferred_store`

## Interfaces

- `Providers() []Provider`: fixed display order (copy); `Provider{Key, Name}`
- `Valid(key) bool`, `ValidateKeys(keys) error`: unknown or duplicate keys are rejected
- `Register(api huma.API, log)`: mounts `GET /api/v1/store-providers`
- `preferred_store` in `GET/PATCH /api/v1/settings`: one key, never null, default `amazon`

## Gotchas

- Fixed keys, in display order: `amazon`, `bookshop`, `apple_books`, `google_play_books`, `kobo`. Renaming one is a data migration. Migration 000023 stripped `openlibrary` from every item's stores.
- No store links in responses (removed in API 0.17.0). Sending `store_links` in `metadata` is `400 unknown_field`.
- The `stores` field is pinnable but never a sort key.
