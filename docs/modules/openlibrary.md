# openlibrary

## Purpose

Provides a bounded, server-side Open Library client for book search, work details and cover downloads. It is the primary book `Provider` in `internal/lookup`. It needs no API key. Design context lives in `/Users/federico/Developer/collections-app/design-docs/`.

## Key files

- `internal/integrations/openlibrary/client.go`: search, work details, cover download, caching, pacing, User-Agent, fixture
- `internal/lookup/openlibrary_provider.go`: adapter onto the lookup `Provider` interface
- `internal/lookup/handler.go`, `wire.go`: `searchBookSuggestions` and its JSON type
- `cmd/api/main.go`: client construction (`NewWithOptions`)

## Interfaces

- `NewWithOptions(Options) *Client`: fixed `openlibrary.org` and `covers.openlibrary.org` origins. `Options` carry `Contact`, `Version`, `Disabled` and `MockFixture` (tests and E2E), plus HTTP and base-URL overrides
- `(*Client).Enabled() bool`: false only when disabled and no fixture is active
- `(*Client).Search(ctx, query, language) ([]Match, error)`: ranked works
- `(*Client).Work(ctx, workID, language) (Match, error)`: work details by `OL<n>W` id
- `(*Client).CoverURL(coverID int) string`, `DownloadCover(ctx, coverID int) ([]byte, error)`: bounded cover image; `ErrNoCover` when absent
- `GET /api/v1/books/suggestions`: results, or `available: false`

## Gotchas

- No key, always on. Optional `OPENLIBRARY_CONTACT` (an e-mail) goes into the User-Agent `CollectionsApp/1 (<contact>)` and paces at 600 ms (3 req/s); without it, requests are paced at 1.1 s (1 req/s).
- Bounded size, 8 s timeout, one retry after 300 ms for network errors, timeouts and 5xx (not 4xx/429, never past the caller's deadline). Searches are cached briefly, and each result is cached by work id, so `Work` after `Search` makes no second request.
- Only a cover download follows redirects (Open Library answers 302 to archive.org): at most 3 hops, `https` to `archive.org` or `*.archive.org` only. JSON requests never redirect.
- `TMDB_MOCK_FIXTURE=true` enables the fixture: the exact query `[mock] book name` returns work `OL900001W` without HTTP, even when disabled.
- Open Library data has no retention duty (D-095); it is kept until the user changes it.
- A missing or failed cover keeps the metadata and imports without an image (Warn log).
- Settings expose availability as read-only `book_lookup_available`.
- The adapter fills `author`, `pages` and `isbn` only. `openlibrary_id` is stored and read-only by convention; the web builds the link from it.
