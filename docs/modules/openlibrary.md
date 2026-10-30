# openlibrary

## Purpose

Provides a bounded, server-side Open Library client for book search, work details and cover downloads, used as the book `Provider` in `internal/lookup`. It needs no API key. Design context lives in `/Users/federico/Developer/collections-app/design-docs/`.

## Key files

- `internal/integrations/openlibrary/client.go`: search, work details, cover download, caching, pacing, User-Agent, fixture
- `internal/lookup/openlibrary_provider.go`: adapter onto the lookup `Provider` interface
- `internal/httpapi/books.go`: `GET /books/suggestions` mapping
- `cmd/api/main.go`: client construction

## Interfaces

- `New(contact string, disabled bool) *Client`: fixed `openlibrary.org` and `covers.openlibrary.org` origins
- `(*Client).Enabled() bool`: false only when disabled and no fixture is active
- `(*Client).Search(ctx, query, language) ([]Match, error)`: ranked works
- `(*Client).Work(ctx, workID, language) (Match, error)`: work details by `OL<n>W` id
- `(*Client).DownloadCover(ctx, coverID) ([]byte, error)`: bounded cover image; `ErrNoCover` when absent
- `GET /api/v1/books/suggestions`: returns results or `available: false`

## Gotchas

- No key, always on. Optional `OPENLIBRARY_CONTACT` (an e-mail) goes into the User-Agent `CollectionsApp/1 (<contact>)` and paces at 600 ms (3 req/s limit); without it requests are unidentified and paced at 1.1 s (1 req/s limit).
- Requests are paced below the identified-client limit (3/s), bounded in size, 8 s timeout, and one retry after 300 ms for network errors, timeouts and 5xx (not 4xx/429, never past the caller's deadline). Searches are cached briefly, each result also by work id, so `Work` after `Search` makes no second request.
- Only a cover download follows redirects (Open Library answers 302 to archive.org): at most 3, https to `archive.org`/`*.archive.org` only. JSON requests never redirect.
- `TMDB_MOCK_FIXTURE=true` enables the fixture: the exact query `[mock] book name` returns work `OL900001W` without HTTP, even when disabled. Other queries behave normally.
- Open Library data has no retention duty (D-095); it is kept until the user changes it.
- Covers may be absent (`ErrNoCover`) or fail to download: the lookup keeps the metadata and imports without an image (Warn log).
- Settings expose this as read-only `book_lookup_available`.
- The adapter fills `author`, `pages` and `isbn` only (the first publication year was removed from the book type in 0.13.0; a suggestion's `year` is display only). `openlibrary_id` is stored and read-only by convention; the web builds the Open Library link from it.
