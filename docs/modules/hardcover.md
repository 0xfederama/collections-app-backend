# hardcover

## Purpose

An optional, bounded, server-side Hardcover GraphQL client, used as a best-effort gap-filler for the automatic (title-only) book lookup in `internal/lookup`. Open Library stays primary. It is disabled without `HARDCOVER_API_KEY`. Design context lives in `/Users/federico/Developer/collections-app/design-docs/`.

## Key files

- `internal/integrations/hardcover/client.go`: ISBN and title search, cover download, pacing, retry, token handling
- `internal/lookup/hardcover.go`: `BookGapFiller` use, ISBN gap fill, title-only create, exact-title picker
- `internal/lookup/types.go`: `ModeCreateUntracked` (create without provenance)
- `cmd/api/main.go`: `lookupSvc.WithHardcover(hardcover.New(...))`

## Interfaces

- `New(token, contact string) *Client`, `NewWithOptions(Options)`: `Options` override the token, HTTP client, base URL, image origin, timeout and interval (tests)
- `(*Client).Enabled() bool`: false without a token (a nil client is disabled too)
- `(*Client).FindByISBN(ctx, isbn) (Book, bool, error)`: bare digits, `per_page` 3; a hit counts only if its `isbns` contain the ISBN or its ISBN-10/13 counterpart
- `(*Client).SearchTitle(ctx, title) ([]Book, error)`: `per_page` 5; the caller decides what is an exact match
- `(*Client).DownloadCover(ctx, url) ([]byte, error)`: only `https://assets.hardcover.app`, no redirects, no token sent
- `(*Service).WithHardcover(BookGapFiller)` in `internal/lookup`

## Gotchas

- Automatic book lookup only. Explicit `lookup: true` or `source_id` requests and the weekly refresh never use it.
- Gap fill: the chosen Open Library work lacks `author` or `pages` and has an ISBN, so one ISBN search fills only the missing field. It never overwrites, and never sets the title or cover.
- Title-only create: Open Library returned nothing or failed (not a cancel). Accepted only on an exact normalised match of the typed title against `title` or `alternative_titles`. The item keeps the typed title.
- A Hardcover-only match uses `ModeCreateUntracked`: no `lookup_imports` row (its provider CHECK allows only `tmdb` and `openlibrary`) and no `openlibrary_id`.
- Request: `POST` to the GraphQL endpoint with `authorization: Bearer <token>`. The `isbn:` prefix finds nothing, so bare digits are sent. A leading `Bearer ` and quotes in the configured value are stripped.
- 5 s timeout per call, one retry after 300 ms on network error or 5xx, 1 s minimum interval, 1 MiB response cap. A decode error counts as no result.
- Every failure is swallowed: WARN log, DEBUG when the context was cancelled. Errors and logs never contain the token or URL.
- `TMDB_MOCK_FIXTURE` enables no Hardcover fixture; tests use fake servers.
- Terms allow storing data for personal projects and hot-linking images. The API is in beta and the token expires; renew `HARDCOVER_API_KEY`.
