# users

## Purpose

Owns the accounts of F-027: one row per person who signed in, keyed by lower-cased e-mail inside the workspace. The identity middleware creates the row on the first request; `GET /api/v1/me` returns the caller's own account. Name and avatar are best-effort, filled from the Access identity endpoint. Design context: `/Users/federico/Developer/collections-app/design-docs/features/F-027-accounts.md`.

## Key files

- `internal/users/model.go`: `User`, `Profile`
- `internal/users/service.go`: `Service`, `Store`, `EnsureByEmail`, `ProfileDue`, `SaveProfile`, bounded cache
- `internal/users/repository.go`: `Postgres` store (one query per method, workspace from the context)
- `internal/users/handler.go`, `wire.go`: `Register`, `getMe`, `MeJSON`, `MeOutput`
- `db/migrations/000002_accounts.up.sql`: `users` table (unique on workspace and email)
- `db/queries/users.sql`: sqlc input; generated code in `internal/platform/database/sqlc/users.sql.go`
- `internal/users/service_test.go`: validation, cache bounds, profile due rules

## Interfaces

- `NewService(store Store, clk clock.Clock) *Service`, `NewPostgres(pool) *Postgres`
- `(*Service).EnsureByEmail(ctx, email) (User, error)`: trims and lower-cases; 422 `validation_failed` outside 3 to 320 characters; creates on first sight
- `(*Service).Get(ctx, id) (User, error)`: one account of the workspace
- `(*Service).ProfileDue(u User) bool`: true when the profile is never fetched or older than `ProfileMaxAge` (24 h); a true answer counts as an attempt
- `(*Service).SaveProfile(ctx, id, Profile) error`: stores name and avatar and stamps `profile_fetched_at`
- `Register(api, log, svc Reader, devIdentities []string)`: adds `GET /api/v1/me` (operation `getMe`)
- `MeJSON`: `id`, `email`, `name` and `avatar_url` (nullable), `dev_identities` (nullable, set only with `DEV_IDENTITY`)

## Gotchas

- Users are per workspace: the unique key is `(workspace_id, email)`, and every query filters by it.
- Name and avatar stay `null` until a profile fetch succeeds; the web shows the e-mail then.
- The e-mail cache (1 minute TTL, 256 entries, cleared when full) only saves a query; it is per instance.
- Profile attempts are spaced 5 minutes apart per user, so a failing identity endpoint is not called per request.
- `GET /me` answers 401 `unauthenticated` when the context has no user; the identity middleware normally prevents that.
- Migration `000002` is structure only and expand-only (a new table), per D-118.
