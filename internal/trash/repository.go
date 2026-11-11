package trash

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/0xfederama/collections-app-backend/internal/apperr"
	"github.com/0xfederama/collections-app-backend/internal/platform/database"
	"github.com/0xfederama/collections-app-backend/internal/platform/database/sqlc"
	"github.com/0xfederama/collections-app-backend/internal/platform/scope"
)

// Repo is the trash data the service needs. Methods take the workspace from the
// context (scope). Rows map to the pure model in model.go; no rule lives here.
// Methods marked SEAM write or read rows of another feature's table and move
// with that feature.
type Repo interface {
	// LockTree serializes structural writes of the workspace until the transaction ends (D-060).
	LockTree(ctx context.Context) error

	// InsertBatch creates the batch of a root (its name and former path) and returns its id.
	InsertBatch(ctx context.Context, rootType string, rootID uuid.UUID, name string, path []PathSegment, now time.Time) (uuid.UUID, error)
	// LockBatch locks a batch row. NotFound ("trash entry") when it is missing.
	LockBatch(ctx context.Context, id uuid.UUID) (Batch, error)
	// DeleteBatch removes a batch row. Nothing may reference it any more (D-051).
	DeleteBatch(ctx context.Context, id uuid.UUID) error
	// DeleteOrphanBatches removes the batches whose root row no longer exists.
	DeleteOrphanBatches(ctx context.Context) error
	// BatchIDs returns the ids of every batch of the workspace (the whole trash).
	BatchIDs(ctx context.Context) ([]uuid.UUID, error)
	// ExpiredBatchIDs returns the ids of the batches trashed before cutoff.
	ExpiredBatchIDs(ctx context.Context, cutoff time.Time) ([]uuid.UUID, error)
	// RetentionDays is the workspace's trash_retention_days. Nil when it is off or
	// when the workspace has no settings row: the auto-purge treats both as off.
	RetentionDays(ctx context.Context) (*int, error)
	// ListBatches returns one page of batches, newest first, with their counts. only, when not nil,
	// restricts the batches to those ids (the ones the caller may see); an empty non-nil slice is none.
	ListBatches(ctx context.Context, limit, offset int, only []uuid.UUID) ([]Entry, error)
	// CountBatches returns the number of batches of the workspace, restricted like ListBatches.
	CountBatches(ctx context.Context, only []uuid.UUID) (int, error)

	// FolderVisibleToCaller and ListVisibleToCaller re-read, inside the transaction and after the tree
	// lock, whether the caller may see a folder or list (any state): a share may have changed since
	// the access snapshot of the request.
	FolderVisibleToCaller(ctx context.Context, id uuid.UUID) (bool, error)
	ListVisibleToCaller(ctx context.Context, id uuid.UUID) (bool, error)
	// FolderMemberIDs returns the users a folder is shared with (a restored folder with members
	// cannot go into another folder).
	FolderMemberIDs(ctx context.Context, id uuid.UUID) ([]uuid.UUID, error)

	// FolderParents returns every folder of the workspace in any state, for the walks.
	FolderParents(ctx context.Context) ([]FolderNode, error)
	// LockFolder locks an active folder row (FOR UPDATE). NotFound when missing or trashed. SEAM: moves to folders.
	LockFolder(ctx context.Context, id uuid.UUID) (FolderRow, error)
	// LockList locks an active list row. NotFound when missing or trashed. SEAM: moves to lists.
	LockList(ctx context.Context, id uuid.UUID) (ListRow, error)
	// LockItem locks an active item row. NotFound when missing or trashed. SEAM: moves to items.
	LockItem(ctx context.Context, id uuid.UUID) (ItemRow, error)
	// GetActiveFolder returns an active folder. NotFound when missing or trashed. SEAM: moves to folders.
	GetActiveFolder(ctx context.Context, id uuid.UUID) (FolderRow, error)
	// GetActiveList returns an active list (its folder and name). NotFound when missing or trashed. SEAM: moves to lists.
	GetActiveList(ctx context.Context, id uuid.UUID) (ListRow, error)
	// GetAnyFolderState returns the name and trashed flag of a folder in any state. NotFound when it never existed.
	GetAnyFolderState(ctx context.Context, id uuid.UUID) (ParentState, error)
	// GetAnyListState returns the name and trashed flag of a list in any state. NotFound when it never existed.
	GetAnyListState(ctx context.Context, id uuid.UUID) (ParentState, error)
	// NextFolderSortOrder is the sort order after the last active folder under parent (nil = the workspace root). SEAM: moves to folders.
	NextFolderSortOrder(ctx context.Context, parent *uuid.UUID) (int, error)
	// NextListSortOrder is the sort order after the last active list in folder (nil = the workspace root). SEAM: moves to lists.
	NextListSortOrder(ctx context.Context, folder *uuid.UUID) (int, error)
	// NextItemSortOrder is the sort order after the last active item of a list. SEAM: moves to items.
	NextItemSortOrder(ctx context.Context, listID uuid.UUID) (int, error)

	// MarkFolders trashes the given active folders into batch at now (deleted_at, trash_batch_id,
	// updated_at). The version stays. SEAM: moves to folders.
	MarkFolders(ctx context.Context, ids []uuid.UUID, batch uuid.UUID, now time.Time) error
	// MarkLists trashes the given active lists into batch. SEAM: moves to lists.
	MarkLists(ctx context.Context, ids []uuid.UUID, batch uuid.UUID, now time.Time) error
	// MarkItems trashes the given active items into batch. SEAM: moves to items.
	MarkItems(ctx context.Context, ids []uuid.UUID, batch uuid.UUID, now time.Time) error
	// ActiveListIDsByFolders returns the active lists directly in the given folders. SEAM: moves to lists.
	ActiveListIDsByFolders(ctx context.Context, folderIDs []uuid.UUID) ([]uuid.UUID, error)
	// ActiveItemIDsByLists returns the active items of the given lists. SEAM: moves to items.
	ActiveItemIDsByLists(ctx context.Context, listIDs []uuid.UUID) ([]uuid.UUID, error)

	// GetTrashedFolder returns the batch's folder root, whatever its state (locked). NotFound ("trash entry") otherwise.
	GetTrashedFolder(ctx context.Context, id, batch uuid.UUID) (FolderRow, error)
	// GetTrashedList returns the batch's list root (locked). NotFound ("trash entry") otherwise. SEAM: moves to lists.
	GetTrashedList(ctx context.Context, id, batch uuid.UUID) (ListRow, error)
	// GetTrashedItem returns the batch's item root (locked). NotFound ("trash entry") otherwise. SEAM: moves to items.
	GetTrashedItem(ctx context.Context, id, batch uuid.UUID) (ItemRow, error)
	// RestoreFolderRoot puts the batch's folder root back under parent (nil = the workspace root) at sortOrder,
	// clears its trash columns and bumps its version. SEAM: moves to folders.
	RestoreFolderRoot(ctx context.Context, id, batch uuid.UUID, parent *uuid.UUID, sortOrder int, now time.Time) error
	// RestoreListRoot puts the batch's list root back into folder (nil = the workspace root). SEAM: moves to lists.
	RestoreListRoot(ctx context.Context, id, batch uuid.UUID, folder *uuid.UUID, sortOrder int, now time.Time) error
	// RestoreItemRoot puts the batch's item root back into listID. SEAM: moves to items.
	RestoreItemRoot(ctx context.Context, id, batch, listID uuid.UUID, sortOrder int, now time.Time) error
	// RestoreBatchFolders clears the trash columns of the rest of the batch's folders (versions stay). SEAM: moves to folders.
	RestoreBatchFolders(ctx context.Context, batch uuid.UUID, now time.Time) error
	// RestoreBatchLists clears the trash columns of the rest of the batch's lists. SEAM: moves to lists.
	RestoreBatchLists(ctx context.Context, batch uuid.UUID, now time.Time) error
	// RestoreBatchItems clears the trash columns of the rest of the batch's items. SEAM: moves to items.
	RestoreBatchItems(ctx context.Context, batch uuid.UUID, now time.Time) error

	// ListListIDsByFolders returns the lists in any state directly in the given folders (purge). SEAM: moves to lists.
	ListListIDsByFolders(ctx context.Context, folderIDs []uuid.UUID) ([]uuid.UUID, error)
	// ListItemIDsByLists returns the items in any state of the given lists (purge). SEAM: moves to items.
	ListItemIDsByLists(ctx context.Context, listIDs []uuid.UUID) ([]uuid.UUID, error)
	// ListAttachmentFiles returns the image attachments of the items with their file keys. Read before the items go.
	ListAttachmentFiles(ctx context.Context, itemIDs []uuid.UUID) ([]AttachmentFile, error)
	// DeleteContentVersions removes the content versions of the items. SEAM: moves to items.
	DeleteContentVersions(ctx context.Context, itemIDs []uuid.UUID) error
	// DeleteLookupImports removes the lookup records of the items. SEAM: moves to items.
	DeleteLookupImports(ctx context.Context, itemIDs []uuid.UUID) error
	// DeleteItems hard-deletes the items. SEAM: moves to items.
	DeleteItems(ctx context.Context, itemIDs []uuid.UUID) error
	// DeleteLists hard-deletes the lists. SEAM: moves to lists.
	DeleteLists(ctx context.Context, listIDs []uuid.UUID) error
	// DeleteFolders hard-deletes the folders in one statement (the parent check runs at its end). SEAM: moves to folders.
	DeleteFolders(ctx context.Context, folderIDs []uuid.UUID) error
	// DeleteFolderMembers removes the member rows of the given folders. They have no foreign key to
	// folders, so a purge deletes them explicitly, before the folders (D-118). SEAM: moves to folders.
	DeleteFolderMembers(ctx context.Context, folderIDs []uuid.UUID) error
	// DeleteTopOrder removes every user's top-level order rows of the given folders and lists. They have
	// no foreign key, so a purge deletes them explicitly (D-118). SEAM: moves to access.
	DeleteTopOrder(ctx context.Context, ids []uuid.UUID) error
	// DeleteAttachmentRows hard-deletes attachment rows, after the items that pointed to them went. SEAM: moves to images.
	DeleteAttachmentRows(ctx context.Context, ids []uuid.UUID) error
}

