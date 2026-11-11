# users

## Purpose

Owns the accounts of F-027: one row per person who signed in, keyed by lower-cased e-mail inside the workspace. The identity middleware creates the row on the first request; `GET /api/v1/me` returns the caller's own account and `GET /api/v1/users` lists the workspace's accounts. Name and avatar are best-effort, filled from the Access identity endpoint. Design context: `/Users/federico/Developer/collections-app/design-docs/features/F-027-accounts.md`.

## Key files

- `internal/users/model.go`: `User`, `Profile`
- `internal/users/service.go`: `Service`, `Store`, `EnsureByEmail`, `ProfileDue`, `SaveProfile`, bounded cache
- `internal/users/repository.go`: `Postgres` store (one query per method, workspace from the context)
- `internal/users/handler.go`, `wire.go`: `Register`, `getMe`, `listUsers`, `MeJSON`, `UserJSON`, `UserListJSON`
- `db/migrations/000002_accounts.up.sql`: `users` table (unique on workspace and email); the owner columns and `user_top_order` of the same file belong to [access](access.md)
- `db/queries/users.sql`: sqlc input; generated code in `internal/platform/database/sqlc/users.sql.go`
- `internal/users/service_test.go`: validation, cache bounds, profile due rules

## Interfaces

- `NewService(store Store, clk clock.Clock) *Service`, `NewPostgres(pool) *Postgres`
- `(*Service).EnsureByEmail(ctx, email) (User, error)`: trims and lower-cases; 422 `validation_failed` outside 3 to 320 characters; creates on first sight
- `(*Service).Get(ctx, id) (User, error)`: one account of the workspace
- `(*Service).ProfileDue(u User) bool`: true when the profile is never fetched or older than `ProfileMaxAge` (24 h); a true answer counts as an attempt
- `(*Service).SaveProfile(ctx, id, Profile) error`: stores name and avatar and stamps `profile_fetched_at`
- `Register(api, log, svc Reader, devIdentities []string)`: adds `GET /api/v1/me` (`getMe`) and `GET /api/v1/users` (`listUsers`)
- `(*Service).List(ctx) ([]User, error)`: every account of the workspace, ordered by e-mail
- `MeJSON`: `id`, `email`, `name` and `avatar_url` (nullable), `identity_source` (`access` | `lan` | `dev`, from `scope.IdentitySource`; a context without one reports `access`; the web hides Log out unless `access`, D-151), `dev_identities` (nullable, set only with `DEV_IDENTITY`)
- `UserListJSON`: `{data: [UserJSON]}` with `id`, `email`, `name`, `avatar_url`; the schema names are `User` and `UserList`

## Gotchas

- Users are per workspace: the unique key is `(workspace_id, email)`, and every query filters by it.
- Name and avatar stay `null` until a profile fetch succeeds; the web shows the e-mail then.
- The e-mail cache (1 minute TTL, 256 entries, cleared when full) only saves a query; it is per instance.
- Profile attempts are spaced 5 minutes apart per user, so a failing identity endpoint is not called per request.
- `GET /me` and `GET /users` answer 401 `unauthenticated` when the context has no user; the identity middleware normally prevents that.
- `GET /users` is unpaginated and unfiltered: any signed-in user sees every account of the workspace (names and e-mails), which is how the sharing picker and the author names work.
- Migration `000002` is expand-only (new tables and nullable columns), per D-118; it moves no data.
- `(*Service).Lookup(ctx, email) (User, bool, error)`: finds a user who signed in without creating one (used to share a folder).
