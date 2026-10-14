// Package trash owns deleting folders, lists and items into the trash, listing
// the trash, restoring a batch and purging it for good (DATA_MODEL §4, API.md
// §5). It knows nothing about HTTP: it returns typed errors from internal/apperr.
//
// A delete is one trash batch: the root plus everything active below it get the
// same trash_batch_id. Every delete, restore and purge takes the per-workspace
// tree lock first (D-060), like every other structural write. Purge deletes
// image files only after its transaction committed (ARCHITECTURE §4): a failed
// file delete leaves orphan files, never a row that points at a missing file,
// and the images sweeper removes the leftovers.
package trash

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/0xfederama/collections-app-backend/internal/apperr"
	"github.com/0xfederama/collections-app-backend/internal/clock"
	"github.com/0xfederama/collections-app-backend/internal/database"
	"github.com/0xfederama/collections-app-backend/internal/database/sqlc"
	"github.com/0xfederama/collections-app-backend/internal/items"
	"github.com/0xfederama/collections-app-backend/internal/storage"
)

// Root types of a trash batch (the `root_type` column).
const (
	TypeFolder = "folder"
	TypeList   = "list"
	TypeItem   = "item"
)

// Page sizes follow the other list endpoints.
const (
	DefaultLimit = items.DefaultLimit
	MaxLimit     = items.MaxLimit
)

// DB is satisfied by *pgxpool.Pool.
type DB interface {
	database.TxBeginner
	sqlc.DBTX
}

// Service is the trash service of one workspace.
type Service struct {
	db          DB
	store       storage.FileStore
	clock       clock.Clock
	log         *slog.Logger
	workspaceID uuid.UUID
}

// New returns a Service bound to the single workspace (D-002). store is used
// by Purge to delete image files.
func New(db DB, store storage.FileStore, clk clock.Clock, log *slog.Logger, workspaceID uuid.UUID) *Service {
	return &Service{db: db, store: store, clock: clk, log: log, workspaceID: workspaceID}
}

// PathSegment is one ancestor of a trashed root as it was when deleted.
type PathSegment struct {
	Type string    `json:"type"` // "folder" or "list"
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
}

// Entry is one trash batch as the API shows it.
type Entry struct {
	ID                uuid.UUID
	RootType          string
	RootID            uuid.UUID
	RootName          string
	FormerPath        []PathSegment
	DeletedAt         time.Time
	Folders           int
	Lists             int
	Items             int
	RestorableInPlace bool
}

// Page is one page of trash entries, newest first.
type Page struct {
	Entries       []Entry
	Limit, Offset int
	Total         int
}

// Destination is the optional body of a restore. A folder or list root takes
// FolderID (nil = the workspace root; FolderSet tells absent from null), an
// item root takes ListID.
type Destination struct {
	FolderSet bool
	FolderID  *uuid.UUID
	ListID    *uuid.UUID
}

// Restored names the root that came back.
type Restored struct {
	RootType string
	RootID   uuid.UUID
}

func (s *Service) now() time.Time { return s.clock.Now() }

func ptrTime(t time.Time) *time.Time { return &t }

func (s *Service) lockTree(ctx context.Context, q *sqlc.Queries) error {
	if err := q.LockTree(ctx, s.workspaceID); err != nil {
		return fmt.Errorf("lock tree: %w", err)
	}
	return nil
}

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

func notFoundOr(err error, resource string) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return apperr.NotFound(resource)
	}
	return fmt.Errorf("get %s: %w", resource, err)
}

// ---- delete ----

