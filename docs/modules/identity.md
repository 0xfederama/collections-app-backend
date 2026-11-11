# identity

## Purpose

Resolves who a request acts as, for every `/api/v1` request (F-027 B1): a verified Cloudflare Access token, else a dev identity, else the LAN owner (`OWNER_EMAIL`, always on, D-150), else 401 `unauthenticated`. It is the only package that reads Access tokens and the headers Cloudflare adds. Design context: `/Users/federico/Developer/collections-app/design-docs/features/F-027-accounts.md`.

## Key files

- `internal/identity/middleware.go`: `Resolver`, `New`, `Options`, `Middleware`, the resolution order, dev identity, background profile fetch
- `internal/identity/keyset.go`: `KeySet`, the team's Access certs with a throttled refetch (1 per minute)
- `internal/identity/profile.go`: `fetchProfile`, name and avatar from `get-identity`
- `internal/identity/middleware_test.go`: order, token, dev, LAN and profile cases

## Interfaces

- `New(o Options) *Resolver`: builds the resolver; the verifier exists only when team domain and audience are both set
- `(*Resolver).Middleware(next http.Handler) http.Handler`: passes non-`/api/v1/` paths through; sets the user ID (`scope.WithUser`) and how it was resolved (`scope.WithIdentitySource`: `access`, `dev`, `lan`; read by `GET /me`) in the context
- `(*Resolver).Wait()`: blocks until started profile fetches finish (tests)
- `Options`: `TeamDomain`, `Audience`, `DevIdentities`, `OwnerEmail` (the LAN owner; empty = no LAN step), `Users`, `Logger`, `Clock`, `HTTPClient`
- `Users` (consumer interface): `EnsureByEmail`, `ProfileDue`, `SaveProfile` (satisfied by `*users.Service`)
- `NewKeySet(teamDomain, client, clk) *KeySet`: `oidc.KeySet` over `https://<team>/cdn-cgi/access/certs`

## Gotchas

- Order: `Cf-Access-Jwt-Assertion` (exactly one value) is verified and nothing else is tried; a bad token is 401, never a fallback. Then dev identity (only if `DevIdentities` is set), then the LAN owner, then 401. Dev identities come before the LAN owner, so a dev/test stack never reaches it.
- Verification: RS256 only; issuer `https://<team>`, audience `ACCESS_AUD`, expiry, and a non-empty `email` claim (lower-cased).
- The plain `Cf-Access-Authenticated-User-Email` header is never read.
- Dev identity: `X-Dev-User` header (exactly one value) or `dev_user` cookie; no value means the first identity. A value outside the list is 401.
- LAN owner (D-150): applies whenever `OwnerEmail` is set and no `Cf-Ray` or `Cf-Connecting-Ip` header is present (any value, even empty); there is no switch. A request with an edge header and no valid token is 401 (fail closed). This relies on cloudflared and the web image's Caddy passing those headers through unchanged: Caddy's `reverse_proxy` forwards client request headers as they are (checked D-151).
- Profile fetch runs in a background goroutine with its own timeout (3 s), uses the token as the `CF_Authorization` cookie and never logs it. A failure is logged at debug level only.
- Key refetch is throttled to once per minute, failures included, so forged tokens cannot hammer Cloudflare.
- Probes and `/admin/*` are not touched by this middleware.
