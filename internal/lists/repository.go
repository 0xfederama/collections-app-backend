package lists

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/0xfederama/collections-app-backend/internal/apperr"
	"github.com/0xfederama/collections-app-backend/internal/platform/database"
	"github.com/0xfederama/collections-app-backend/internal/platform/database/sqlc"
	"github.com/0xfederama/collections-app-backend/internal/platform/scope"
)

// DB is satisfied by *pgxpool.Pool: plain queries plus transactions.
type DB interface {
	database.TxBeginner
	sqlc.DBTX
}

// Postgres implements Store on a pool.
type Postgres struct{ db DB }

func NewPostgres(db DB) *Postgres { return &Postgres{db: db} }

// InTx runs fn in one transaction: commit on nil, rollback on error.
func (p *Postgres) InTx(ctx context.Context, fn func(Repo) error) error {
	return database.InTx(ctx, p.db, func(q *sqlc.Queries) error {
		return fn(&repo{q: q})
	})
}

// Get reads one active list outside a transaction.
func (p *Postgres) Get(ctx context.Context, id uuid.UUID) (List, error) {
	return (&repo{q: sqlc.New(p.db)}).Get(ctx, id)
}

// repo runs the queries of one transaction.
type repo struct{ q *sqlc.Queries }

var _ Repo = (*repo)(nil)

func (r *repo) LockTree(ctx context.Context) error {
	return r.q.LockTree(ctx, scope.WorkspaceID(ctx))
}

func (r *repo) Get(ctx context.Context, id uuid.UUID) (List, error) {
	row, err := r.q.GetList(ctx, sqlc.GetListParams{ID: id, WorkspaceID: scope.WorkspaceID(ctx)})
	if err != nil {
		return List{}, notFound(err, "list")
	}
	return fromCountedRow(row), nil
}

// GetForUpdate locks the active row (sqlc's LockList).
func (r *repo) GetForUpdate(ctx context.Context, id uuid.UUID) (List, error) {
	row, err := r.q.LockList(ctx, sqlc.LockListParams{ID: id, WorkspaceID: scope.WorkspaceID(ctx)})
	if err != nil {
		return List{}, notFound(err, "list")
	}
	return fromRow(row), nil
}

func (r *repo) ListActive(ctx context.Context) ([]List, error) {
	rows, err := r.q.ListActiveLists(ctx, scope.WorkspaceID(ctx))
	if err != nil {
		return nil, fmt.Errorf("list lists: %w", err)
	}
	out := make([]List, 0, len(rows))
	for _, row := range rows {
		out = append(out, fromCountedRow(sqlc.GetListRow(row)))
	}
	return out, nil
}

func (r *repo) ChildIDs(ctx context.Context, folder *uuid.UUID) ([]uuid.UUID, error) {
	ids, err := r.q.ListActiveListIDsByFolder(ctx, sqlc.ListActiveListIDsByFolderParams{WorkspaceID: scope.WorkspaceID(ctx), FolderID: nullUUID(folder)})
	if err != nil {
		return nil, fmt.Errorf("list child lists: %w", err)
	}
	return ids, nil
}

func (r *repo) NextSortOrder(ctx context.Context, folder *uuid.UUID) (int, error) {
	next, err := r.q.NextListSortOrder(ctx, sqlc.NextListSortOrderParams{WorkspaceID: scope.WorkspaceID(ctx), FolderID: nullUUID(folder)})
	if err != nil {
		return 0, fmt.Errorf("next list sort order: %w", err)
	}
	return int(next), nil
}