// DeleteFolder moves an active folder and everything active below it to the
// trash. Errors in order: NotFound, VersionConflict.
func (s *Service) DeleteFolder(ctx context.Context, id uuid.UUID, version int) error {
	return database.InTx(ctx, s.db, func(q *sqlc.Queries) error {
		if err := s.lockTree(ctx, q); err != nil {
			return err
		}
		cur, err := q.LockFolder(ctx, sqlc.LockFolderParams{ID: id, WorkspaceID: s.workspaceID})
		if err != nil {
			return notFoundOr(err, "folder")
		}
		if int(cur.Version) != version {
			return apperr.VersionConflict(int(cur.Version))
		}
		path, err := s.folderPath(ctx, q, ptrUUID(cur.ParentFolderID))
		if err != nil {
			return err
		}
		batch, err := s.insertBatch(ctx, q, TypeFolder, id, cur.Name, path)
		if err != nil {
			return err
		}
		ids, err := q.ActiveFolderSubtreeIDs(ctx, sqlc.ActiveFolderSubtreeIDsParams{RootID: id, WorkspaceID: s.workspaceID})
		if err != nil {
			return fmt.Errorf("folder subtree: %w", err)
		}
		now := s.now()
		if err := q.TrashFolders(ctx, sqlc.TrashFoldersParams{Now: &now, BatchID: uuid.NullUUID{UUID: batch, Valid: true}, WorkspaceID: s.workspaceID, Ids: ids}); err != nil {
			return fmt.Errorf("trash folders: %w", err)
		}
		if err := q.TrashListsInFolders(ctx, sqlc.TrashListsInFoldersParams{Now: &now, BatchID: uuid.NullUUID{UUID: batch, Valid: true}, WorkspaceID: s.workspaceID, FolderIds: ids}); err != nil {
			return fmt.Errorf("trash lists: %w", err)
		}
		return s.trashItemsOfLists(ctx, q, batch)
	})
}

// DeleteList moves an active list and its active items to the trash. Errors in
// order: NotFound, VersionConflict.
func (s *Service) DeleteList(ctx context.Context, id uuid.UUID, version int) error {
	return database.InTx(ctx, s.db, func(q *sqlc.Queries) error {
		if err := s.lockTree(ctx, q); err != nil {
			return err
		}
		cur, err := q.LockList(ctx, sqlc.LockListParams{ID: id, WorkspaceID: s.workspaceID})
		if err != nil {
			return notFoundOr(err, "list")
		}
		if int(cur.Version) != version {
			return apperr.VersionConflict(int(cur.Version))
		}
		path, err := s.folderPath(ctx, q, ptrUUID(cur.FolderID))
		if err != nil {
			return err
		}
		batch, err := s.insertBatch(ctx, q, TypeList, id, cur.Name, path)
		if err != nil {
			return err
		}
		if err := q.TrashList(ctx, sqlc.TrashListParams{Now: ptrTime(s.now()), BatchID: uuid.NullUUID{UUID: batch, Valid: true}, ID: id, WorkspaceID: s.workspaceID}); err != nil {
			return fmt.Errorf("trash list: %w", err)
		}
		return s.trashItemsOfLists(ctx, q, batch)
	})
}

// DeleteItem moves an active item to the trash. Errors in order: NotFound,
// VersionConflict.
func (s *Service) DeleteItem(ctx context.Context, id uuid.UUID, version int) error {
	return database.InTx(ctx, s.db, func(q *sqlc.Queries) error {
		if err := s.lockTree(ctx, q); err != nil {
			return err
		}
		cur, err := q.LockItem(ctx, sqlc.LockItemParams{ID: id, WorkspaceID: s.workspaceID})
		if err != nil {
			return notFoundOr(err, "item")
		}
		if int(cur.Version) != version {
			return apperr.VersionConflict(int(cur.Version))
		}
		list, err := q.GetActiveListForItems(ctx, sqlc.GetActiveListForItemsParams{ID: cur.ListID, WorkspaceID: s.workspaceID})
		if err != nil {
			return fmt.Errorf("item's list: %w", err) // an active item always has an active list
		}
		path, err := s.folderPath(ctx, q, ptrUUID(list.FolderID))
		if err != nil {
			return err
		}
		path = append(path, PathSegment{Type: TypeList, ID: list.ID, Name: list.Name})
		batch, err := s.insertBatch(ctx, q, TypeItem, id, cur.Title, path)
		if err != nil {
			return err
		}
		if err := q.TrashItem(ctx, sqlc.TrashItemParams{Now: ptrTime(s.now()), BatchID: uuid.NullUUID{UUID: batch, Valid: true}, ID: id, WorkspaceID: s.workspaceID}); err != nil {
			return fmt.Errorf("trash item: %w", err)
		}
		return nil
	})
}

