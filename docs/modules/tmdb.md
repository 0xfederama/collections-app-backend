# tmdb

## Purpose

Provides an optional bounded server-side client for TMDB movie and TV search, details, and poster downloads. Production uses fixed TMDB HTTPS origins; design context lives in `/Users/federico/Developer/collections-app/design-docs/`.

## Key files

- `internal/tmdb/client.go`: API client, result mapping, cache, pacing, poster download
- `internal/httpapi/tmdb.go`: suggestions endpoint mapping
- `internal/config/config.go`: optional `TMDB_API_KEY`
- `cmd/api/main.go`: client construction and availability wiring

## Interfaces

- `New(apiKey string) *Client`: uses fixed production API and image origins
- `(*Client).Enabled() bool`: whether a server key is configured
- `(*Client).Search(ctx, kind, query, language) ([]Match, error)`: movie or TV suggestions
- `(*Client).Details(ctx, kind, id, language, country) (Details, error)`: localized metadata and provider data
- `(*Client).DownloadPoster(ctx, posterPath) ([]byte, error)`: fetches a bounded poster image
- `GET /api/v1/tmdb/suggestions`: returns provider-ranked suggestions or a disabled empty result

## Gotchas

- Key is optional and server-side only; blank means lookup disabled. Set it in `deploy/server/.env`, never browser configuration.
- Requests have timeouts and response-size bounds, are paced, handle retry-after, and stop redirects; search results are briefly cached.
- TMDB attribution requires an [approved logo and notice](https://www.themoviedb.org/about/logos-attribution) in About. [Provider availability](https://developer.themoviedb.org/reference/tv-series-watch-providers) requires JustWatch attribution.
- TMDB data and imported posters must expire by six months; see [TMDB API terms](https://www.themoviedb.org/api-terms-of-use). Design details: `/Users/federico/Developer/collections-app/design-docs/`.
