# stores

## Purpose

Owns the fixed list of online stores a `stores` field on books can name, and builds their public links at read time from an item's ISBN, title or Open Library id. Metadata keeps only provider keys; no I/O happens here. Design context lives in `/Users/federico/Developer/collections-app/design-docs/`.

## Key files

- `internal/stores/stores.go`: provider list, key validation, `Link` builder
- `internal/items/stores.go`: `store_links` computation for item reads
- `internal/httpapi/stores.go`: `GET /store-providers` handler
- `db/migrations/000020_preferred_stores.up.sql`: `workspace_settings.preferred_stores`

## Interfaces

- `Providers() []Provider`: fixed display order (copy)
- `Valid(key) bool`, `ValidateKeys(keys) error`: unknown or duplicate keys are rejected
- `Link(provider, LinkInput) string`: ISBN first, then title; `""` when nothing to search. `LinkInput.Country` (the workspace `streaming_country_code`) picks regional sites
- `AmazonDomain(country) string`: one table (same as the web's `AMAZON_DOMAINS`), `amazon.com` for any other country
- `GET /api/v1/store-providers`: the fixed list, independent of the workspace
- `preferred_stores` in `GET/PATCH /api/v1/settings`: at most six unique keys

## Gotchas

- Fixed keys: `openlibrary`, `bookshop`, `amazon`, `apple_books`, `google_play_books`, `kobo`. Renaming one is a data migration.
- `store_links` is read-only and computed on read; sending it in `metadata` is `400 unknown_field`.
- Country-aware: Amazon (table), Bookshop.org (UK site for GB), Google Play (`gl=`); Open Library, Apple Books and Kobo stay global (no verifiable regional search URL). Links are computed on read, so a later country change moves them.
- Links are always `https`, every value is URL-escaped, and hyphens and spaces are stripped from ISBNs.
- Open Library links use the work id first, then ISBN, then a title search.
- The `stores` field is pinnable but never a sort key.