func (r *repo) Insert(ctx context.Context, l List, now time.Time) (List, error) {
	pinned, err := pinnedToJSON(l.PinnedFields)
	if err != nil {
		return List{}, err
	}
	row, err := r.q.InsertList(ctx, sqlc.InsertListParams{
		WorkspaceID: scope.WorkspaceID(ctx), FolderID: nullUUID(l.FolderID), Name: l.Name, Type: l.Type, Description: l.Description,
		DisplayMode: l.DisplayMode, SortMode: l.SortMode, PinnedFields: pinned, Icon: l.Icon, IconColor: l.IconColor,
		SortOrder: int32(l.SortOrder), Now: now,
	})
	if err != nil {
		return List{}, fmt.Errorf("insert list: %w", err)
	}
	return fromRow(row), nil
}

func (r *repo) Update(ctx context.Context, l List, now time.Time) (List, error) {
	pinned, err := pinnedToJSON(l.PinnedFields)
	if err != nil {
		return List{}, err
	}
	row, err := r.q.UpdateList(ctx, sqlc.UpdateListParams{
		Name: l.Name, Description: l.Description, DisplayMode: l.DisplayMode, SortMode: l.SortMode,
		PinnedFields: pinned, Icon: l.Icon, IconColor: l.IconColor,
		Now: now, ID: l.ID, WorkspaceID: scope.WorkspaceID(ctx),
	})
	if err != nil {
		return List{}, notFound(err, "list")
	}
	return fromRow(row), nil
}

func (r *repo) UpdateType(ctx context.Context, id uuid.UUID, typ *string, now time.Time) (List, error) {
	row, err := r.q.UpdateListType(ctx, sqlc.UpdateListTypeParams{Type: typ, Now: now, ID: id, WorkspaceID: scope.WorkspaceID(ctx)})
	if err != nil {
		return List{}, notFound(err, "list")
	}
	return fromRow(row), nil
}

func (r *repo) Move(ctx context.Context, id uuid.UUID, folder *uuid.UUID, sortOrder int, now time.Time) (List, error) {
	row, err := r.q.MoveList(ctx, sqlc.MoveListParams{FolderID: nullUUID(folder), SortOrder: int32(sortOrder), Now: now, ID: id, WorkspaceID: scope.WorkspaceID(ctx)})
	if err != nil {
		return List{}, notFound(err, "list")
	}
	return fromRow(row), nil
}

func (r *repo) SetSortOrder(ctx context.Context, id uuid.UUID, sortOrder int, now time.Time) error {
	if err := r.q.SetListSortOrder(ctx, sqlc.SetListSortOrderParams{SortOrder: int32(sortOrder), Now: now, ID: id, WorkspaceID: scope.WorkspaceID(ctx)}); err != nil {
		return fmt.Errorf("set list sort order: %w", err)
	}
	return nil
}

// FolderChain loads the active folders once and walks the parents in Go. It
// stops at a missing or trashed parent (as the recursive query did) and at a
// repeated folder, so corrupt cycles terminate.
func (r *repo) FolderChain(ctx context.Context, folderID uuid.UUID) ([]PathFolder, error) {
	rows, err := r.q.ListActiveFolders(ctx, scope.WorkspaceID(ctx))
	if err != nil {
		return nil, fmt.Errorf("list folders: %w", err)
	}
	return folderChain(rows, folderID), nil
}

// folderChain returns folderID and its ancestors among rows (active folders), root first.
func folderChain(rows []sqlc.Folder, folderID uuid.UUID) []PathFolder {
	byID := make(map[uuid.UUID]sqlc.Folder, len(rows))
	for _, f := range rows {
		byID[f.ID] = f
	}
	chain := []PathFolder{}
	seen := make(map[uuid.UUID]bool, len(rows))
	id := folderID
	for {
		f, ok := byID[id]
		if !ok || seen[id] {
			break
		}
		seen[id] = true
		chain = append(chain, PathFolder{ID: f.ID, Name: f.Name})
		if !f.ParentFolderID.Valid {
			break
		}
		id = f.ParentFolderID.UUID
	}
	slices.Reverse(chain)
	return chain
}

func (r *repo) FolderExists(ctx context.Context, id uuid.UUID) (bool, error) {
	_, err := r.q.GetFolder(ctx, sqlc.GetFolderParams{ID: id, WorkspaceID: scope.WorkspaceID(ctx)})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("get folder: %w", err)
	}
	return true, nil
}

