# hardcover

## Purpose

An optional, bounded, server-side Hardcover GraphQL client used as a gap-filler for the automatic (title only, best-effort) book lookup in `internal/lookup`. Open Library stays primary. It is disabled without `HARDCOVER_API_KEY`. Design context lives in `/Users/federico/Developer/collections-app/design-docs/`.

## Key files

- `internal/integrations/hardcover/client.go`: search by ISBN or title, cover download, pacing, retry, token handling
- `internal/lookup/hardcover.go`: `BookGapFiller` interface, ISBN gap fill, title-only create, exact-title picker
- `internal/lookup/store.go`: `ModeCreateUntracked` (create without provenance)
- `cmd/api/main.go`: `lookupSvc.WithHardcover(hardcover.New(...))`

## Interfaces

- `New(token, contact string) *Client`, `NewWithOptions(Options)` (base URL, image origin, timeout and interval overrides for tests)
- `(*Client).Enabled() bool`: false without a token (a nil client is disabled too)
- `(*Client).FindByISBN(ctx, isbn) (Book, bool, error)`: searches the bare digits (`per_page` 3); a hit counts only if its `isbns` contain the ISBN or its ISBN-10/13 counterpart
- `(*Client).SearchTitle(ctx, title) ([]Book, error)`: `per_page` 5, the caller decides what is an exact match
- `(*Client).DownloadCover(ctx, url) ([]byte, error)`: only `https://assets.hardcover.app`, no redirects, no token sent
- `Service.WithHardcover(BookGapFiller)`

## Behaviour (books only, automatic path only)

- Gap fill: the chosen Open Library work lacks `author` or `pages` and has an `isbn` -> one ISBN search; only the missing field is filled from `author_names` / `pages`. Never overwrites, never the title or cover. Also runs for item lookup fill/rematch without a selected id.
- Title-only create: Open Library returned no results at all, or failed (not a cancel), on an automatic create -> `SearchTitle`; accepted only when the normalised typed title equals the normalised `title` or an `alternative_titles` entry (the most complete hit wins, ties earliest). Author, pages, first valid ISBN and cover are applied; the item keeps the typed title and the default-store fill still applies.
- A Hardcover-only match is stored with `ModeCreateUntracked`: no `lookup_imports` row (its provider CHECK only allows `tmdb`/`openlibrary`) and no `openlibrary_id`, so later refresh needs a selected Open Library match. No migration.
- Explicit `lookup: true` / `source_id` requests, refresh and retention never use Hardcover.

## Gotchas

- Request: `POST https://api.hardcover.app/v1/graphql`, `authorization: Bearer <token>`, `search(query:, query_type:"Book", per_page:, page:1){ results }`. The `isbn:` prefix finds nothing, so the bare digits are sent. A leading `Bearer ` and quotes in the configured value are stripped.
- 5 s timeout per call (their server limit is 2 s), one retry after 300 ms on network error or 5xx, 1 s minimum interval (free tier 60/min), 1 MiB response cap. A decode error is "no result".
- Every failure (no key, 401/403/429/5xx, timeout, bad JSON) is swallowed: WARN log, DEBUG for a cancelled context; errors and logs never contain the token or URL.
- User-Agent `CollectionsApp/1 (<OPENLIBRARY_CONTACT>)` like the Open Library client.
- `TMDB_MOCK_FIXTURE` enables no Hardcover fixture; tests use fake servers.
- Hardcover's terms allow storing its data for personal projects and hot-linking images. The API is in beta and the token expires (renew `HARDCOVER_API_KEY`).