// folderPath returns the ancestor folders starting at folder (root first),
// empty for the workspace root.
func (s *Service) folderPath(ctx context.Context, q *sqlc.Queries, folder *uuid.UUID) ([]PathSegment, error) {
	path := []PathSegment{}
	if folder == nil {
		return path, nil
	}
	rows, err := q.FolderPath(ctx, sqlc.FolderPathParams{FolderID: *folder, WorkspaceID: s.workspaceID})
	if err != nil {
		return nil, fmt.Errorf("former path: %w", err)
	}
	for _, r := range rows {
		path = append(path, PathSegment{Type: TypeFolder, ID: r.ID, Name: r.Name})
	}
	return path, nil
}

func (s *Service) insertBatch(ctx context.Context, q *sqlc.Queries, rootType string, rootID uuid.UUID, name string, path []PathSegment) (uuid.UUID, error) {
	raw, err := json.Marshal(path)
	if err != nil {
		return uuid.Nil, fmt.Errorf("encode former path: %w", err)
	}
	id, err := q.InsertTrashBatch(ctx, sqlc.InsertTrashBatchParams{
		WorkspaceID: s.workspaceID, RootType: rootType, RootID: rootID, RootName: name, FormerPath: raw, Now: s.now(),
	})
	if err != nil {
		return uuid.Nil, fmt.Errorf("insert trash batch: %w", err)
	}
	return id, nil
}

// trashItemsOfLists trashes the active items of every list of the batch.
func (s *Service) trashItemsOfLists(ctx context.Context, q *sqlc.Queries, batch uuid.UUID) error {
	err := q.TrashItemsOfBatchLists(ctx, sqlc.TrashItemsOfBatchListsParams{
		Now: ptrTime(s.now()), BatchID: uuid.NullUUID{UUID: batch, Valid: true}, WorkspaceID: s.workspaceID,
	})
	if err != nil {
		return fmt.Errorf("trash items: %w", err)
	}
	return nil
}

// ---- list ----

// List returns the trash batches, newest first. limit 0 means the default.
func (s *Service) List(ctx context.Context, limit, offset int) (Page, error) {
	if limit <= 0 {
		limit = DefaultLimit
	}
	limit = min(limit, MaxLimit)
	offset = max(offset, 0)
	q := sqlc.New(s.db)
	rows, err := q.ListTrashBatches(ctx, sqlc.ListTrashBatchesParams{WorkspaceID: s.workspaceID, PageLimit: int32(limit), PageOffset: int32(offset)})
	if err != nil {
		return Page{}, fmt.Errorf("list trash: %w", err)
	}
	total, err := q.CountTrashBatches(ctx, s.workspaceID)
	if err != nil {
		return Page{}, fmt.Errorf("count trash: %w", err)
	}
	page := Page{Entries: make([]Entry, 0, len(rows)), Limit: limit, Offset: offset, Total: int(total)}
	for _, r := range rows {
		path := []PathSegment{}
		if err := json.Unmarshal(r.FormerPath, &path); err != nil {
			return Page{}, fmt.Errorf("decode former path: %w", err)
		}
		page.Entries = append(page.Entries, Entry{
			ID: r.ID, RootType: r.RootType, RootID: r.RootID, RootName: r.RootName, FormerPath: path, DeletedAt: r.DeletedAt,
			Folders: int(r.FolderCount), Lists: int(r.ListCount), Items: int(r.ItemCount), RestorableInPlace: r.RestorableInPlace,
		})
	}
	return page, nil
}

// ---- restore ----