// DB is satisfied by *pgxpool.Pool.
type DB interface {
	database.TxBeginner
	sqlc.DBTX
}

// Postgres implements the repository on a pool. The reads below run outside a
// transaction, one query each on the pool, with the same mapping as the Repo
// methods of the same name. Every other method runs inside InTx.
type Postgres struct{ db DB }

// NewPostgres returns the repository on db (a pool, which also starts transactions).
func NewPostgres(db DB) *Postgres { return &Postgres{db: db} }

func (p *Postgres) reads() *repo { return &repo{q: sqlc.New(p.db)} }

func (p *Postgres) ListBatches(ctx context.Context, limit, offset int, only []uuid.UUID) ([]Entry, error) {
	return p.reads().ListBatches(ctx, limit, offset, only)
}

func (p *Postgres) CountBatches(ctx context.Context, only []uuid.UUID) (int, error) {
	return p.reads().CountBatches(ctx, only)
}

func (p *Postgres) RetentionDays(ctx context.Context) (*int, error) {
	return p.reads().RetentionDays(ctx)
}

// InTx runs fn in one transaction: commit on nil, rollback on error.
func (p *Postgres) InTx(ctx context.Context, fn func(Repo) error) error {
	return database.InTx(ctx, p.db, func(q *sqlc.Queries) error {
		return fn(&repo{q: q})
	})
}