func (r *repo) TypeDefinition(ctx context.Context, key string) (TypeInfo, []FieldInfo, error) {
	ws := scope.WorkspaceID(ctx)
	def, err := r.q.GetTypeDefinitionByKey(ctx, sqlc.GetTypeDefinitionByKeyParams{WorkspaceID: ws, Key: key})
	if err != nil {
		return TypeInfo{}, nil, notFound(err, "type")
	}
	rows, err := r.q.ListTypeFields(ctx, sqlc.ListTypeFieldsParams{WorkspaceID: ws, TypeID: def.ID})
	if err != nil {
		return TypeInfo{}, nil, fmt.Errorf("list type fields: %w", err)
	}
	fields := make([]FieldInfo, 0, len(rows))
	for _, f := range rows {
		fields = append(fields, FieldInfo{Key: f.Key, FieldType: f.FieldType})
	}
	return TypeInfo{Key: def.Key, AllowsPrice: def.AllowsPrice, DefaultSortField: def.DefaultSortField}, fields, nil
}

func (r *repo) ClearItemsMetadata(ctx context.Context, listID uuid.UUID, now time.Time) error {
	if _, err := r.q.ClearListItemsMetadata(ctx, sqlc.ClearListItemsMetadataParams{Now: now, ListID: listID, WorkspaceID: scope.WorkspaceID(ctx)}); err != nil {
		return fmt.Errorf("clear items metadata: %w", err)
	}
	return nil
}

func (r *repo) ClearItemsPrice(ctx context.Context, listID uuid.UUID, now time.Time) error {
	if _, err := r.q.ClearListItemsPrice(ctx, sqlc.ClearListItemsPriceParams{Now: now, ListID: listID, WorkspaceID: scope.WorkspaceID(ctx)}); err != nil {
		return fmt.Errorf("clear items price: %w", err)
	}
	return nil
}

// nullUUID converts an optional id (nil = root) to its sqlc form.
func nullUUID(id *uuid.UUID) uuid.NullUUID {
	if id == nil {
		return uuid.NullUUID{}
	}
	return uuid.NullUUID{UUID: *id, Valid: true}
}

func ptrUUID(id uuid.NullUUID) *uuid.UUID {
	if !id.Valid {
		return nil
	}
	v := id.UUID
	return &v
}

// notFound turns "no rows" into the domain error for resource; other errors pass through.
func notFound(err error, resource string) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return apperr.NotFound(resource)
	}
	return err
}

// fromRow converts a list row without an item count (the write queries).
func fromRow(l sqlc.List) List {
	return List{
		ID: l.ID, FolderID: ptrUUID(l.FolderID), Name: l.Name, Type: l.Type, Description: l.Description,
		DisplayMode: l.DisplayMode, SortMode: l.SortMode, PinnedFields: pinnedFromJSON(l.PinnedFields), Icon: l.Icon, IconColor: l.IconColor,
		SortOrder: int(l.SortOrder), Version: int(l.Version), CreatedAt: l.CreatedAt.UTC(), UpdatedAt: l.UpdatedAt.UTC(),
	}
}

// fromCountedRow converts a list row with its item count (the read queries).
func fromCountedRow(l sqlc.GetListRow) List {
	return List{
		ID: l.ID, FolderID: ptrUUID(l.FolderID), Name: l.Name, Type: l.Type, Description: l.Description,
		DisplayMode: l.DisplayMode, SortMode: l.SortMode, PinnedFields: pinnedFromJSON(l.PinnedFields), Icon: l.Icon, IconColor: l.IconColor,
		SortOrder: int(l.SortOrder), ItemCount: int(l.ItemCount), Version: int(l.Version),
		CreatedAt: l.CreatedAt.UTC(), UpdatedAt: l.UpdatedAt.UTC(),
	}
}
