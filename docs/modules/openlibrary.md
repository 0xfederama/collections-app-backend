# openlibrary

## Purpose

Provides a bounded, server-side Open Library client for book search, work details and cover downloads, used as the book `Provider` in `internal/lookup`. It needs no API key. Design context lives in `/Users/federico/Developer/collections-app/design-docs/`.

## Key files

- `internal/openlibrary/client.go`: search, work details, cover download, caching, pacing, User-Agent, fixture
- `internal/lookup/openlibrary_provider.go`: adapter onto the lookup `Provider` interface
- `internal/httpapi/books.go`: `GET /books/suggestions` mapping
- `internal/config/config.go`: `OPENLIBRARY_CONTACT` and `OPENLIBRARY_DISABLED`
- `cmd/api/main.go`: client construction

## Interfaces

- `New(contact string, disabled bool) *Client`: fixed `openlibrary.org` and `covers.openlibrary.org` origins
- `(*Client).Enabled() bool`: false only when disabled and no fixture is active
- `(*Client).Search(ctx, query, language) ([]Match, error)`: ranked works
- `(*Client).Work(ctx, workID, language) (Match, error)`: work details by `OL<n>W` id
- `(*Client).DownloadCover(ctx, coverID) ([]byte, error)`: bounded cover image; `ErrNoCover` when absent
- `GET /api/v1/books/suggestions`: returns results or `available: false`

## Gotchas

- No key. `OPENLIBRARY_CONTACT` (an e-mail or URL) goes into the User-Agent; `OPENLIBRARY_DISABLED=true` makes no request at all.
- Requests are paced below the identified-client limit (3/s), bounded in size and timeout, and searches are cached briefly.
- `TMDB_MOCK_FIXTURE=true` enables the fixture: the exact query `[mock] book name` returns work `OL900001W` without HTTP, even when disabled. Other queries behave normally.
- Open Library data has no retention duty (D-095); it is kept until the user changes it.
- Covers may be absent (`ErrNoCover`); the item is still imported without an image.
- Settings expose this as read-only `book_lookup_available`.
