package tree

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/0xfederama/collections-app-backend/internal/apperr"
	"github.com/0xfederama/collections-app-backend/internal/database"
	"github.com/0xfederama/collections-app-backend/internal/database/sqlc"
)

// CreateFolder appends a folder after its active siblings. A missing or
// trashed parent is DestinationUnavailable.
func (s *Service) CreateFolder(ctx context.Context, name string, parent *uuid.UUID) (Folder, error) {
	name, fe := cleanName(name)
	if fe != nil {
		return Folder{}, apperr.Validation(*fe)
	}
	var out Folder
	err := database.InTx(ctx, s.db, func(q *sqlc.Queries) error {
		if err := s.lockTree(ctx, q); err != nil {
			return err
		}
		if err := s.requireActiveFolder(ctx, q, parent); err != nil {
			return err
		}
		next, err := q.NextFolderSortOrder(ctx, sqlc.NextFolderSortOrderParams{WorkspaceID: s.workspaceID, ParentFolderID: nullUUID(parent)})
		if err != nil {
			return fmt.Errorf("next folder sort order: %w", err)
		}
		row, err := q.InsertFolder(ctx, sqlc.InsertFolderParams{
			WorkspaceID: s.workspaceID, ParentFolderID: nullUUID(parent), Name: name, SortOrder: next, Now: s.now(),
		})
		if err != nil {
			return fmt.Errorf("insert folder: %w", err)
		}
		out = folderFromRow(row)
		return nil
	})
	return out, err
}

// GetFolder returns an active folder.
func (s *Service) GetFolder(ctx context.Context, id uuid.UUID) (Folder, error) {
	row, err := sqlc.New(s.db).GetFolder(ctx, sqlc.GetFolderParams{ID: id, WorkspaceID: s.workspaceID})
	if errors.Is(err, pgx.ErrNoRows) {
		return Folder{}, apperr.NotFound("folder")
	}
	if err != nil {
		return Folder{}, fmt.Errorf("get folder: %w", err)
	}
	return folderFromRow(row), nil
}

// UpdateFolder renames a folder (name nil = no field supplied: nothing changes
// and the version is not bumped). version is the caller's If-Match version.
func (s *Service) UpdateFolder(ctx context.Context, id uuid.UUID, version int, name *string) (Folder, error) {
	var newName string
	if name != nil {
		var fe *apperr.FieldError
		if newName, fe = cleanName(*name); fe != nil {
			return Folder{}, apperr.Validation(*fe)
		}
	}
	var out Folder
	err := database.InTx(ctx, s.db, func(q *sqlc.Queries) error {
		cur, err := s.lockFolder(ctx, q, id, version)
		if err != nil {
			return err
		}
		if name == nil {
			out = folderFromRow(cur)
			return nil
		}
		row, err := q.RenameFolder(ctx, sqlc.RenameFolderParams{Name: newName, Now: s.now(), ID: id, WorkspaceID: s.workspaceID})
		if err != nil {
			return fmt.Errorf("rename folder: %w", err)
		}
		out = folderFromRow(row)
		return nil
	})
	return out, err
}

