package trash

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/0xfederama/collections-app-backend/internal/apperr"
	"github.com/0xfederama/collections-app-backend/internal/platform/clock"
	"github.com/0xfederama/collections-app-backend/internal/platform/scope"
	"github.com/0xfederama/collections-app-backend/internal/storage"
)

// Store is what the trash rules need from the database: a transaction for every
// write (the Repo of the transaction), and the reads that run on the pool.
type Store interface {
	// InTx runs fn with a Repo of one transaction: commit on nil, rollback on error.
	InTx(ctx context.Context, fn func(Repo) error) error
	// ListBatches returns one page of batches, newest first, with their counts. only, when not nil,
	// restricts the batches to those ids; an empty non-nil slice is none.
	ListBatches(ctx context.Context, limit, offset int, only []uuid.UUID) ([]Entry, error)
	// CountBatches returns the number of batches of the workspace, restricted like ListBatches.
	CountBatches(ctx context.Context, only []uuid.UUID) (int, error)
	// RetentionDays is the workspace's trash_retention_days (nil = off).
	RetentionDays(ctx context.Context) (*int, error)
}

var _ Store = (*Postgres)(nil)

// Service holds the trash rules of one workspace (the workspace comes from the
// context). Every delete, restore and purge takes the tree lock first (D-060) and
// does its reads and writes through the Repo of one transaction. Files are deleted
// after the commit (ARCHITECTURE §4).
type Service struct {
	store  Store
	files  storage.FileStore
	clock  clock.Clock
	log    *slog.Logger
	access Access
}

// Access answers what the caller may see (internal/access, wired in httpapi). It is read before
// the transaction, never inside it: a second pool connection could stall the pool.
type Access interface {
	FolderVisible(ctx context.Context, id uuid.UUID) (bool, error)
	ListVisible(ctx context.Context, id uuid.UUID) (bool, error)
	// VisibleBatchIDs returns the batches whose root's box the caller may see. Never nil.
	VisibleBatchIDs(ctx context.Context) ([]uuid.UUID, error)
}

// WithAccess returns the service filtering the trash list and "empty trash" to the batches the
// caller may see, and checking restore destinations. The auto-purge uses the service without it:
// it works on the whole workspace.
func (t *Service) WithAccess(a Access) *Service {
	c := *t
	c.access = a
	return &c
}

// visibleBatches returns the ids the caller may see, or nil for a service without access (no filter).
func (t *Service) visibleBatches(ctx context.Context) ([]uuid.UUID, error) {
	if t.access == nil {
		return nil, nil
	}
	ids, err := t.access.VisibleBatchIDs(ctx)
	if err != nil {
		return nil, fmt.Errorf("visible trash batches: %w", err)
	}
	return ids, nil
}

// NewService returns the rules over store. files deletes the image files of purged
// items after the commit.
func NewService(store Store, files storage.FileStore, clk clock.Clock, log *slog.Logger) *Service {
	return &Service{store: store, files: files, clock: clk, log: log}
}

func (t *Service) now() time.Time { return t.clock.Now() }

// errUnscopedTrash is returned when the context carries no workspace. The queries
// would match no row for it, but a delete or purge must not depend on that: the
// trash refuses to run before any read or write.
var errUnscopedTrash = errors.New("trash: operation needs a workspace in the context")

// requireWorkspace returns errUnscopedTrash for a context with no workspace.
func requireWorkspace(ctx context.Context) error {
	if scope.WorkspaceID(ctx) == uuid.Nil {
		return errUnscopedTrash
	}
	return nil
}

// isNotFound reports whether err is a missing or trashed resource.
func isNotFound(err error) bool {
	var nf *apperr.NotFoundError
	return errors.As(err, &nf)
}

// ancestorPath is the former path of a row whose parent folder is folder (nil =
// the workspace root): the active ancestors, root first. Never nil.
func ancestorPath(nodes []FolderNode, folder *uuid.UUID) []PathSegment {
	if folder == nil {
		return []PathSegment{}
	}
	return activePath(nodes, *folder)
}

// ---- delete ----