// Restore brings batch id back. Without a destination the root returns to its
// original parent, or the call fails with ParentInTrash (naming the blocking
// parent). With a destination it is re-parented like a move: DestinationUnavailable
// for a missing or trashed target, Cycle for a folder into its own subtree. The
// root is appended last among its siblings and its version is bumped.
func (s *Service) Restore(ctx context.Context, id uuid.UUID, dest *Destination) (Restored, error) {
	var out Restored
	err := database.InTx(ctx, s.db, func(q *sqlc.Queries) error {
		if err := s.lockTree(ctx, q); err != nil {
			return err
		}
		batch, err := q.LockTrashBatch(ctx, sqlc.LockTrashBatchParams{ID: id, WorkspaceID: s.workspaceID})
		if err != nil {
			return notFoundOr(err, "trash entry")
		}
		out = Restored{RootType: batch.RootType, RootID: batch.RootID}
		bid := uuid.NullUUID{UUID: id, Valid: true}
		now := s.now()
		switch batch.RootType {
		case TypeFolder:
			err = s.restoreFolder(ctx, q, batch, bid, dest, now)
		case TypeList:
			err = s.restoreList(ctx, q, batch, bid, dest, now)
		case TypeItem:
			err = s.restoreItem(ctx, q, batch, bid, dest, now)
		default:
			err = fmt.Errorf("unknown trash root type %q", batch.RootType)
		}
		if err != nil {
			return err
		}
		// The root is back; clear the rest of the batch, then the batch row
		// itself (nothing references it any more, D-051).
		if err := q.RestoreBatchFolders(ctx, sqlc.RestoreBatchFoldersParams{Now: now, WorkspaceID: s.workspaceID, BatchID: bid}); err != nil {
			return fmt.Errorf("restore folders: %w", err)
		}
		if err := q.RestoreBatchLists(ctx, sqlc.RestoreBatchListsParams{Now: now, WorkspaceID: s.workspaceID, BatchID: bid}); err != nil {
			return fmt.Errorf("restore lists: %w", err)
		}
		if err := q.RestoreBatchItems(ctx, sqlc.RestoreBatchItemsParams{Now: now, WorkspaceID: s.workspaceID, BatchID: bid}); err != nil {
			return fmt.Errorf("restore items: %w", err)
		}
		if err := q.DeleteTrashBatch(ctx, sqlc.DeleteTrashBatchParams{ID: id, WorkspaceID: s.workspaceID}); err != nil {
			return fmt.Errorf("delete trash batch: %w", err)
		}
		return nil
	})
	return out, err
}

func (s *Service) restoreFolder(ctx context.Context, q *sqlc.Queries, b sqlc.TrashBatch, bid uuid.NullUUID, dest *Destination, now time.Time) error {
	root, err := q.GetTrashedFolder(ctx, sqlc.GetTrashedFolderParams{ID: b.RootID, WorkspaceID: s.workspaceID, BatchID: bid})
	if err != nil {
		return notFoundOr(err, "trash entry")
	}
	parent, err := s.folderTarget(ctx, q, ptrUUID(root.ParentFolderID), dest, &b.RootID)
	if err != nil {
		return err
	}
	next, err := q.NextFolderSortOrder(ctx, sqlc.NextFolderSortOrderParams{WorkspaceID: s.workspaceID, ParentFolderID: nullUUID(parent)})
	if err != nil {
		return fmt.Errorf("next folder sort order: %w", err)
	}
	if err := q.RestoreFolderRoot(ctx, sqlc.RestoreFolderRootParams{
		ParentFolderID: nullUUID(parent), SortOrder: next, Now: now, ID: b.RootID, WorkspaceID: s.workspaceID, BatchID: bid,
	}); err != nil {
		return fmt.Errorf("restore folder root: %w", err)
	}
	return nil
}

func (s *Service) restoreList(ctx context.Context, q *sqlc.Queries, b sqlc.TrashBatch, bid uuid.NullUUID, dest *Destination, now time.Time) error {
	root, err := q.GetTrashedList(ctx, sqlc.GetTrashedListParams{ID: b.RootID, WorkspaceID: s.workspaceID, BatchID: bid})
	if err != nil {
		return notFoundOr(err, "trash entry")
	}
	folder, err := s.folderTarget(ctx, q, ptrUUID(root.FolderID), dest, nil)
	if err != nil {
		return err
	}
	next, err := q.NextListSortOrder(ctx, sqlc.NextListSortOrderParams{WorkspaceID: s.workspaceID, FolderID: nullUUID(folder)})
	if err != nil {
		return fmt.Errorf("next list sort order: %w", err)
	}
	if err := q.RestoreListRoot(ctx, sqlc.RestoreListRootParams{
		FolderID: nullUUID(folder), SortOrder: next, Now: now, ID: b.RootID, WorkspaceID: s.workspaceID, BatchID: bid,
	}); err != nil {
		return fmt.Errorf("restore list root: %w", err)
	}
	return nil
}

