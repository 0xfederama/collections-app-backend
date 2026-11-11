# access

## Purpose

Decides what the caller may see (F-027 B2, D-147). Ownership sits only on top-level folders and lists; a nested resource belongs to its top-level ancestor (its "box"). A resource is visible when its box is, and `Snapshot.Visible` is the one place that rule is written (B3 adds shared box folders there). Features reach it through small interfaces they declare, wired in `httpapi`. Design context: `/Users/federico/Developer/collections-app/design-docs/features/F-027-accounts.md`.

## Key files

- `internal/access/model.go`: `Kind`, `Node`, `Box`, `Snapshot`, the visibility rule
- `internal/access/service.go`: `Service`, the per-request snapshot cache (`WithCache`), the answers the guard and features use
- `internal/access/repository.go`: `Postgres`, one plain query per read (snapshot, item and batch roots, per-user top order)
- `internal/access/backfill.go`: `Backfill`, `ErrOwnerEmailRequired` (startup owner adoption)
- `db/queries/access.sql`: sqlc inputs for the reads, the order upsert and the backfill
- `internal/access/access_test.go`, `backfill_test.go`: box rules, cycles, cache, backfill and concurrent startups

## Interfaces

- `NewService(repo Repo) *Service`; `NewPostgres(db DB) *Postgres` (a pool, also the backfill store)
- `(*Service).Visible(ctx, kind Kind, id) (bool, error)`: any state; a missing resource or no user in the context is false
- `FolderVisible` / `ListVisible(ctx, id) (bool, error)`: shorthands used by folders, lists, items and trash
- `VisibleFolderIDs` / `VisibleListIDs(ctx)`: the active folders and lists the caller may see (tree, search, reorder)
- `VisibleBatchIDs(ctx) ([]uuid.UUID, error)`: trash batches whose root box the caller may see; never nil
- `TopOrder(ctx) (folders, lists map[uuid.UUID]int, error)`: the caller's own positions of their top-level rows
- `WithCache(ctx) context.Context`: per-request snapshot, read at most once (`httpapi` sets it on every request)
- `Backfill(ctx, store BackfillStore, ownerEmail string, log *slog.Logger) error`: gives ownerless top-level rows to that user, one tx under its own advisory lock

## Gotchas

- Owner is set only on top-level rows. Moving a folder or list into a folder clears it; moving to the top keeps the stored owner or takes the caller's when there is none (SQL owner rule in `folders.sql`, `lists.sql`, `trash.sql`).
- Visibility covers rows in any state: a trashed resource keeps its box. The `Visible*IDs` helpers return active rows only.
- No user in the context means nothing is visible: search answers empty, the trash list is empty.
- The snapshot is read once per request and is stale after a write; use it before the writes of that request only.
- Background jobs (sweeper, auto-purge, lookup refresh) do not go through access and work on the whole workspace.
- Backfill runs at every startup from `cmd/api`: a no-op when nothing is ownerless; the owner is `OWNER_EMAIL`, else the first `DEV_IDENTITY`. With ownerless rows and neither, startup fails (`ErrOwnerEmailRequired`).
- Backfill adopts every ownerless top-level row, trashed ones included, and creates the owner user when missing.