// DeleteFolder moves an active folder and everything active below it to the
// trash as one batch. Order: the folders, then the lists directly in them, then
// the items of those lists. Older batches nested below stay as they are. Errors
// in order: NotFound, VersionConflict.
func (t *Service) DeleteFolder(ctx context.Context, id uuid.UUID, version int) error {
	if err := requireWorkspace(ctx); err != nil {
		return err
	}
	return t.store.InTx(ctx, func(r Repo) error {
		if err := r.LockTree(ctx); err != nil {
			return err
		}
		cur, err := r.LockFolder(ctx, id)
		if err != nil {
			return err
		}
		if cur.Version != version {
			return apperr.VersionConflict(cur.Version)
		}
		nodes, err := r.FolderParents(ctx)
		if err != nil {
			return err
		}
		now := t.now()
		batch, err := r.InsertBatch(ctx, TypeFolder, id, cur.Name, ancestorPath(nodes, cur.ParentID), now)
		if err != nil {
			return err
		}
		folders := activeSubtree(nodes, id)
		if err := r.MarkFolders(ctx, folders, batch, now); err != nil {
			return err
		}
		lists, err := r.ActiveListIDsByFolders(ctx, folders)
		if err != nil {
			return err
		}
		if err := r.MarkLists(ctx, lists, batch, now); err != nil {
			return err
		}
		return t.trashItemsOf(ctx, r, lists, batch, now)
	})
}

// DeleteList moves an active list and its active items to the trash as one batch.
// Errors in order: NotFound, VersionConflict.
func (t *Service) DeleteList(ctx context.Context, id uuid.UUID, version int) error {
	if err := requireWorkspace(ctx); err != nil {
		return err
	}
	return t.store.InTx(ctx, func(r Repo) error {
		if err := r.LockTree(ctx); err != nil {
			return err
		}
		cur, err := r.LockList(ctx, id)
		if err != nil {
			return err
		}
		if cur.Version != version {
			return apperr.VersionConflict(cur.Version)
		}
		nodes, err := r.FolderParents(ctx)
		if err != nil {
			return err
		}
		now := t.now()
		batch, err := r.InsertBatch(ctx, TypeList, id, cur.Name, ancestorPath(nodes, cur.FolderID), now)
		if err != nil {
			return err
		}
		lists := []uuid.UUID{id}
		if err := r.MarkLists(ctx, lists, batch, now); err != nil {
			return err
		}
		return t.trashItemsOf(ctx, r, lists, batch, now)
	})
}

// DeleteItem moves an active item to the trash as one batch. Errors in order:
// NotFound, VersionConflict.
func (t *Service) DeleteItem(ctx context.Context, id uuid.UUID, version int) error {
	if err := requireWorkspace(ctx); err != nil {
		return err
	}
	return t.store.InTx(ctx, func(r Repo) error {
		if err := r.LockTree(ctx); err != nil {
			return err
		}
		cur, err := r.LockItem(ctx, id)
		if err != nil {
			return err
		}
		if cur.Version != version {
			return apperr.VersionConflict(cur.Version)
		}
		list, err := r.GetActiveList(ctx, cur.ListID)
		if err != nil {
			if isNotFound(err) {
				// An active item always has an active list: not a client error.
				return errors.New("item's list: an active item always has an active list")
			}
			return err
		}
		nodes, err := r.FolderParents(ctx)
		if err != nil {
			return err
		}
		path := append(ancestorPath(nodes, list.FolderID), PathSegment{Type: TypeList, ID: list.ID, Name: list.Name})
		now := t.now()
		batch, err := r.InsertBatch(ctx, TypeItem, id, cur.Title, path, now)
		if err != nil {
			return err
		}
		return r.MarkItems(ctx, []uuid.UUID{id}, batch, now)
	})
}

// trashItemsOf marks the active items of lists, which the batch has just trashed.
func (t *Service) trashItemsOf(ctx context.Context, r Repo, lists []uuid.UUID, batch uuid.UUID, now time.Time) error {
	items, err := r.ActiveItemIDsByLists(ctx, lists)
	if err != nil {
		return err
	}
	return r.MarkItems(ctx, items, batch, now)
}

// ---- list ----