// repo runs the queries of one transaction (or of the pool, through reads).
type repo struct{ q *sqlc.Queries }

var _ Repo = (*repo)(nil)

func (r *repo) LockTree(ctx context.Context) error {
	return r.q.LockTree(ctx, scope.WorkspaceID(ctx))
}

func (r *repo) InsertBatch(ctx context.Context, rootType string, rootID uuid.UUID, name string, path []PathSegment, now time.Time) (uuid.UUID, error) {
	if path == nil {
		path = []PathSegment{}
	}
	raw, err := json.Marshal(path)
	if err != nil {
		return uuid.Nil, fmt.Errorf("encode former path: %w", err)
	}
	id, err := r.q.InsertTrashBatch(ctx, sqlc.InsertTrashBatchParams{
		WorkspaceID: scope.WorkspaceID(ctx), RootType: rootType, RootID: rootID, RootName: name, FormerPath: raw, Now: now,
	})
	if err != nil {
		return uuid.Nil, fmt.Errorf("insert trash batch: %w", err)
	}
	return id, nil
}

func (r *repo) LockBatch(ctx context.Context, id uuid.UUID) (Batch, error) {
	row, err := r.q.LockTrashBatch(ctx, sqlc.LockTrashBatchParams{ID: id, WorkspaceID: scope.WorkspaceID(ctx)})
	if err != nil {
		return Batch{}, mapNotFound(err, "trash entry")
	}
	return Batch{ID: row.ID, RootType: row.RootType, RootID: row.RootID, RootName: row.RootName, DeletedAt: row.DeletedAt}, nil
}

func (r *repo) DeleteBatch(ctx context.Context, id uuid.UUID) error {
	if err := r.q.DeleteTrashBatch(ctx, sqlc.DeleteTrashBatchParams{ID: id, WorkspaceID: scope.WorkspaceID(ctx)}); err != nil {
		return fmt.Errorf("delete trash batch: %w", err)
	}
	return nil
}

func (r *repo) DeleteOrphanBatches(ctx context.Context) error {
	if err := r.q.DeleteOrphanTrashBatches(ctx, scope.WorkspaceID(ctx)); err != nil {
		return fmt.Errorf("delete orphan trash batches: %w", err)
	}
	return nil
}

func (r *repo) BatchIDs(ctx context.Context) ([]uuid.UUID, error) {
	ids, err := r.q.ListTrashBatchIDs(ctx, scope.WorkspaceID(ctx))
	if err != nil {
		return nil, fmt.Errorf("list trash batch ids: %w", err)
	}
	return ids, nil
}

