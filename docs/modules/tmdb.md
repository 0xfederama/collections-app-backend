# tmdb

## Purpose

Provides an optional, bounded, server-side TMDB client for movie and TV search, details, watch-provider links and poster downloads. It is wrapped as the movie and TV `Provider` in `internal/lookup`. Production uses fixed TMDB HTTPS origins. Design context lives in `/Users/federico/Developer/collections-app/design-docs/`.

## Key files

- `internal/integrations/tmdb/client.go`: API client, result mapping, cache, pacing, poster download
- `internal/lookup/tmdb_provider.go`: adapter onto the lookup `Provider` interface
- `internal/lookup/handler.go`, `wire.go`: `listTmdbSuggestions` and its JSON type
- `internal/platform/config/config.go`: optional `TMDB_API_KEY` and E2E fixture switch
- `cmd/api/main.go`: client construction and availability wiring

## Interfaces

- `New(apiKey string) *Client`, `NewWithOptions(apiKey, Options)`: fixed production origins; `Options` overrides HTTP client, base URLs, timeout and `MockFixture`
- `(*Client).Enabled() bool`: true with a server key or with the mock fixture switch
- `(*Client).Search(ctx, type, query, language) ([]Match, error)`: movie or TV suggestions
- `(*Client).Details(ctx, type, id, language, country) (Details, error)`: localized metadata and provider data
- `(*Client).WatchLink(ctx, type, id, country) (string, error)`: JustWatch page for a country; `""` when TMDB has none. No production caller in the tree (tests only)
- `(*Client).DownloadPoster(ctx, posterPath) ([]byte, error)`: bounded poster image
- `GET /api/v1/tmdb/suggestions`: provider-ranked suggestions, or a disabled empty result

## Gotchas

- The key is optional and server-side only; blank means lookup is disabled. Set it in `deploy/server/.env`, never in browser configuration.
- `TMDB_MOCK_FIXTURE=true` is an E2E-only switch, rejected when `APP_ENV=production`. It also enables the Open Library fixture (see [openlibrary](openlibrary.md)).
- With the switch, the exact query `[mock] movie name` returns a fixture without HTTP (id `900001`). Other queries keep normal TMDB behaviour.
- `Details` returns the JustWatch page as `streaming.link`, only for `https` on `justwatch.com` or `www.justwatch.com`. The mock returns `https://www.justwatch.com/<country>/movie/mock`.
- Titles and movie directors are Latin script when TMDB has one (`names.go`): localized if Latin, then the en-US translation (US first; `Details` uses `append_to_response=translations`), then the Latin original; directors then the first Latin `also_known_as` of `/person/{id}` (a failed person call keeps the credit name; only a cancelled request fails Details). A non-Latin value is kept only as a last resort.
- Extra calls: `Details` makes one call plus one `/person` call per non-Latin director (at most 3 per Details). `Search` makes one extra `en-US` search only when a result has a non-Latin localized title. Search suggestions keep the raw TMDB `original_title`.
- Requests have timeouts and response-size bounds, are paced, honour retry-after, do not follow redirects, and search results are briefly cached.
- Attribution: an [approved TMDB logo and notice](https://www.themoviedb.org/about/logos-attribution) in About; [watch-provider data](https://developer.themoviedb.org/reference/tv-series-watch-providers) also needs JustWatch credit.
- TMDB data and posters must expire within six months ([TMDB API terms](https://www.themoviedb.org/api-terms-of-use)). Retention is in [lookup](lookup.md).
