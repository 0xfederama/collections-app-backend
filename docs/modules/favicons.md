# favicons

## Purpose

Serves the favicon of an item's website link (R28). The server fetches the icon of the link's origin itself, keeps it in a disk cache under `DATA_DIR/favicons` and answers `GET /api/v1/favicon`, so the web never calls a third-party favicon service. The outbound client refuses non-public addresses at dial time. Design context lives in `/Users/federico/Developer/collections-app/design-docs/`.

## Key files

- `internal/favicons/service.go`: `Service.Get`, `Normalize` (origin and cache key), disk cache, TTLs, one fetch per key
- `internal/favicons/fetch.go`: `HTTPFetcher`, dial-time address guard, `<link rel=icon>` parsing, content sniffing
- `internal/favicons/model.go`: size and TTL limits, `Icon`, `ErrNoIcon`, `ErrBlocked`
- `internal/favicons/handler.go`: `Register`, `getFavicon` (streams the bytes with cache headers)
- `internal/favicons/favicons_test.go`: httptest origins; loopback is allowed only through `Options.AllowLoopback`

## Interfaces

- `NewService(fetch Fetcher, files storage.FileStore, clk clock.Clock, log *slog.Logger) *Service`
- `(*Service).Get(ctx, rawURL) (Icon, error)`: cached or fetched icon; `apperr.FaviconNotFound` or a URL validation error
- `Fetcher`: `Fetch(ctx, origin) (Icon, error)`; `NewHTTPFetcher(Options)` is the production client (`cmd/api` passes zero `Options`)
- `Normalize(rawURL) (origin, key, err)`: http or https only, host lowercased, at most 2048 characters
- `Register(api, log, svc)`: `GET /api/v1/favicon` (`getFavicon`, tag `favicon`); 200 image bytes, 404 `favicon_not_found`, 422 on `url`

## Gotchas

- The cache key is the SHA-256 of the lowercased host and port only. The scheme is not in the key, so `http://` and `https://` of one host share an entry, and the first request's scheme is the one fetched.
- Entries are on disk as `favicons/<key>.img` and `favicons/<key>.json`. The sidecar is written last and makes an entry visible. Icons live 30 days, "no icon" answers 1 day; expiry is checked on read and an expired entry is overwritten, never deleted.
- Only "no icon" results are cached as misses. Network failures (timeout, refused) answer 404 uncached; a blocked origin answers 422 uncached.
- The dial guard runs on every connection, redirects included, and refuses loopback, private, link-local, CGNAT, multicast, reserved and NAT64/6to4 addresses. Proxies are disabled.
- Lookup order: `<link rel=icon>` (up to 3 candidates, then `apple-touch-icon`) from `/`, then `/favicon.ico`. Max 3 redirects, 512 KiB page, 256 KiB icon, 10 s per lookup.
- Content is sniffed from the bytes: PNG, ICO, GIF, JPEG and WebP only. SVG is refused on purpose.
- Concurrent requests for one key share one fetch. The fetch outlives the first caller's request, so a cancelled caller does not abort it.
- A cache write failure is logged and the fetched icon is still served. The image sweeper walks only `images/`, so it never touches favicon files.