func (r *repo) ExpiredBatchIDs(ctx context.Context, cutoff time.Time) ([]uuid.UUID, error) {
	ids, err := r.q.ListExpiredTrashBatchIDs(ctx, sqlc.ListExpiredTrashBatchIDsParams{WorkspaceID: scope.WorkspaceID(ctx), Cutoff: cutoff})
	if err != nil {
		return nil, fmt.Errorf("list expired trash batch ids: %w", err)
	}
	return ids, nil
}

func (r *repo) RetentionDays(ctx context.Context) (*int, error) {
	days, err := r.q.GetTrashRetentionDays(ctx, scope.WorkspaceID(ctx))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read trash retention: %w", err)
	}
	if days == nil {
		return nil, nil
	}
	v := int(*days)
	return &v, nil
}

func (r *repo) ListBatches(ctx context.Context, limit, offset int, only []uuid.UUID) ([]Entry, error) {
	rows, err := r.q.ListBatches(ctx, sqlc.ListBatchesParams{WorkspaceID: scope.WorkspaceID(ctx), OnlyIds: only, PageLimit: int32(limit), PageOffset: int32(offset)})
	if err != nil {
		return nil, fmt.Errorf("list trash: %w", err)
	}
	out := make([]Entry, 0, len(rows))
	for _, row := range rows {
		path, err := decodePath(row.FormerPath)
		if err != nil {
			return nil, err
		}
		out = append(out, Entry{
			ID: row.ID, RootType: row.RootType, RootID: row.RootID, RootName: row.RootName, FormerPath: path, DeletedAt: row.DeletedAt,
			Folders: int(row.FolderCount), Lists: int(row.ListCount), Items: int(row.ItemCount), RestorableInPlace: row.RestorableInPlace,
		})
	}
	return out, nil
}

func (r *repo) CountBatches(ctx context.Context, only []uuid.UUID) (int, error) {
	total, err := r.q.CountBatches(ctx, sqlc.CountBatchesParams{WorkspaceID: scope.WorkspaceID(ctx), OnlyIds: only})
	if err != nil {
		return 0, fmt.Errorf("count trash: %w", err)
	}
	return int(total), nil
}

func (r *repo) FolderParents(ctx context.Context) ([]FolderNode, error) {
	rows, err := r.q.ListFolderStates(ctx, scope.WorkspaceID(ctx))
	if err != nil {
		return nil, fmt.Errorf("list folders: %w", err)
	}
	out := make([]FolderNode, 0, len(rows))
	for _, row := range rows {
		out = append(out, FolderNode{ID: row.ID, ParentID: fromNullUUID(row.ParentFolderID), Name: row.Name, Trashed: row.Trashed})
	}
	return out, nil
}

// LockFolder is SEAM: moves to folders.
func (r *repo) LockFolder(ctx context.Context, id uuid.UUID) (FolderRow, error) {
	row, err := r.q.LockFolder(ctx, sqlc.LockFolderParams{ID: id, WorkspaceID: scope.WorkspaceID(ctx)})
	if err != nil {
		return FolderRow{}, mapNotFound(err, "folder")
	}
	return toFolderRow(row), nil
}

// LockList is SEAM: moves to lists.
func (r *repo) LockList(ctx context.Context, id uuid.UUID) (ListRow, error) {
	row, err := r.q.LockList(ctx, sqlc.LockListParams{ID: id, WorkspaceID: scope.WorkspaceID(ctx)})
	if err != nil {
		return ListRow{}, mapNotFound(err, "list")
	}
	return toListRow(row), nil
}

// LockItem is SEAM: moves to items.
func (r *repo) LockItem(ctx context.Context, id uuid.UUID) (ItemRow, error) {
	row, err := r.q.LockItem(ctx, sqlc.LockItemParams{ID: id, WorkspaceID: scope.WorkspaceID(ctx)})
	if err != nil {
		return ItemRow{}, mapNotFound(err, "item")
	}
	return toItemRow(row), nil
}

// GetActiveFolder is SEAM: moves to folders.
func (r *repo) GetActiveFolder(ctx context.Context, id uuid.UUID) (FolderRow, error) {
	row, err := r.q.GetFolder(ctx, sqlc.GetFolderParams{ID: id, WorkspaceID: scope.WorkspaceID(ctx)})
	if err != nil {
		return FolderRow{}, mapNotFound(err, "folder")
	}
	return toFolderRow(row), nil
}

// GetActiveList is SEAM: moves to lists.
func (r *repo) GetActiveList(ctx context.Context, id uuid.UUID) (ListRow, error) {
	row, err := r.q.GetActiveListForItems(ctx, sqlc.GetActiveListForItemsParams{ID: id, WorkspaceID: scope.WorkspaceID(ctx)})
	if err != nil {
		return ListRow{}, mapNotFound(err, "list")
	}
	return ListRow{ID: row.ID, Name: row.Name, FolderID: fromNullUUID(row.FolderID)}, nil
}