// MoveFolder re-parents a folder (nil parent = root), appended last. Errors in
// order: NotFound, VersionConflict, DestinationUnavailable, Cycle (the
// destination is the folder itself or one of its descendants).
func (s *Service) MoveFolder(ctx context.Context, id uuid.UUID, version int, parent *uuid.UUID) (Folder, error) {
	var out Folder
	err := database.InTx(ctx, s.db, func(q *sqlc.Queries) error {
		if err := s.lockTree(ctx, q); err != nil {
			return err
		}
		if _, err := s.lockFolder(ctx, q, id, version); err != nil {
			return err
		}
		if err := s.requireActiveFolder(ctx, q, parent); err != nil {
			return err
		}
		if parent != nil {
			n, err := q.FolderIsSelfOrAncestor(ctx, sqlc.FolderIsSelfOrAncestorParams{FolderID: id, DestinationID: *parent, WorkspaceID: s.workspaceID})
			if err != nil {
				return fmt.Errorf("cycle check: %w", err)
			}
			if n > 0 {
				return apperr.Cycle()
			}
		}
		next, err := q.NextFolderSortOrder(ctx, sqlc.NextFolderSortOrderParams{WorkspaceID: s.workspaceID, ParentFolderID: nullUUID(parent)})
		if err != nil {
			return fmt.Errorf("next folder sort order: %w", err)
		}
		row, err := q.MoveFolder(ctx, sqlc.MoveFolderParams{ParentFolderID: nullUUID(parent), SortOrder: next, Now: s.now(), ID: id, WorkspaceID: s.workspaceID})
		if err != nil {
			return fmt.Errorf("move folder: %w", err)
		}
		out = folderFromRow(row)
		return nil
	})
	return out, err
}

// ReorderFolders rewrites the order of the child folders of parent (nil =
// root). ids must be exactly the active children, else OrderStale. A missing
// or trashed parent is NotFound.
func (s *Service) ReorderFolders(ctx context.Context, parent *uuid.UUID, ids []uuid.UUID) error {
	return database.InTx(ctx, s.db, func(q *sqlc.Queries) error {
		if err := s.lockTree(ctx, q); err != nil {
			return err
		}
		if parent != nil {
			if _, err := q.GetFolder(ctx, sqlc.GetFolderParams{ID: *parent, WorkspaceID: s.workspaceID}); err != nil {
				return notFoundOr(err, "folder")
			}
		}
		current, err := q.ListActiveFolderIDsByParent(ctx, sqlc.ListActiveFolderIDsByParentParams{WorkspaceID: s.workspaceID, ParentFolderID: nullUUID(parent)})
		if err != nil {
			return fmt.Errorf("list sibling folders: %w", err)
		}
		if !sameSet(current, ids) {
			return apperr.OrderStale()
		}
		if err := q.ReorderFolders(ctx, sqlc.ReorderFoldersParams{Now: s.now(), WorkspaceID: s.workspaceID, Ids: ids}); err != nil {
			return fmt.Errorf("reorder folders: %w", err)
		}
		return nil
	})
}

// lockFolder locks the active folder row and checks the caller's version.
func (s *Service) lockFolder(ctx context.Context, q *sqlc.Queries, id uuid.UUID, version int) (sqlc.Folder, error) {
	cur, err := q.LockFolder(ctx, sqlc.LockFolderParams{ID: id, WorkspaceID: s.workspaceID})
	if err != nil {
		return sqlc.Folder{}, notFoundOr(err, "folder")
	}
	if int(cur.Version) != version {
		return sqlc.Folder{}, apperr.VersionConflict(int(cur.Version))
	}
	return cur, nil
}

// requireActiveFolder reports DestinationUnavailable when id is set but is not
// an active folder. A nil id (the root) always exists.
func (s *Service) requireActiveFolder(ctx context.Context, q *sqlc.Queries, id *uuid.UUID) error {
	if id == nil {
		return nil
	}
	if _, err := q.GetFolder(ctx, sqlc.GetFolderParams{ID: *id, WorkspaceID: s.workspaceID}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return apperr.DestinationUnavailable()
		}
		return fmt.Errorf("get destination folder: %w", err)
	}
	return nil
}

func notFoundOr(err error, resource string) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return apperr.NotFound(resource)
	}
	return fmt.Errorf("get %s: %w", resource, err)
}

// sameSet reports whether got holds exactly the ids of want: same length, no
// duplicates, nothing foreign or missing.
func sameSet(want, got []uuid.UUID) bool {
	if len(want) != len(got) {
		return false
	}
	set := make(map[uuid.UUID]bool, len(want))
	for _, id := range want {
		set[id] = true
	}
	for _, id := range got {
		if !set[id] {
			return false
		}
		delete(set, id) // a repeated id finds nothing the second time
	}
	return len(set) == 0
}