// List returns the trash batches, newest first. limit 0 means the default.
func (t *Service) List(ctx context.Context, limit, offset int) (Page, error) {
	if limit <= 0 {
		limit = DefaultLimit
	}
	limit = min(limit, MaxLimit)
	offset = max(offset, 0)
	only, err := t.visibleBatches(ctx)
	if err != nil {
		return Page{}, err
	}
	entries, err := t.store.ListBatches(ctx, limit, offset, only)
	if err != nil {
		return Page{}, err
	}
	total, err := t.store.CountBatches(ctx, only)
	if err != nil {
		return Page{}, err
	}
	return Page{Entries: entries, Limit: limit, Offset: offset, Total: total}, nil
}

// ---- restore ----

// Restore brings batch id back. Without a destination the root returns to its
// original parent, or the call fails with ParentInTrash (naming the blocking
// parent). With a destination it is re-parented like a move: DestinationUnavailable
// for a missing or trashed target, Cycle for a folder into its own subtree. The
// root is appended last among its siblings and its version is bumped. The rest of
// the batch is cleared, then the batch row is deleted last (D-051).
func (t *Service) Restore(ctx context.Context, id uuid.UUID, dest *Destination) (Restored, error) {
	if err := requireWorkspace(ctx); err != nil {
		return Restored{}, err
	}
	destErr := t.destinationVisible(ctx, dest)
	var out Restored
	err := t.store.InTx(ctx, func(r Repo) error {
		if err := r.LockTree(ctx); err != nil {
			return err
		}
		b, err := r.LockBatch(ctx, id)
		if err != nil {
			return err
		}
		out = Restored{RootType: b.RootType, RootID: b.RootID}
		now := t.now()
		switch b.RootType {
		case TypeFolder:
			err = t.restoreFolder(ctx, r, b, dest, destErr, now)
		case TypeList:
			err = t.restoreList(ctx, r, b, dest, destErr, now)
		case TypeItem:
			err = t.restoreItem(ctx, r, b, dest, destErr, now)
		default:
			err = fmt.Errorf("unknown trash root type %q", b.RootType)
		}
		if err != nil {
			return err
		}
		// The root is back; clear the rest of the batch, then the batch row itself
		// (nothing references it any more, D-051).
		if err := r.RestoreBatchFolders(ctx, b.ID, now); err != nil {
			return err
		}
		if err := r.RestoreBatchLists(ctx, b.ID, now); err != nil {
			return err
		}
		if err := r.RestoreBatchItems(ctx, b.ID, now); err != nil {
			return err
		}
		return r.DeleteBatch(ctx, b.ID)
	})
	return out, err
}

func (t *Service) restoreFolder(ctx context.Context, r Repo, b Batch, dest *Destination, destErr error, now time.Time) error {
	root, err := r.GetTrashedFolder(ctx, b.RootID, b.ID)
	if err != nil {
		return err
	}
	parent, err := t.folderTarget(ctx, r, root.ParentID, dest, destErr, &b.RootID)
	if err != nil {
		return err
	}
	next, err := r.NextFolderSortOrder(ctx, parent)
	if err != nil {
		return err
	}
	return r.RestoreFolderRoot(ctx, b.RootID, b.ID, parent, next, now)
}

func (t *Service) restoreList(ctx context.Context, r Repo, b Batch, dest *Destination, destErr error, now time.Time) error {
	root, err := r.GetTrashedList(ctx, b.RootID, b.ID)
	if err != nil {
		return err
	}
	folder, err := t.folderTarget(ctx, r, root.FolderID, dest, destErr, nil)
	if err != nil {
		return err
	}
	next, err := r.NextListSortOrder(ctx, folder)
	if err != nil {
		return err
	}
	return r.RestoreListRoot(ctx, b.RootID, b.ID, folder, next, now)
}