func (r *repo) GetAnyFolderState(ctx context.Context, id uuid.UUID) (ParentState, error) {
	row, err := r.q.GetAnyFolderState(ctx, sqlc.GetAnyFolderStateParams{ID: id, WorkspaceID: scope.WorkspaceID(ctx)})
	if err != nil {
		return ParentState{}, mapNotFound(err, "folder")
	}
	return ParentState{Name: row.Name, Trashed: row.Trashed}, nil
}

func (r *repo) GetAnyListState(ctx context.Context, id uuid.UUID) (ParentState, error) {
	row, err := r.q.GetAnyListState(ctx, sqlc.GetAnyListStateParams{ID: id, WorkspaceID: scope.WorkspaceID(ctx)})
	if err != nil {
		return ParentState{}, mapNotFound(err, "list")
	}
	return ParentState{Name: row.Name, Trashed: row.Trashed}, nil
}

// NextFolderSortOrder is SEAM: moves to folders.
func (r *repo) NextFolderSortOrder(ctx context.Context, parent *uuid.UUID) (int, error) {
	next, err := r.q.NextFolderSortOrder(ctx, sqlc.NextFolderSortOrderParams{WorkspaceID: scope.WorkspaceID(ctx), ParentFolderID: toNullUUID(parent)})
	if err != nil {
		return 0, fmt.Errorf("next folder sort order: %w", err)
	}
	return int(next), nil
}

// NextListSortOrder is SEAM: moves to lists.
func (r *repo) NextListSortOrder(ctx context.Context, folder *uuid.UUID) (int, error) {
	next, err := r.q.NextListSortOrder(ctx, sqlc.NextListSortOrderParams{WorkspaceID: scope.WorkspaceID(ctx), FolderID: toNullUUID(folder)})
	if err != nil {
		return 0, fmt.Errorf("next list sort order: %w", err)
	}
	return int(next), nil
}

// NextItemSortOrder is SEAM: moves to items.
func (r *repo) NextItemSortOrder(ctx context.Context, listID uuid.UUID) (int, error) {
	next, err := r.q.NextItemSortOrder(ctx, sqlc.NextItemSortOrderParams{ListID: listID, WorkspaceID: scope.WorkspaceID(ctx)})
	if err != nil {
		return 0, fmt.Errorf("next item sort order: %w", err)
	}
	return int(next), nil
}

// MarkFolders is SEAM: moves to folders.
func (r *repo) MarkFolders(ctx context.Context, ids []uuid.UUID, batch uuid.UUID, now time.Time) error {
	if err := r.q.MarkFoldersTrashed(ctx, sqlc.MarkFoldersTrashedParams{
		Now: &now, BatchID: validUUID(batch), WorkspaceID: scope.WorkspaceID(ctx), Ids: ids,
	}); err != nil {
		return fmt.Errorf("trash folders: %w", err)
	}
	return nil
}

// MarkLists is SEAM: moves to lists.
func (r *repo) MarkLists(ctx context.Context, ids []uuid.UUID, batch uuid.UUID, now time.Time) error {
	if err := r.q.MarkListsTrashed(ctx, sqlc.MarkListsTrashedParams{
		Now: &now, BatchID: validUUID(batch), WorkspaceID: scope.WorkspaceID(ctx), Ids: ids,
	}); err != nil {
		return fmt.Errorf("trash lists: %w", err)
	}
	return nil
}

// MarkItems is SEAM: moves to items.
func (r *repo) MarkItems(ctx context.Context, ids []uuid.UUID, batch uuid.UUID, now time.Time) error {
	if err := r.q.MarkItemsTrashed(ctx, sqlc.MarkItemsTrashedParams{
		Now: &now, BatchID: validUUID(batch), WorkspaceID: scope.WorkspaceID(ctx), Ids: ids,
	}); err != nil {
		return fmt.Errorf("trash items: %w", err)
	}
	return nil
}

// ActiveListIDsByFolders is SEAM: moves to lists.
func (r *repo) ActiveListIDsByFolders(ctx context.Context, folderIDs []uuid.UUID) ([]uuid.UUID, error) {
	ids, err := r.q.ListActiveListIDsByFolders(ctx, sqlc.ListActiveListIDsByFoldersParams{WorkspaceID: scope.WorkspaceID(ctx), FolderIds: folderIDs})
	if err != nil {
		return nil, fmt.Errorf("active lists of folders: %w", err)
	}
	return ids, nil
}

