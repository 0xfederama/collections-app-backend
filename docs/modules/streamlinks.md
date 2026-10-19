# streamlinks

`internal/streamlinks` builds the read-only `metadata.streaming.platform_links` (API 0.15.0): for each platform in `streaming.platforms`, a URL that opens that service's search for the item's title.

## Rules
- Computed at read time from the title and the stored platform names, never stored, no database access. `httpapi` calls `AddToMetadata` for the full item and for every row (list, search, recent), so the values are identical.
- The key is the platform name exactly as stored. A platform not in the table has no entry; an empty (trimmed) title gives no links. With no known platform the object is empty.
- A platform name is matched after `Normalize` (lowercase, `+` spelled `plus`, everything else that is not a letter or digit removed), by exact alias, then by prefix for plan variants (`netflix*`, `amazonprimevideo*`, `primevideo*`, `disneyplus*`).
- The title is trimmed and escaped with `url.QueryEscape` (spaces become `+`). Every URL is https. All hosts are global: `Link` takes the item's `streaming.country` but ignores it, since no country-specific host is known.
- Client-sent `platform_links` is dropped by `items.validateClientMetadata`; it is not a stored key.

## Table
`<title>` is the escaped title. None of the patterns was checked against the live sites.

| Service (aliases after normalisation) | URL | Verified? |
|---|---|---|
| Netflix (`netflix`, any `netflix...`, e.g. "Netflix basic with Ads") | `https://www.netflix.com/search?q=<title>` | unverified, check in a browser |
| Prime Video (`amazonprimevideo`, `primevideo`, and prefixes) | `https://www.primevideo.com/search/?phrase=<title>` | unverified, check in a browser |
| Disney Plus (`disneyplus`, so "Disney+" too, and prefixes) | `https://www.disneyplus.com/search?q=<title>` | unverified, check in a browser |
| Apple TV (`appletv`, `appletvplus`) | `https://tv.apple.com/search?term=<title>` | unverified, check in a browser |
| Max (`max`, `hbomax`) | `https://play.max.com/search?q=<title>` | unverified, check in a browser |
| Paramount Plus (`paramountplus`, so "Paramount+") | `https://www.paramountplus.com/search/?q=<title>` | unverified, check in a browser |
| Hulu (`hulu`) | `https://www.hulu.com/search?q=<title>` | unverified, check in a browser |
| Peacock (`peacock`, `peacockpremium`) | `https://www.peacocktv.com/search?q=<title>` | unverified, check in a browser |
| YouTube (`youtube`) | `https://www.youtube.com/results?search_query=<title>` | unverified, check in a browser |
| Crunchyroll (`crunchyroll`) | `https://www.crunchyroll.com/search?q=<title>` | unverified, check in a browser |
| MUBI (`mubi`) | `https://mubi.com/search/films?query=<title>` | unverified, check in a browser |

Left out on purpose (no known search URL): RaiPlay, Infinity+, NOW, Sky Go, Rakuten TV, Google Play Movies, Plex, Pluto TV, Tubi TV. To add one, add an alias in `streamlinks.go`, a row here and a test case.