func (t *Service) restoreItem(ctx context.Context, r Repo, b Batch, dest *Destination, destErr error, now time.Time) error {
	root, err := r.GetTrashedItem(ctx, b.RootID, b.ID)
	if err != nil {
		return err
	}
	list := root.ListID
	if dest != nil {
		if fe := checkDestination(TypeItem, dest); fe != nil {
			return apperr.Validation(*fe)
		}
		list = *dest.ListID
		if destErr != nil {
			return destErr
		}
		if _, err := r.GetActiveList(ctx, list); err != nil {
			if isNotFound(err) {
				return apperr.DestinationUnavailable()
			}
			return fmt.Errorf("destination list: %w", err)
		}
	} else if _, err := r.GetActiveList(ctx, list); err != nil {
		if !isNotFound(err) {
			return fmt.Errorf("original list: %w", err)
		}
		return t.blockedBy(ctx, r, TypeList, list)
	}
	next, err := r.NextItemSortOrder(ctx, list)
	if err != nil {
		return err
	}
	return r.RestoreItemRoot(ctx, b.RootID, b.ID, list, next, now)
}

// folderTarget picks the parent folder (nil = workspace root) for a restored
// folder or list. rootFolder is set for a folder root (cycle check). Without a
// destination, the original parent must be active.
func (t *Service) folderTarget(ctx context.Context, r Repo, original *uuid.UUID, dest *Destination, destErr error, rootFolder *uuid.UUID) (*uuid.UUID, error) {
	if dest == nil {
		if original == nil {
			return nil, nil
		}
		if _, err := r.GetActiveFolder(ctx, *original); err != nil {
			if !isNotFound(err) {
				return nil, fmt.Errorf("original parent: %w", err)
			}
			return nil, t.blockedBy(ctx, r, TypeFolder, *original)
		}
		return original, nil
	}
	rootType := TypeList
	if rootFolder != nil {
		rootType = TypeFolder
	}
	if fe := checkDestination(rootType, dest); fe != nil {
		return nil, apperr.Validation(*fe)
	}
	parent := dest.FolderID
	if parent == nil {
		return nil, nil
	}
	if rootFolder != nil {
		// Into the folder itself or a descendant, whatever its state: a cycle,
		// not just an unavailable target (descendants are in the trash too).
		nodes, err := r.FolderParents(ctx)
		if err != nil {
			return nil, err
		}
		if inSubtreeOf(nodes, *parent, *rootFolder) {
			return nil, apperr.Cycle()
		}
	}
	if destErr != nil {
		return nil, destErr
	}
	if _, err := r.GetActiveFolder(ctx, *parent); err != nil {
		if isNotFound(err) {
			return nil, apperr.DestinationUnavailable()
		}
		return nil, fmt.Errorf("destination folder: %w", err)
	}
	return parent, nil
}

// blockedBy builds the parent_in_trash conflict for an original parent that is
// not active. A parent that no longer exists cannot be named (the foreign keys
// make that unreachable: purging a parent purges its trashed children too).
func (t *Service) blockedBy(ctx context.Context, r Repo, typ string, id uuid.UUID) error {
	var (
		state ParentState
		err   error
	)
	if typ == TypeFolder {
		state, err = r.GetAnyFolderState(ctx, id)
	} else {
		state, err = r.GetAnyListState(ctx, id)
	}
	if isNotFound(err) {
		return &apperr.ConflictError{Code: apperr.CodeParentInTrash}
	}
	if err != nil {
		return fmt.Errorf("blocking parent: %w", err)
	}
	return apperr.ParentInTrash(apperr.BlockingParent{Type: typ, ID: id.String(), Name: state.Name})
}

// checkDestination reports the field that does not name the key its root type
// needs (nil when it does).
func checkDestination(rootType string, d *Destination) *apperr.FieldError {
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

// destinationVisible checks, before the transaction, that the caller may see the folder or list a
// restore targets. One they cannot see is as unavailable as a missing one; the error is returned
// inside the transaction at the point where the existence check used to fail. A destination of
// the wrong shape is left to the field rules (checkDestination).
func (t *Service) destinationVisible(ctx context.Context, dest *Destination) error {
	if dest == nil || t.access == nil {
		return nil
	}
	var (
		ok  = true
		err error
	)
	switch {
	case dest.ListID != nil:
		ok, err = t.access.ListVisible(ctx, *dest.ListID)
	case dest.FolderID != nil:
		ok, err = t.access.FolderVisible(ctx, *dest.FolderID)
	}
	if err != nil {
		return fmt.Errorf("check destination: %w", err)
	}
	if !ok {
		return apperr.DestinationUnavailable()
	}
	return nil
}