// ActiveItemIDsByLists is SEAM: moves to items.
func (r *repo) ActiveItemIDsByLists(ctx context.Context, listIDs []uuid.UUID) ([]uuid.UUID, error) {
	ids, err := r.q.ListActiveItemIDsByLists(ctx, sqlc.ListActiveItemIDsByListsParams{WorkspaceID: scope.WorkspaceID(ctx), ListIds: listIDs})
	if err != nil {
		return nil, fmt.Errorf("active items of lists: %w", err)
	}
	return ids, nil
}

func (r *repo) GetTrashedFolder(ctx context.Context, id, batch uuid.UUID) (FolderRow, error) {
	row, err := r.q.GetTrashedFolder(ctx, sqlc.GetTrashedFolderParams{ID: id, WorkspaceID: scope.WorkspaceID(ctx), BatchID: validUUID(batch)})
	if err != nil {
		return FolderRow{}, mapNotFound(err, "trash entry")
	}
	return toFolderRow(row), nil
}

// GetTrashedList is SEAM: moves to lists.
func (r *repo) GetTrashedList(ctx context.Context, id, batch uuid.UUID) (ListRow, error) {
	row, err := r.q.GetTrashedList(ctx, sqlc.GetTrashedListParams{ID: id, WorkspaceID: scope.WorkspaceID(ctx), BatchID: validUUID(batch)})
	if err != nil {
		return ListRow{}, mapNotFound(err, "trash entry")
	}
	return toListRow(row), nil
}

// GetTrashedItem is SEAM: moves to items.
func (r *repo) GetTrashedItem(ctx context.Context, id, batch uuid.UUID) (ItemRow, error) {
	row, err := r.q.GetTrashedItem(ctx, sqlc.GetTrashedItemParams{ID: id, WorkspaceID: scope.WorkspaceID(ctx), BatchID: validUUID(batch)})
	if err != nil {
		return ItemRow{}, mapNotFound(err, "trash entry")
	}
	return toItemRow(row), nil
}

// RestoreFolderRoot is SEAM: moves to folders.
func (r *repo) RestoreFolderRoot(ctx context.Context, id, batch uuid.UUID, parent *uuid.UUID, sortOrder int, now time.Time) error {
	if err := r.q.RestoreFolderRoot(ctx, sqlc.RestoreFolderRootParams{
		ParentFolderID: toNullUUID(parent), CallerUserID: callerID(ctx), SortOrder: int32(sortOrder), Now: now, ID: id, WorkspaceID: scope.WorkspaceID(ctx), BatchID: validUUID(batch),
	}); err != nil {
		return fmt.Errorf("restore folder root: %w", err)
	}
	return nil
}

// RestoreListRoot is SEAM: moves to lists.
func (r *repo) RestoreListRoot(ctx context.Context, id, batch uuid.UUID, folder *uuid.UUID, sortOrder int, now time.Time) error {
	if err := r.q.RestoreListRoot(ctx, sqlc.RestoreListRootParams{
		FolderID: toNullUUID(folder), CallerUserID: callerID(ctx), SortOrder: int32(sortOrder), Now: now, ID: id, WorkspaceID: scope.WorkspaceID(ctx), BatchID: validUUID(batch),
	}); err != nil {
		return fmt.Errorf("restore list root: %w", err)
	}
	return nil
}

// RestoreItemRoot is SEAM: moves to items.
func (r *repo) RestoreItemRoot(ctx context.Context, id, batch, listID uuid.UUID, sortOrder int, now time.Time) error {
	if err := r.q.RestoreItemRoot(ctx, sqlc.RestoreItemRootParams{
		ListID: listID, SortOrder: int32(sortOrder), Now: now, ID: id, CallerUserID: callerID(ctx), WorkspaceID: scope.WorkspaceID(ctx), BatchID: validUUID(batch),
	}); err != nil {
		return fmt.Errorf("restore item root: %w", err)
	}
	return nil
}

// RestoreBatchFolders is SEAM: moves to folders.
func (r *repo) RestoreBatchFolders(ctx context.Context, batch uuid.UUID, now time.Time) error {
	if err := r.q.RestoreBatchFolders(ctx, sqlc.RestoreBatchFoldersParams{Now: now, WorkspaceID: scope.WorkspaceID(ctx), BatchID: validUUID(batch)}); err != nil {
		return fmt.Errorf("restore folders: %w", err)
	}
	return nil
}

// RestoreBatchLists is SEAM: moves to lists.
func (r *repo) RestoreBatchLists(ctx context.Context, batch uuid.UUID, now time.Time) error {
	if err := r.q.RestoreBatchLists(ctx, sqlc.RestoreBatchListsParams{Now: now, WorkspaceID: scope.WorkspaceID(ctx), BatchID: validUUID(batch)}); err != nil {
		return fmt.Errorf("restore lists: %w", err)
	}
	return nil
}