func (s *Service) restoreItem(ctx context.Context, q *sqlc.Queries, b sqlc.TrashBatch, bid uuid.NullUUID, dest *Destination, now time.Time) error {
	root, err := q.GetTrashedItem(ctx, sqlc.GetTrashedItemParams{ID: b.RootID, WorkspaceID: s.workspaceID, BatchID: bid})
	if err != nil {
		return notFoundOr(err, "trash entry")
	}
	list := root.ListID
	if dest != nil {
		if fe := destinationErrors(TypeItem, dest); fe != nil {
			return apperr.Validation(*fe)
		}
		list = *dest.ListID
		if _, err := q.GetActiveListForItems(ctx, sqlc.GetActiveListForItemsParams{ID: list, WorkspaceID: s.workspaceID}); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return apperr.DestinationUnavailable()
			}
			return fmt.Errorf("destination list: %w", err)
		}
	} else if _, err := q.GetActiveListForItems(ctx, sqlc.GetActiveListForItemsParams{ID: list, WorkspaceID: s.workspaceID}); err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("original list: %w", err)
		}
		return s.blockedBy(ctx, q, TypeList, list)
	}
	next, err := q.NextItemSortOrder(ctx, sqlc.NextItemSortOrderParams{ListID: list, WorkspaceID: s.workspaceID})
	if err != nil {
		return fmt.Errorf("next item sort order: %w", err)
	}
	if err := q.RestoreItemRoot(ctx, sqlc.RestoreItemRootParams{
		ListID: list, SortOrder: next, Now: now, ID: b.RootID, WorkspaceID: s.workspaceID, BatchID: bid,
	}); err != nil {
		return fmt.Errorf("restore item root: %w", err)
	}
	return nil
}

// folderTarget picks the parent folder (nil = workspace root) for a restored
// folder or list. rootFolder is set for a folder root (cycle check).
func (s *Service) folderTarget(ctx context.Context, q *sqlc.Queries, original *uuid.UUID, dest *Destination, rootFolder *uuid.UUID) (*uuid.UUID, error) {
	rootType := TypeList
	if rootFolder != nil {
		rootType = TypeFolder
	}
	if dest == nil {
		if original == nil {
			return nil, nil
		}
		if _, err := q.GetFolder(ctx, sqlc.GetFolderParams{ID: *original, WorkspaceID: s.workspaceID}); err != nil {
			if !errors.Is(err, pgx.ErrNoRows) {
				return nil, fmt.Errorf("original parent: %w", err)
			}
			return nil, s.blockedBy(ctx, q, TypeFolder, *original)
		}
		return original, nil
	}
	if fe := destinationErrors(rootType, dest); fe != nil {
		return nil, apperr.Validation(*fe)
	}
	parent := dest.FolderID
	if parent == nil {
		return nil, nil
	}
	if rootFolder != nil {
		// Into the folder itself or a descendant, whatever its state: a cycle,
		// not just an unavailable target (descendants are in the trash too).
		n, err := q.FolderIsInSubtreeOf(ctx, sqlc.FolderIsInSubtreeOfParams{CandidateID: *parent, WorkspaceID: s.workspaceID, FolderID: *rootFolder})
		if err != nil {
			return nil, fmt.Errorf("cycle check: %w", err)
		}
		if n > 0 {
			return nil, apperr.Cycle()
		}
	}
	if _, err := q.GetFolder(ctx, sqlc.GetFolderParams{ID: *parent, WorkspaceID: s.workspaceID}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apperr.DestinationUnavailable()
		}
		return nil, fmt.Errorf("destination folder: %w", err)
	}
	return parent, nil
}

// blockedBy builds the parent_in_trash conflict for an original parent that is
// not active. A parent that no longer exists cannot be named (the foreign keys
// make that unreachable: purging a parent purges its trashed children too).
func (s *Service) blockedBy(ctx context.Context, q *sqlc.Queries, kind string, id uuid.UUID) error {
	var (
		name string
		err  error
	)
	if kind == TypeFolder {
		var row sqlc.GetAnyFolderStateRow
		row, err = q.GetAnyFolderState(ctx, sqlc.GetAnyFolderStateParams{ID: id, WorkspaceID: s.workspaceID})
		name = row.Name
	} else {
		var row sqlc.GetAnyListStateRow
		row, err = q.GetAnyListState(ctx, sqlc.GetAnyListStateParams{ID: id, WorkspaceID: s.workspaceID})
		name = row.Name
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return &apperr.ConflictError{Code: apperr.CodeParentInTrash}
	}
	if err != nil {
		return fmt.Errorf("blocking parent: %w", err)
	}
	return apperr.ParentInTrash(apperr.BlockingParent{Type: kind, ID: id.String(), Name: name})
}

