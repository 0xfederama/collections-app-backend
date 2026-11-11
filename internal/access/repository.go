package access

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/0xfederama/collections-app-backend/internal/platform/database"
	"github.com/0xfederama/collections-app-backend/internal/platform/database/sqlc"
	"github.com/0xfederama/collections-app-backend/internal/platform/scope"
)

// DB is satisfied by *pgxpool.Pool: plain queries plus transactions (the backfill).
type DB interface {
	database.TxBeginner
	sqlc.DBTX
}

// Postgres implements Repo (and the backfill store) on a pool.
type Postgres struct {
	db DB
	q  *sqlc.Queries
}

var _ Repo = (*Postgres)(nil)

func NewPostgres(db DB) *Postgres { return &Postgres{db: db, q: sqlc.New(db)} }

func (p *Postgres) Snapshot(ctx context.Context) (Snapshot, error) {
	ws := scope.WorkspaceID(ctx)
	frows, err := p.q.AccessFolders(ctx, ws)
	if err != nil {
		return Snapshot{}, fmt.Errorf("access folders: %w", err)
	}
	lrows, err := p.q.AccessLists(ctx, ws)
	if err != nil {
		return Snapshot{}, fmt.Errorf("access lists: %w", err)
	}
	folders := make([]Node, 0, len(frows))
	for _, r := range frows {
		folders = append(folders, Node{ID: r.ID, ParentID: ptr(r.ParentFolderID), Owner: ptr(r.OwnerUserID), Trashed: r.Trashed})
	}
	lists := make([]Node, 0, len(lrows))
	for _, r := range lrows {
		lists = append(lists, Node{ID: r.ID, ParentID: ptr(r.FolderID), Owner: ptr(r.OwnerUserID), Trashed: r.Trashed})
	}
	return NewSnapshot(folders, lists), nil
}

func (p *Postgres) ItemList(ctx context.Context, id uuid.UUID) (uuid.UUID, bool, error) {
	list, err := p.q.AccessItemList(ctx, sqlc.AccessItemListParams{ID: id, WorkspaceID: scope.WorkspaceID(ctx)})
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, false, nil
	}
	if err != nil {
		return uuid.Nil, false, fmt.Errorf("access item list: %w", err)
	}
	return list, true, nil
}

func (p *Postgres) AttachmentItem(ctx context.Context, id uuid.UUID) (uuid.UUID, bool, error) {
	item, err := p.q.AccessAttachmentItem(ctx, sqlc.AccessAttachmentItemParams{
		ID: uuid.NullUUID{UUID: id, Valid: true}, WorkspaceID: scope.WorkspaceID(ctx),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, false, nil
	}
	if err != nil {
		return uuid.Nil, false, fmt.Errorf("access attachment item: %w", err)
	}
	return item, true, nil
}

func (p *Postgres) TrashRoot(ctx context.Context, id uuid.UUID) (TrashRoot, bool, error) {
	row, err := p.q.AccessTrashBatch(ctx, sqlc.AccessTrashBatchParams{ID: id, WorkspaceID: scope.WorkspaceID(ctx)})
	if errors.Is(err, pgx.ErrNoRows) {
		return TrashRoot{}, false, nil
	}
	if err != nil {
		return TrashRoot{}, false, fmt.Errorf("access trash batch: %w", err)
	}
	root := TrashRoot{BatchID: id, Type: row.RootType, RootID: row.RootID}
	if row.RootType == rootItem {
		list, ok, err := p.ItemList(ctx, row.RootID)
		if err != nil {
			return TrashRoot{}, false, err
		}
		if ok {
			root.ItemList = &list
		}
	}
	return root, true, nil
}

func (p *Postgres) TrashRoots(ctx context.Context) ([]TrashRoot, error) {
	rows, err := p.q.AccessTrashRoots(ctx, scope.WorkspaceID(ctx))
	if err != nil {
		return nil, fmt.Errorf("access trash roots: %w", err)
	}
	out := make([]TrashRoot, 0, len(rows))
	for _, r := range rows {
		out = append(out, TrashRoot{BatchID: r.ID, Type: r.RootType, RootID: r.RootID, ItemList: ptr(r.ItemListID)})
	}
	return out, nil
}

func (p *Postgres) TopOrder(ctx context.Context, user uuid.UUID) (map[uuid.UUID]int, map[uuid.UUID]int, error) {
	rows, err := p.q.ListUserTopOrder(ctx, sqlc.ListUserTopOrderParams{WorkspaceID: scope.WorkspaceID(ctx), UserID: user})
	if err != nil {
		return nil, nil, fmt.Errorf("list top order: %w", err)
	}
	folders, lists := map[uuid.UUID]int{}, map[uuid.UUID]int{}
	for _, r := range rows {
		switch r.EntryType {
		case rootFolder:
			folders[r.EntryID] = int(r.SortOrder)
		case rootList:
			lists[r.EntryID] = int(r.SortOrder)
		}
	}
	return folders, lists, nil
}

func ptr(id uuid.NullUUID) *uuid.UUID {
	if !id.Valid {
		return nil
	}
	v := id.UUID
	return &v
}

// backfillLockKey is the advisory lock of the ownership backfill. It must differ from the
// workspace bootstrap key (workspace.bootstrapLockKey, ...01).
const backfillLockKey int64 = 0x436F6C6C41707002 // "CollApp" + 2

// InTx runs the backfill in one transaction: commit on nil, rollback on error.
func (p *Postgres) InTx(ctx context.Context, fn func(BackfillRepo) error) error {
	return database.InTx(ctx, p.db, func(q *sqlc.Queries) error {
		return fn(backfillRepo{q: q})
	})
}

type backfillRepo struct{ q *sqlc.Queries }

func (b backfillRepo) Lock(ctx context.Context) error {
	return b.q.LockAccessBackfill(ctx, backfillLockKey)
}

func (b backfillRepo) CountOwnerless(ctx context.Context) (int, int, error) {
	ws := scope.WorkspaceID(ctx)
	f, err := b.q.CountOwnerlessTopFolders(ctx, ws)
	if err != nil {
		return 0, 0, err
	}
	l, err := b.q.CountOwnerlessTopLists(ctx, ws)
	if err != nil {
		return 0, 0, err
	}
	return int(f), int(l), nil
}

func (b backfillRepo) UpsertUser(ctx context.Context, email string) (uuid.UUID, error) {
	u, err := b.q.UpsertUserByEmail(ctx, sqlc.UpsertUserByEmailParams{WorkspaceID: scope.WorkspaceID(ctx), Email: email})
	if err != nil {
		return uuid.Nil, err
	}
	return u.ID, nil
}

func (b backfillRepo) Assign(ctx context.Context, owner uuid.UUID) (int, int, error) {
	ws := scope.WorkspaceID(ctx)
	o := uuid.NullUUID{UUID: owner, Valid: true}
	f, err := b.q.AssignOwnerlessTopFolders(ctx, sqlc.AssignOwnerlessTopFoldersParams{OwnerUserID: o, WorkspaceID: ws})
	if err != nil {
		return 0, 0, err
	}
	l, err := b.q.AssignOwnerlessTopLists(ctx, sqlc.AssignOwnerlessTopListsParams{OwnerUserID: o, WorkspaceID: ws})
	if err != nil {
		return 0, 0, err
	}
	return int(f), int(l), nil
}