// RestoreBatchItems is SEAM: moves to items.
func (r *repo) RestoreBatchItems(ctx context.Context, batch uuid.UUID, now time.Time) error {
	if err := r.q.RestoreBatchItems(ctx, sqlc.RestoreBatchItemsParams{Now: now, WorkspaceID: scope.WorkspaceID(ctx), BatchID: validUUID(batch)}); err != nil {
		return fmt.Errorf("restore items: %w", err)
	}
	return nil
}

// ListListIDsByFolders is SEAM: moves to lists.
func (r *repo) ListListIDsByFolders(ctx context.Context, folderIDs []uuid.UUID) ([]uuid.UUID, error) {
	ids, err := r.q.ListListIDsByFolders(ctx, sqlc.ListListIDsByFoldersParams{WorkspaceID: scope.WorkspaceID(ctx), FolderIds: folderIDs})
	if err != nil {
		return nil, fmt.Errorf("collect lists: %w", err)
	}
	return ids, nil
}

// ListItemIDsByLists is SEAM: moves to items.
func (r *repo) ListItemIDsByLists(ctx context.Context, listIDs []uuid.UUID) ([]uuid.UUID, error) {
	ids, err := r.q.ListItemIDsByLists(ctx, sqlc.ListItemIDsByListsParams{WorkspaceID: scope.WorkspaceID(ctx), ListIds: listIDs})
	if err != nil {
		return nil, fmt.Errorf("collect items: %w", err)
	}
	return ids, nil
}

func (r *repo) ListAttachmentFiles(ctx context.Context, itemIDs []uuid.UUID) ([]AttachmentFile, error) {
	rows, err := r.q.ListAttachmentKeysByItems(ctx, sqlc.ListAttachmentKeysByItemsParams{WorkspaceID: scope.WorkspaceID(ctx), ItemIds: itemIDs})
	if err != nil {
		return nil, fmt.Errorf("collect attachments: %w", err)
	}
	out := make([]AttachmentFile, 0, len(rows))
	for _, row := range rows {
		out = append(out, AttachmentFile{ID: row.ID, StorageKey: row.StorageKey, ThumbnailStorageKey: row.ThumbnailStorageKey})
	}
	return out, nil
}

// DeleteContentVersions is SEAM: moves to items.
func (r *repo) DeleteContentVersions(ctx context.Context, itemIDs []uuid.UUID) error {
	if err := r.q.DeleteContentVersionsOfItems(ctx, itemIDs); err != nil {
		return fmt.Errorf("delete content versions: %w", err)
	}
	return nil
}

// DeleteLookupImports is SEAM: moves to items.
func (r *repo) DeleteLookupImports(ctx context.Context, itemIDs []uuid.UUID) error {
	if err := r.q.DeleteLookupImportsOfItems(ctx, sqlc.DeleteLookupImportsOfItemsParams{WorkspaceID: scope.WorkspaceID(ctx), ItemIds: itemIDs}); err != nil {
		return fmt.Errorf("delete lookup imports: %w", err)
	}
	return nil
}

// DeleteItems is SEAM: moves to items.
func (r *repo) DeleteItems(ctx context.Context, itemIDs []uuid.UUID) error {
	if err := r.q.DeleteItems(ctx, sqlc.DeleteItemsParams{WorkspaceID: scope.WorkspaceID(ctx), ItemIds: itemIDs}); err != nil {
		return fmt.Errorf("delete items: %w", err)
	}
	return nil
}

// DeleteLists is SEAM: moves to lists.
func (r *repo) DeleteLists(ctx context.Context, listIDs []uuid.UUID) error {
	if err := r.q.DeleteLists(ctx, sqlc.DeleteListsParams{WorkspaceID: scope.WorkspaceID(ctx), ListIds: listIDs}); err != nil {
		return fmt.Errorf("delete lists: %w", err)
	}
	return nil
}

// DeleteFolderMembers is SEAM: moves to folders.
func (r *repo) DeleteFolderMembers(ctx context.Context, folderIDs []uuid.UUID) error {
	if err := r.q.DeleteFolderMembersOf(ctx, sqlc.DeleteFolderMembersOfParams{WorkspaceID: scope.WorkspaceID(ctx), Ids: folderIDs}); err != nil {
		return fmt.Errorf("delete folder members: %w", err)
	}
	return nil
}

func (r *repo) FolderVisibleToCaller(ctx context.Context, id uuid.UUID) (bool, error) {
	user, ok := scope.UserID(ctx)
	if !ok {
		return false, nil
	}
	v, err := r.q.FolderVisibleTo(ctx, sqlc.FolderVisibleToParams{ID: id, WorkspaceID: scope.WorkspaceID(ctx), UserID: user})
	if err != nil {
		return false, fmt.Errorf("folder visible: %w", err)
	}
	return v, nil
}