// destinationErrors checks that dest names the key its root type needs.
func destinationErrors(rootType string, d *Destination) *apperr.FieldError {
	if rootType == TypeItem {
		switch {
		case d.FolderSet:
			return &apperr.FieldError{Field: "destination.folder_id", Message: "an item is restored into a list: use list_id"}
		case d.ListID == nil:
			return &apperr.FieldError{Field: "destination.list_id", Message: "is required to restore an item to another list"}
		}
		return nil
	}
	switch {
	case d.ListID != nil:
		return &apperr.FieldError{Field: "destination.list_id", Message: "a " + rootType + " is restored into a folder: use folder_id"}
	case !d.FolderSet:
		return &apperr.FieldError{Field: "destination.folder_id", Message: "is required (null restores to the root)"}
	}
	return nil
}

// ---- purge ----

// Purge deletes batch id for good: the root with its whole subtree (every
// batch inside it), the attachment rows of its items and, after the commit,
// their image files. A file that cannot be deleted is logged; the images
// sweeper removes it later.
func (s *Service) Purge(ctx context.Context, id uuid.UUID) error {
	var keys []string
	err := database.InTx(ctx, s.db, func(q *sqlc.Queries) error {
		if err := s.lockTree(ctx, q); err != nil {
			return err
		}
		batch, err := q.LockTrashBatch(ctx, sqlc.LockTrashBatchParams{ID: id, WorkspaceID: s.workspaceID})
		if err != nil {
			return notFoundOr(err, "trash entry")
		}
		atts, err := q.PurgeAttachments(ctx, sqlc.PurgeAttachmentsParams{RootType: batch.RootType, RootID: batch.RootID, WorkspaceID: s.workspaceID})
		if err != nil {
			return fmt.Errorf("collect attachments: %w", err)
		}
		switch batch.RootType {
		case TypeFolder:
			_, err = q.PurgeFolder(ctx, sqlc.PurgeFolderParams{ID: batch.RootID, WorkspaceID: s.workspaceID})
		case TypeList:
			_, err = q.PurgeList(ctx, sqlc.PurgeListParams{ID: batch.RootID, WorkspaceID: s.workspaceID})
		case TypeItem:
			_, err = q.PurgeItem(ctx, sqlc.PurgeItemParams{ID: batch.RootID, WorkspaceID: s.workspaceID})
		default:
			err = fmt.Errorf("unknown trash root type %q", batch.RootType)
		}
		if err != nil {
			return fmt.Errorf("purge root: %w", err)
		}
		ids := make([]uuid.UUID, 0, len(atts))
		keys = keys[:0]
		for _, a := range atts {
			ids = append(ids, a.ID)
			keys = append(keys, a.StorageKey, a.ThumbnailStorageKey)
		}
		if err := q.PurgeAttachmentRows(ctx, sqlc.PurgeAttachmentRowsParams{WorkspaceID: s.workspaceID, Ids: ids}); err != nil {
			return fmt.Errorf("delete attachments: %w", err)
		}
		// Batch id first (it may still be the root's own row, already gone),
		// then batches nested in the subtree whose roots went with it.
		if err := q.DeleteTrashBatch(ctx, sqlc.DeleteTrashBatchParams{ID: id, WorkspaceID: s.workspaceID}); err != nil {
			return fmt.Errorf("delete trash batch: %w", err)
		}
		if err := q.DeleteOrphanTrashBatches(ctx, s.workspaceID); err != nil {
			return fmt.Errorf("delete nested trash batches: %w", err)
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, k := range keys {
		if err := s.store.Delete(k); err != nil {
			s.log.WarnContext(context.WithoutCancel(ctx), "image file cleanup after purge failed; the sweeper will retry", "err", err)
		}
	}
	return nil
}