func (r *repo) ListVisibleToCaller(ctx context.Context, id uuid.UUID) (bool, error) {
	user, ok := scope.UserID(ctx)
	if !ok {
		return false, nil
	}
	row, err := r.q.ListBoxRow(ctx, sqlc.ListBoxRowParams{ID: id, WorkspaceID: scope.WorkspaceID(ctx)})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("list box: %w", err)
	}
	if row.FolderID.Valid {
		return r.FolderVisibleToCaller(ctx, row.FolderID.UUID)
	}
	return row.OwnerUserID.Valid && row.OwnerUserID.UUID == user, nil
}

func (r *repo) FolderMemberIDs(ctx context.Context, id uuid.UUID) ([]uuid.UUID, error) {
	rows, err := r.q.ListFolderMembers(ctx, sqlc.ListFolderMembersParams{WorkspaceID: scope.WorkspaceID(ctx), FolderID: id})
	if err != nil {
		return nil, fmt.Errorf("folder members: %w", err)
	}
	out := make([]uuid.UUID, 0, len(rows))
	for _, m := range rows {
		out = append(out, m.UserID)
	}
	return out, nil
}

// DeleteTopOrder is SEAM: moves to access.
func (r *repo) DeleteTopOrder(ctx context.Context, ids []uuid.UUID) error {
	if err := r.q.DeleteUserTopOrderOf(ctx, sqlc.DeleteUserTopOrderOfParams{WorkspaceID: scope.WorkspaceID(ctx), Ids: ids}); err != nil {
		return fmt.Errorf("delete top-level order: %w", err)
	}
	return nil
}

// DeleteFolders is SEAM: moves to folders.
func (r *repo) DeleteFolders(ctx context.Context, folderIDs []uuid.UUID) error {
	if err := r.q.DeleteFolders(ctx, sqlc.DeleteFoldersParams{WorkspaceID: scope.WorkspaceID(ctx), FolderIds: folderIDs}); err != nil {
		return fmt.Errorf("delete folders: %w", err)
	}
	return nil
}

// DeleteAttachmentRows is SEAM: moves to images.
func (r *repo) DeleteAttachmentRows(ctx context.Context, ids []uuid.UUID) error {
	if err := r.q.PurgeAttachmentRows(ctx, sqlc.PurgeAttachmentRowsParams{WorkspaceID: scope.WorkspaceID(ctx), Ids: ids}); err != nil {
		return fmt.Errorf("delete attachments: %w", err)
	}
	return nil
}

// toFolderRow maps a folder row (any read that returns the whole row).
func toFolderRow(f sqlc.Folder) FolderRow {
	return FolderRow{ID: f.ID, Name: f.Name, ParentID: fromNullUUID(f.ParentFolderID), OwnerID: fromNullUUID(f.OwnerUserID), Version: int(f.Version)}
}

// toListRow maps a list row (any read that returns the whole row).
func toListRow(l sqlc.List) ListRow {
	return ListRow{ID: l.ID, Name: l.Name, FolderID: fromNullUUID(l.FolderID), OwnerID: fromNullUUID(l.OwnerUserID), Version: int(l.Version)}
}

// toItemRow maps an item row (any read that returns the whole row).
func toItemRow(i sqlc.Item) ItemRow {
	return ItemRow{ID: i.ID, Title: i.Title, ListID: i.ListID, Version: int(i.Version)}
}

// toNullUUID is a nullable uuid column value (nil is NULL).
func toNullUUID(id *uuid.UUID) uuid.NullUUID {
	if id == nil {
		return uuid.NullUUID{}
	}
	return uuid.NullUUID{UUID: *id, Valid: true}
}

// validUUID is a set nullable uuid column value (for trash_batch_id).
func validUUID(id uuid.UUID) uuid.NullUUID {
	return uuid.NullUUID{UUID: id, Valid: true}
}

// fromNullUUID is the pointer of a nullable uuid column (nil is NULL).
func fromNullUUID(id uuid.NullUUID) *uuid.UUID {
	if !id.Valid {
		return nil
	}
	v := id.UUID
	return &v
}

// decodePath reads a former_path column: a JSON array of path segments.
func decodePath(raw []byte) ([]PathSegment, error) {
	path := []PathSegment{}
	if err := json.Unmarshal(raw, &path); err != nil {
		return nil, fmt.Errorf("decode former path: %w", err)
	}
	return path, nil
}

// mapNotFound maps a missing row to apperr.NotFound and wraps any other error.
func mapNotFound(err error, resource string) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return apperr.NotFound(resource)
	}
	return fmt.Errorf("%s: %w", resource, err)
}

// callerID is the user of the request as a nullable id. A restore to the top level gives the root
// to this user unless it already has an owner (a root restored in place keeps its stored owner).
func callerID(ctx context.Context) uuid.NullUUID {
	id, ok := scope.UserID(ctx)
	return uuid.NullUUID{UUID: id, Valid: ok}
}
